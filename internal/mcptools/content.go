package mcptools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxTopicBytes caps what read_topic will hand back in one call. A 60-page
// lecture transcript would otherwise land whole in the model's context and
// crowd out everything else in the conversation. Retrieval (build steps 3-5) is
// what removes this limit properly, by returning the relevant passages instead
// of the whole document.
const maxTopicBytes = 128 << 10

type contentTopic struct {
	TopicID     int    `json:"topic_id" jsonschema:"pass this to read_topic"`
	Title       string `json:"title"`
	Kind        string `json:"kind" jsonschema:"file for a document, link for an external URL"`
	Readable    bool   `json:"readable" jsonschema:"false means read_topic cannot return text for it"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty" jsonschema:"where a link topic points"`
	Updated     string `json:"updated,omitempty"`
}

type contentModule struct {
	Path        string         `json:"path" jsonschema:"module title, with parent modules separated by >"`
	Description string         `json:"description,omitempty"`
	Topics      []contentTopic `json:"topics"`
}

type listContentOutput struct {
	Course  string          `json:"course"`
	Modules []contentModule `json:"modules"`
	Note    string          `json:"note,omitempty"`
}

// browseContent flattens the content tree.
//
// Modules nest arbitrarily, but a nested output type would be a recursive
// schema for the model to walk and would bury a topic three levels down. A flat
// list of modules carrying a breadcrumb Path keeps every topic one hop from the
// top while preserving where it sits.
func (s *Server) browseContent(ctx context.Context, _ *mcp.CallToolRequest, args courseArgs) (*mcp.CallToolResult, listContentOutput, error) {
	course, err := s.resolveCourse(ctx, args.Course)
	if err != nil {
		return nil, listContentOutput{}, err
	}

	root, err := s.client.ContentRoot(ctx, course.Id)
	if err != nil {
		return nil, listContentOutput{}, fmt.Errorf("reading %s content: %w", course.Code, err)
	}

	out := listContentOutput{Course: course.Code}
	out.Modules = s.walk(root, nil)
	if len(out.Modules) == 0 {
		out.Note = fmt.Sprintf("%s has no content available to you.", course.Code)
	}
	return nil, out, nil
}

// walk descends the tree, dropping hidden modules along with everything filed
// under them. Skipping the subtree rather than just the module itself is the
// point: an unreleased module's topics are unreleased too, and recursing into
// it would surface them individually with no hint they were withheld.
func (s *Server) walk(objs []brightspace.ContentObject, trail []string) []contentModule {
	var out []contentModule
	for _, o := range objs {
		if !o.IsModule() || o.IsHidden {
			continue
		}
		path := append(append([]string{}, trail...), o.Title)

		mod := contentModule{
			Path:        strings.Join(path, " > "),
			Description: richText(o.Description),
			Topics:      []contentTopic{},
		}
		for _, child := range o.Structure {
			if child.IsModule() || child.IsHidden {
				continue
			}
			mod.Topics = append(mod.Topics, contentTopic{
				TopicID:     child.Id,
				Title:       child.Title,
				Kind:        topicKind(child.TopicType),
				Readable:    child.TopicType == brightspace.TopicFile,
				Description: richText(child.Description),
				URL:         linkURL(child),
				Updated:     s.at(child.LastModifiedDate),
			})
		}
		out = append(out, mod)
		out = append(out, s.walk(o.Structure, path)...)
	}
	return out
}

// visible reports whether topicID survived the walk, and is therefore material
// the student is allowed to see.
func visible(modules []contentModule, topicID int) bool {
	for _, m := range modules {
		for _, t := range m.Topics {
			if t.TopicID == topicID {
				return true
			}
		}
	}
	return false
}

func topicKind(t brightspace.TopicType) string {
	switch t {
	case brightspace.TopicFile:
		return "file"
	case brightspace.TopicLink:
		return "link"
	default:
		return "other"
	}
}

// linkURL exposes the destination of a link topic only. A file topic's Url is
// an internal tenant path with no meaning to a student, and showing it invites
// the model to cite a location nobody can open.
func linkURL(o brightspace.ContentObject) string {
	if o.TopicType == brightspace.TopicLink {
		return o.Url
	}
	return ""
}

type readTopicArgs struct {
	Course  string `json:"course" jsonschema:"course code from list_courses, for example MTH1003"`
	TopicID int    `json:"topic_id" jsonschema:"topic_id from browse_content"`
}

type readTopicOutput struct {
	Course    string `json:"course"`
	TopicID   int    `json:"topic_id"`
	MediaType string `json:"media_type"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty" jsonschema:"true when the document was longer than this tool returns"`
}

func (s *Server) readTopic(ctx context.Context, _ *mcp.CallToolRequest, args readTopicArgs) (*mcp.CallToolResult, readTopicOutput, error) {
	course, err := s.resolveCourse(ctx, args.Course)
	if err != nil {
		return nil, readTopicOutput{}, err
	}

	// Check visibility before fetching. browse_content filters withheld
	// material, but a topic id is just a number: asking for one directly would
	// otherwise walk straight past that filter and hand back the solution set
	// the instructor has not released. Reusing walk means this cannot drift out
	// of agreement with what browse_content is willing to show.
	root, err := s.client.ContentRoot(ctx, course.Id)
	if err != nil {
		return nil, readTopicOutput{}, fmt.Errorf("reading %s content: %w", course.Code, err)
	}
	if !visible(s.walk(root, nil), args.TopicID) {
		// Deliberately the same message whether the topic is withheld or does
		// not exist. Distinguishing them would confirm that an unreleased
		// document is there, which is most of what the guess was after.
		return nil, readTopicOutput{}, fmt.Errorf(
			"no topic %d available in %s — call browse_content for the topics you can read",
			args.TopicID, course.Code)
	}

	body, mediaType, err := s.client.TopicFile(ctx, course.Id, args.TopicID)
	switch {
	case errors.Is(err, brightspace.ErrNotFileTopic):
		return nil, readTopicOutput{}, fmt.Errorf(
			"topic %d in %s has no document behind it — it is a link or publisher topic, so there is nothing to read",
			args.TopicID, course.Code)
	case errors.Is(err, brightspace.ErrNotFound):
		return nil, readTopicOutput{}, fmt.Errorf(
			"no topic %d in %s — call browse_content for the topic ids that exist", args.TopicID, course.Code)
	case err != nil:
		return nil, readTopicOutput{}, fmt.Errorf("reading topic %d in %s: %w", args.TopicID, course.Code, err)
	}
	defer body.Close()

	// Binary formats need extraction, which is the content store's job and does
	// not exist yet. Say which format it is rather than returning mojibake and
	// letting the model narrate a document it cannot actually see.
	if !isTextual(mediaType) {
		return nil, readTopicOutput{}, fmt.Errorf(
			"topic %d in %s is %s, which needs text extraction before it can be read (build steps 3-5)",
			args.TopicID, course.Code, mediaType)
	}

	// One byte past the cap distinguishes "exactly at the limit" from "cut off".
	buf, err := io.ReadAll(io.LimitReader(body, maxTopicBytes+1))
	if err != nil {
		return nil, readTopicOutput{}, fmt.Errorf("reading topic %d in %s: %w", args.TopicID, course.Code, err)
	}

	out := readTopicOutput{Course: course.Code, TopicID: args.TopicID, MediaType: mediaType}
	if len(buf) > maxTopicBytes {
		buf, out.Truncated = buf[:maxTopicBytes], true
	}
	out.Text = string(buf)
	return nil, out, nil
}

// isTextual reports whether a media type can be handed to a model as-is.
func isTextual(mediaType string) bool {
	base, _, _ := strings.Cut(mediaType, ";")
	base = strings.ToLower(strings.TrimSpace(base))
	if strings.HasPrefix(base, "text/") {
		return true
	}
	switch base {
	case "application/json", "application/xml", "application/xhtml+xml":
		return true
	}
	return false
}
