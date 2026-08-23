package mcptools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultWindowDays is how far ahead list_deadlines looks when the caller does
// not say. Two weeks covers "this week" and "next week", which is most of what
// gets asked, without burying the near term under a month of noise.
const defaultWindowDays = 14

// mergeWindow is how far apart two entries with the same course and title can
// be and still be considered the same obligation. D2L auto-creates a calendar
// event for an assignment's due date, and an exam's calendar entry starts when
// the room opens while its quiz closes later — so the timestamps rarely match
// exactly even when the thing is identical.
const mergeWindow = 24 * time.Hour

// deadline is one dated obligation, whatever produced it.
type deadline struct {
	Course   string `json:"course"`
	Title    string `json:"title"`
	Kind     string `json:"kind" jsonschema:"assignment, quiz, or event"`
	Due      string `json:"due" jsonschema:"local time in the configured timezone"`
	Details  string `json:"details,omitempty"`
	Location string `json:"location,omitempty"`
	AllDay   bool   `json:"all_day,omitempty"`

	// Submitted is set only for assignments, and is a pointer so that "this is
	// not an assignment" stays distinct from "this assignment has nothing
	// submitted". Reported from the student's own file count, so it means
	// something was uploaded — not that it was uploaded on time or is correct.
	Submitted *bool `json:"submitted,omitempty"`

	at  time.Time
	key string
}

type deadlinesArgs struct {
	Course   string `json:"course,omitempty" jsonschema:"course code to limit to; omit for every enrolled course"`
	Days     int    `json:"days,omitempty" jsonschema:"how many days ahead to look, default 14"`
	PastDays int    `json:"past_days,omitempty" jsonschema:"how many days back to also include, default 0; use this for what did I miss"`
}

type listDeadlinesOutput struct {
	From      string     `json:"from" jsonschema:"start of the window searched"`
	To        string     `json:"to" jsonschema:"end of the window searched"`
	Deadlines []deadline `json:"deadlines"`
	Note      string     `json:"note,omitempty"`
}

// listDeadlines merges the three routes that carry due dates into one list.
//
// They overlap heavily — Brightspace mirrors an assignment's due date into the
// calendar — so a naive concatenation reports most things twice. See merge.
func (s *Server) listDeadlines(ctx context.Context, _ *mcp.CallToolRequest, args deadlinesArgs) (*mcp.CallToolResult, listDeadlinesOutput, error) {
	courses, err := s.scope(ctx, args.Course)
	if err != nil {
		return nil, listDeadlinesOutput{}, err
	}

	days := args.Days
	if days <= 0 {
		days = defaultWindowDays
	}
	now := s.now()
	from := now.AddDate(0, 0, -max(args.PastDays, 0))
	to := now.AddDate(0, 0, days)

	out := listDeadlinesOutput{From: s.at(from), To: s.at(to)}

	// Assignments and quizzes first: they carry instructions, submission state
	// and attempt limits, so when a calendar event duplicates one of them the
	// richer record is the one already in hand.
	var found []deadline
	ids := make([]int, 0, len(courses))
	byID := make(map[int]string, len(courses))
	for _, c := range courses {
		ids = append(ids, c.Id)
		byID[c.Id] = c.Code

		folders, err := s.client.DropboxFolders(ctx, c.Id)
		if err != nil {
			return nil, listDeadlinesOutput{}, fmt.Errorf("reading %s assignments: %w", c.Code, err)
		}
		for _, f := range folders {
			if f.IsHidden || f.DueDate == nil {
				continue
			}
			submitted := f.TotalFiles > 0
			found = append(found, deadline{
				Course:    c.Code,
				Title:     f.Name,
				Kind:      "assignment",
				Details:   richText(f.CustomInstructions),
				Submitted: &submitted,
				at:        *f.DueDate,
			})
		}

		quizzes, err := s.client.Quizzes(ctx, c.Id)
		if err != nil {
			return nil, listDeadlinesOutput{}, fmt.Errorf("reading %s quizzes: %w", c.Code, err)
		}
		for _, q := range quizzes {
			// Inactive is the quiz equivalent of hidden.
			if !q.IsActive {
				continue
			}
			// A quiz with neither a due date nor a closing time has no
			// deadline — it is practice material, not an obligation.
			when := q.DueDate
			if when == nil {
				when = q.EndDate
			}
			if when == nil {
				continue
			}
			found = append(found, deadline{
				Course: c.Code,
				Title:  q.Name,
				Kind:   "quiz",
				at:     *when,
			})
		}
	}

	// One call covers every course, which is what makes cross-course planning
	// cheap rather than a fan-out across enrollments.
	events, err := s.client.MyEvents(ctx, ids, from, to)
	if err != nil {
		return nil, listDeadlinesOutput{}, fmt.Errorf("reading calendar: %w", err)
	}
	calendar := make([]deadline, 0, len(events))
	for _, e := range events {
		code, ok := byID[e.OrgUnitId]
		if !ok {
			// An event from an org unit outside the requested scope.
			continue
		}
		calendar = append(calendar, deadline{
			Course:   code,
			Title:    e.Title,
			Kind:     "event",
			Details:  strings.TrimSpace(e.Description),
			Location: e.Location,
			AllDay:   e.IsAllDay,
			at:       e.StartDateTime,
		})
	}

	merged := merge(found, calendar)

	out.Deadlines = make([]deadline, 0, len(merged))
	for _, d := range merged {
		if d.at.Before(from) || d.at.After(to) {
			continue
		}
		d.Due = s.at(d.at)
		out.Deadlines = append(out.Deadlines, d)
	}
	sort.SliceStable(out.Deadlines, func(i, j int) bool {
		return out.Deadlines[i].at.Before(out.Deadlines[j].at)
	})

	if len(out.Deadlines) == 0 {
		// Naming the window turns an empty answer into a diagnosable one:
		// "nothing is due" and "you asked about the wrong fortnight" look
		// identical otherwise.
		out.Note = fmt.Sprintf("Nothing due between %s and %s.", out.From, out.To)
	}
	return nil, out, nil
}

// merge folds calendar events into the assignment and quiz entries they
// duplicate, keeping the richer record and taking the fields only the calendar
// has — a room number, and the instructor's description of what an exam covers.
//
// Two entries are the same obligation when they belong to the same course, have
// the same title once "due"-style suffixes are stripped, and fall within
// mergeWindow of each other. The time bound is what stops a recurring title
// ("Quiz") from collapsing a semester's worth of them into one.
func merge(primary, calendar []deadline) []deadline {
	out := make([]deadline, len(primary))
	copy(out, primary)
	for i := range out {
		out[i].key = normalizeTitle(out[i].Title)
	}

	for _, e := range calendar {
		key := normalizeTitle(e.Title)
		matched := false
		for i := range out {
			if out[i].Course != e.Course || out[i].key != key {
				continue
			}
			if absDuration(out[i].at.Sub(e.at)) > mergeWindow {
				continue
			}
			if out[i].Details == "" {
				out[i].Details = e.Details
			}
			if out[i].Location == "" {
				out[i].Location = e.Location
			}
			matched = true
			break
		}
		if !matched {
			e.key = key
			out = append(out, e)
		}
	}
	return out
}

// normalizeTitle strips the phrasing the calendar adds to a due date so
// "Homework 2 due" and the "Homework 2" folder compare equal.
func normalizeTitle(s string) string {
	s = spacePattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), " ")
	for _, suffix := range []string{" is due", " due", " deadline", " submission"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimSpace(s)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// scope resolves the course argument to the set of courses a tool should read.
// An empty code means every enrolled course, which is what makes the
// cross-course questions answerable in one call.
func (s *Server) scope(ctx context.Context, code string) ([]brightspace.OrgUnitInfo, error) {
	if strings.TrimSpace(code) == "" {
		return s.courseList(ctx)
	}
	course, err := s.resolveCourse(ctx, code)
	if err != nil {
		return nil, err
	}
	return []brightspace.OrgUnitInfo{course}, nil
}

type assignment struct {
	Title        string `json:"title"`
	Due          string `json:"due,omitempty" jsonschema:"empty means the assignment has no due date"`
	Instructions string `json:"instructions,omitempty"`
	Submitted    bool   `json:"submitted" jsonschema:"whether the student has uploaded anything to this folder"`
	FileCount    int    `json:"file_count"`
	OpensAt      string `json:"opens_at,omitempty"`
	ClosesAt     string `json:"closes_at,omitempty" jsonschema:"after this the folder stops accepting submissions, which can be later than the due date"`
}

type listAssignmentsOutput struct {
	Course      string       `json:"course"`
	Assignments []assignment `json:"assignments"`
	Note        string       `json:"note,omitempty"`
}

func (s *Server) listAssignments(ctx context.Context, _ *mcp.CallToolRequest, args courseArgs) (*mcp.CallToolResult, listAssignmentsOutput, error) {
	course, err := s.resolveCourse(ctx, args.Course)
	if err != nil {
		return nil, listAssignmentsOutput{}, err
	}

	folders, err := s.client.DropboxFolders(ctx, course.Id)
	if err != nil {
		return nil, listAssignmentsOutput{}, fmt.Errorf("reading %s assignments: %w", course.Code, err)
	}

	out := listAssignmentsOutput{Course: course.Code, Assignments: make([]assignment, 0, len(folders))}
	for _, f := range folders {
		if f.IsHidden {
			continue
		}
		out.Assignments = append(out.Assignments, assignment{
			Title:        f.Name,
			Due:          s.when(f.DueDate),
			Instructions: richText(f.CustomInstructions),
			Submitted:    f.TotalFiles > 0,
			FileCount:    f.TotalFiles,
			OpensAt:      s.when(f.Availability.StartDate),
			ClosesAt:     s.when(f.Availability.EndDate),
		})
	}
	if len(out.Assignments) == 0 {
		out.Note = fmt.Sprintf("%s has no assignment folders available to you.", course.Code)
	}
	return nil, out, nil
}
