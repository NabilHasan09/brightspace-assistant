package mcptools

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultAnnouncementDays is how far back list_announcements looks by default.
// A month covers "did I miss anything" without dredging up the syllabus post
// from week one every time.
const defaultAnnouncementDays = 30

type announcement struct {
	Course  string `json:"course"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Posted  string `json:"posted,omitempty" jsonschema:"empty means the announcement carries no date"`
	Global  bool   `json:"global,omitempty" jsonschema:"an institution-wide notice rather than a course one"`
	Expired bool   `json:"expired,omitempty" jsonschema:"no longer displayed in Brightspace, but may still be the answer to what changed"`

	at time.Time
}

type announcementsArgs struct {
	Course string `json:"course,omitempty" jsonschema:"course code to limit to; omit for every enrolled course"`
	Days   int    `json:"days,omitempty" jsonschema:"how many days back to look, default 30"`
}

type listAnnouncementsOutput struct {
	Since         string         `json:"since" jsonschema:"the oldest announcement date included"`
	Announcements []announcement `json:"announcements"`
	Note          string         `json:"note,omitempty"`
}

func (s *Server) listAnnouncements(ctx context.Context, _ *mcp.CallToolRequest, args announcementsArgs) (*mcp.CallToolResult, listAnnouncementsOutput, error) {
	courses, err := s.scope(ctx, args.Course)
	if err != nil {
		return nil, listAnnouncementsOutput{}, err
	}

	days := args.Days
	if days <= 0 {
		days = defaultAnnouncementDays
	}
	now := s.now()
	since := now.AddDate(0, 0, -days)

	out := listAnnouncementsOutput{Since: s.at(since)}
	for _, c := range courses {
		items, err := s.client.NewsItems(ctx, c.Id, since)
		if err != nil {
			return nil, listAnnouncementsOutput{}, fmt.Errorf("reading %s announcements: %w", c.Code, err)
		}
		for _, n := range items {
			// An unpublished item is a draft the instructor has not released.
			if !n.IsPublished {
				continue
			}
			// A future StartDate is a scheduled announcement that Brightspace
			// is not displaying yet. Same withholding as a draft, arriving
			// through a different field — the instructor picked a date and the
			// student is not meant to see it before then.
			if n.StartDate != nil && n.StartDate.After(now) {
				continue
			}
			a := announcement{
				Course: c.Code,
				Title:  n.Title,
				Body:   richText(n.Body),
				Posted: s.when(n.StartDate),
				Global: n.IsGlobal,
			}
			if n.StartDate != nil {
				a.at = *n.StartDate
			}
			// Expired announcements are kept rather than dropped. "Exam 2 moved
			// to Room 214" stops displaying the day the exam passes, and that is
			// exactly when a student asks what changed — so flag it and let the
			// model say it is no longer posted.
			a.Expired = n.EndDate != nil && n.EndDate.Before(now)
			out.Announcements = append(out.Announcements, a)
		}
	}

	// Newest first, undated last: an announcement with no date is standing
	// boilerplate and must not sit above something that actually just changed.
	sort.SliceStable(out.Announcements, func(i, j int) bool {
		return out.Announcements[i].at.After(out.Announcements[j].at)
	})

	if len(out.Announcements) == 0 {
		out.Note = fmt.Sprintf("No announcements posted since %s.", out.Since)
	}
	return nil, out, nil
}
