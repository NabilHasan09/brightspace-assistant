// Package mcptools exposes the Brightspace data layer as MCP tools. No model
// calls happen here — these are the raw tools a specialist calls, and that
// cmd/chat consumes directly.
//
// Three things this layer owns that the brightspace package deliberately does
// not:
//
// Course names. Students say "MTH1003"; every Valence route wants 6001. Tools
// take the code and resolve it here, so an orchestrator never has to call
// list_courses first just to translate.
//
// Visibility. The brightspace package mirrors the API and returns hidden
// modules, unpublished announcements, and hidden dropbox folders unfiltered.
// This is the layer that drops them. Everything below is a faithful mirror;
// everything above is student-facing, and an assistant that surfaces an
// unreleased exam because the API happened to return it is the failure this
// boundary exists to prevent. See dropHidden.
//
// Time. Deadlines are answered against a clock and a timezone, both injected.
// A student in New York needs "due 11:59 PM Thursday", not the 04:59Z that D2L
// returns, and the fixtures are dated to a semester that has already passed —
// so tests and demos need to ask "what's due" as of a date that isn't today.
//
// Build step 6.
package mcptools

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server holds everything the tool handlers need. One per process.
type Server struct {
	client brightspace.Client
	now    func() time.Time
	loc    *time.Location
	roles  map[int]string

	courses courseCache
}

// Option configures a Server.
type Option func(*Server)

// WithClock replaces the wall clock. Deadline windows are computed from it, so
// this is what lets tests ask "what's due this week" as of a fixed date, and
// what lets a demo against the fixtures ask as of the semester they describe.
func WithClock(now func() time.Time) Option {
	return func(s *Server) { s.now = now }
}

// WithLocation sets the timezone deadlines are reported in. D2L returns UTC,
// and a 04:59Z due date is 11:59 PM the previous day in New York — a whole
// day's difference in a student's head, which makes this a correctness setting
// rather than a formatting preference.
func WithLocation(loc *time.Location) Option {
	return func(s *Server) { s.loc = loc }
}

// WithRoleNames maps an institution's numeric classlist role ids to names.
//
// D2L assigns these per institution and returns no name alongside them, so
// "who is my TA?" is unanswerable until someone supplies this mapping. Without
// it get_classlist reports names and says the roles are unknown, which is the
// correct degradation — inferring a TA from a role number would confidently
// name the wrong person.
func WithRoleNames(roles map[int]string) Option {
	return func(s *Server) { s.roles = roles }
}

// New builds a Server over any Client — MockClient for fixtures, LiveClient
// once a tenant exists. Defaults to the real clock and UTC.
func New(client brightspace.Client, opts ...Option) *Server {
	s := &Server{
		client: client,
		now:    time.Now,
		loc:    time.UTC,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Register adds every data tool to srv.
//
// This function is the registry: the tool list and the handler wiring are the
// same lines, so a tool cannot be defined and left unserved. Descriptions are
// written as trigger conditions ("Call this when the user asks…") rather than
// labels, because in data mode the description is the entire routing signal —
// there is no router, and the orchestrator picks from these strings alone.
func (s *Server) Register(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_courses",
		Description: "List the student's enrolled courses with their course codes. " +
			"Call this when the user asks what classes they are taking, or when you need " +
			"a course code for another tool and the user named a course loosely " +
			"(\"my calculus class\"). Every other tool takes the code this returns.",
	}, s.listCourses)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_grades",
		Description: "Get the student's gradebook for one course: every graded item, the " +
			"score, and any instructor feedback, plus the final grade if the course " +
			"releases one. Call this when the user asks about a grade, a score, how they " +
			"did on something, what feedback they got, or what they need on a remaining " +
			"assessment.",
	}, s.getGrades)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_deadlines",
		Description: "List everything due in a time window, across all courses at once or " +
			"one course, merging calendar events, assignment folders, and quizzes into a " +
			"single list sorted by when it is due. Call this when the user asks what is " +
			"due, what is coming up, when an exam is, what to work on, or what they should " +
			"prioritize.",
	}, s.listDeadlines)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_assignments",
		Description: "List one course's assignment submission folders with their " +
			"requirements, due dates, and whether the student has submitted. Call this " +
			"when the user asks what an assignment requires, how to submit, how many " +
			"attempts they get, or whether a submission went through.",
	}, s.listAssignments)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "browse_content",
		Description: "Browse a course's content tree: modules (weeks/units) and the " +
			"documents filed under them, with titles and dates. Call this when the user " +
			"asks what material exists, what was covered in a week, whether something " +
			"specific has been posted, or when you need a topic_id to read a document. " +
			"This lists titles only — use read_topic to read a document's contents.",
	}, s.browseContent)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "read_topic",
		Description: "Read the text of one course document, identified by the topic_id " +
			"from browse_content. Call this after browse_content when the user asks you to " +
			"summarize, explain, or quote from specific course material.",
	}, s.readTopic)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_announcements",
		Description: "List a course's announcements, newest first. Call this when the user " +
			"asks about announcements, updates, news, whether they missed anything, or " +
			"whether the instructor changed a date or a plan.",
	}, s.listAnnouncements)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_discussions",
		Description: "List a course's discussion forums and the threads inside them. Call " +
			"this when the user asks about a discussion board or a discussion prompt, or " +
			"when you need a forum_id and topic_id to read a thread.",
	}, s.listDiscussions)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "read_discussion",
		Description: "Read the posts in one discussion thread, oldest first, with replies " +
			"linked to what they answer. Call this after list_discussions when the user " +
			"asks what the discussion says, whether anyone replied to them, or to " +
			"summarize a thread.",
	}, s.readDiscussion)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_classlist",
		Description: "List the people enrolled in a course — instructors, TAs, and " +
			"classmates — with contact details where the institution publishes them. Call " +
			"this when the user asks who teaches a course, who their TA is, or how to " +
			"reach them. Contact fields are often withheld by the institution; report the " +
			"name and say the rest is not published rather than guessing.",
	}, s.getClasslist)
}

// courseArgs is embedded by every single-course tool so the parameter is named
// and described identically everywhere.
type courseArgs struct {
	Course string `json:"course" jsonschema:"course code from list_courses, for example MTH1003"`
}

// richText flattens D2L's paired plain/HTML block to something worth handing a
// model. Text is authoritative; the HTML fallback is a crude tag strip, which
// is enough for instructor-pasted prose and not intended to survive real markup.
func richText(rt brightspace.RichText) string {
	if t := strings.TrimSpace(rt.Text); t != "" {
		return t
	}
	if rt.Html == "" {
		return ""
	}
	stripped := tagPattern.ReplaceAllString(rt.Html, " ")
	return strings.TrimSpace(spacePattern.ReplaceAllString(html.UnescapeString(stripped), " "))
}

var (
	tagPattern   = regexp.MustCompile(`<[^>]*>`)
	spacePattern = regexp.MustCompile(`\s+`)
)

// when formats an optional timestamp in the configured zone. Empty means the
// field was genuinely absent, which is different from a zero date and must not
// be rendered as one — "due 0001-01-01" reads as a real deadline.
func (s *Server) when(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return s.at(*t)
}

// at formats a timestamp that is always present.
func (s *Server) at(t time.Time) string {
	return t.In(s.loc).Format("2006-01-02 15:04 MST")
}

// Every handler below drops objects carrying IsHidden, and announcements that
// are not published. The brightspace package returns them because it mirrors
// the API; this is where they stop. An unreleased exam, a draft announcement
// and a hidden module are the same mistake, and surfacing any of them is worse
// than returning nothing.
//
// IsLocked is deliberately not filtered. A locked item is visible but not yet
// openable, and a student asking what is coming up should still see that it
// exists.

// errNoCourse builds the not-found message, listing what does exist so the
// model can correct itself in one turn instead of guessing again.
//
// Handlers return this as an ordinary error, which the SDK packs into a tool
// result with IsError set. That keeps a wrong course code a conversation the
// model can recover from rather than a protocol failure underneath it.
func errNoCourse(code string, known []string) error {
	if len(known) == 0 {
		return fmt.Errorf("no course matches %q, and no enrolled courses were found", code)
	}
	return fmt.Errorf("no course matches %q; enrolled courses are: %s", code, strings.Join(known, ", "))
}
