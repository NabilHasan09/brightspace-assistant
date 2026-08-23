package mcptools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type discussionThread struct {
	ForumID int    `json:"forum_id" jsonschema:"pass this to read_discussion"`
	TopicID int    `json:"topic_id" jsonschema:"pass this to read_discussion"`
	Forum   string `json:"forum"`
	Title   string `json:"title"`
	Prompt  string `json:"prompt,omitempty" jsonschema:"what the instructor asked for in this thread"`
	Locked  bool   `json:"locked,omitempty" jsonschema:"readable but closed to new posts"`
	Opens   string `json:"opens,omitempty"`
	Closes  string `json:"closes,omitempty"`
}

type listDiscussionsOutput struct {
	Course  string             `json:"course"`
	Threads []discussionThread `json:"threads"`
	Note    string             `json:"note,omitempty"`
}

// listDiscussions returns threads rather than a forum tree. A student asks
// about a discussion, not about the container it lives in, and flattening means
// the result already carries both ids read_discussion needs.
func (s *Server) listDiscussions(ctx context.Context, _ *mcp.CallToolRequest, args courseArgs) (*mcp.CallToolResult, listDiscussionsOutput, error) {
	course, err := s.resolveCourse(ctx, args.Course)
	if err != nil {
		return nil, listDiscussionsOutput{}, err
	}

	forums, err := s.client.DiscussionForums(ctx, course.Id)
	if err != nil {
		return nil, listDiscussionsOutput{}, fmt.Errorf("reading %s discussions: %w", course.Code, err)
	}

	out := listDiscussionsOutput{Course: course.Code, Threads: []discussionThread{}}
	for _, f := range forums {
		// Skipping the forum skips its threads without asking for them, which
		// is both cheaper and safer than filtering them one by one afterwards.
		if f.IsHidden {
			continue
		}
		topics, err := s.client.DiscussionTopics(ctx, course.Id, f.ForumId)
		if err != nil {
			return nil, listDiscussionsOutput{}, fmt.Errorf("reading %s forum %q: %w", course.Code, f.Name, err)
		}
		for _, t := range topics {
			if t.IsHidden {
				continue
			}
			out.Threads = append(out.Threads, discussionThread{
				ForumID: f.ForumId,
				TopicID: t.TopicId,
				Forum:   f.Name,
				Title:   t.Name,
				Prompt:  richText(t.Description),
				Locked:  t.IsLocked,
				Opens:   s.when(t.StartDate),
				Closes:  s.when(t.EndDate),
			})
		}
	}
	if len(out.Threads) == 0 {
		out.Note = fmt.Sprintf("%s has no discussion threads available to you.", course.Code)
	}
	return nil, out, nil
}

type discussionPost struct {
	PostID  int    `json:"post_id"`
	Author  string `json:"author"`
	Posted  string `json:"posted"`
	Subject string `json:"subject,omitempty"`
	Message string `json:"message"`
	Edited  string `json:"edited,omitempty"`

	// ReplyTo carries the thread shape: D2L returns posts flat, and this is the
	// only field that says what a post is answering. Absent on a thread starter.
	ReplyTo *int `json:"reply_to,omitempty" jsonschema:"post_id this is a reply to"`

	Unread bool `json:"unread,omitempty" jsonschema:"whether the student has not read this post; unverified against a live tenant, so treat false as unknown rather than definitely read"`
}

type readDiscussionArgs struct {
	Course  string `json:"course" jsonschema:"course code from list_courses, for example MTH1003"`
	ForumID int    `json:"forum_id" jsonschema:"forum_id from list_discussions"`
	TopicID int    `json:"topic_id" jsonschema:"topic_id from list_discussions"`
}

type readDiscussionOutput struct {
	Course      string           `json:"course"`
	Posts       []discussionPost `json:"posts" jsonschema:"oldest first"`
	UnreadCount int              `json:"unread_count"`
	Note        string           `json:"note,omitempty"`
}

func (s *Server) readDiscussion(ctx context.Context, _ *mcp.CallToolRequest, args readDiscussionArgs) (*mcp.CallToolResult, readDiscussionOutput, error) {
	course, err := s.resolveCourse(ctx, args.Course)
	if err != nil {
		return nil, readDiscussionOutput{}, err
	}

	posts, err := s.client.DiscussionPosts(ctx, course.Id, args.ForumID, args.TopicID)
	if err != nil {
		return nil, readDiscussionOutput{}, fmt.Errorf(
			"reading thread %d in %s: %w — call list_discussions for the ids that exist",
			args.TopicID, course.Code, err)
	}

	out := readDiscussionOutput{Course: course.Code, Posts: []discussionPost{}}
	deleted := 0
	for _, p := range posts {
		// D2L returns deleted posts with their text intact. Someone retracted
		// that, so it does not go to the model — but the count does, because a
		// reply to a deleted post otherwise reads as a non sequitur.
		if p.IsDeleted {
			deleted++
			continue
		}
		author := p.DisplayName
		if p.IsAnonymous {
			// The payload can still carry a name on an anonymous post,
			// depending on who is asking. Never pass it through.
			author = "Anonymous"
		}
		if !p.IsRead {
			out.UnreadCount++
		}
		out.Posts = append(out.Posts, discussionPost{
			PostID:  p.PostId,
			Author:  author,
			Posted:  s.at(p.DatePosted),
			Subject: p.Subject,
			Message: richText(p.Message),
			Edited:  s.when(p.LastEditedDate),
			ReplyTo: p.ParentPostId,
			Unread:  !p.IsRead,
		})
	}

	switch {
	case len(out.Posts) == 0 && deleted > 0:
		out.Note = fmt.Sprintf("Every post in this thread (%d) has been deleted.", deleted)
	case len(out.Posts) == 0:
		out.Note = "Nobody has posted in this thread yet."
	case deleted == 1:
		out.Note = "1 deleted post is not shown."
	case deleted > 1:
		out.Note = fmt.Sprintf("%d deleted posts are not shown.", deleted)
	}
	return nil, out, nil
}
