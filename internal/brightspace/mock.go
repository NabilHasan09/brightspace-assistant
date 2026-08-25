package brightspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
)

// extraTypes fills gaps in Go's builtin MIME table. Markdown has no entry
// there and none of the system mime.types files reliably supply one, so it
// would otherwise resolve to application/octet-stream — the binary branch,
// which is exactly the wrong route for a text format.
var extraTypes = map[string]string{
	".md":       "text/markdown; charset=utf-8",
	".markdown": "text/markdown; charset=utf-8",
}

// mimeForExt resolves a file extension to a MIME type, falling back to
// octet-stream. Only the mock needs this: the live API supplies a Content-Type
// header and has no extension to guess from.
func mimeForExt(ext string) string {
	if t, ok := extraTypes[strings.ToLower(ext)]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

// MockClient serves fixtures from an fs.FS laid out as:
//
//	enrollments.json
//	courses/<CODE>/content-root.json
//	courses/<CODE>/files/<name>
//
// It is load-bearing, not decorative: most of this project is built and tested
// against it, so fixtures must match the documented Valence schemas rather
// than whatever shape is convenient here. The risk that buys is real — if a
// fixture misreads the schema, the mock and every test above it agree with
// each other and stay green until real credentials arrive.
//
// content-root.json is the single source of truth for a course's tree.
// ModuleStructure and TopicFile both derive their answers from it rather than
// reading separate files, because fixtures that must agree with each other
// eventually will not.
type MockClient struct {
	fsys fs.FS
}

func NewMockClient(fsys fs.FS) *MockClient {
	return &MockClient{fsys: fsys}
}

func (m *MockClient) readJSON(name string, dst any) error {
	b, err := fs.ReadFile(m.fsys, name)
	if err != nil {
		return fmt.Errorf("mock: read %s: %w", name, err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("mock: parse %s: %w", name, err)
	}
	return nil
}

func (m *MockClient) MyEnrollments(ctx context.Context) ([]MyOrgUnitInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var resp MyEnrollmentsResponse
	if err := m.readJSON("enrollments.json", &resp); err != nil {
		return nil, err
	}
	// Non-nil so an empty fixture compares equal to the live client's result.
	out := make([]MyOrgUnitInfo, 0, len(resp.Items))
	return append(out, resp.Items...), nil
}

// courseCode maps an org unit id to the fixture directory name. The live API
// needs no such step; this exists only so fixtures can be filed under a
// human-readable "MTH1003" instead of "6001".
func (m *MockClient) courseCode(ctx context.Context, orgUnitID int) (string, error) {
	units, err := m.MyEnrollments(ctx)
	if err != nil {
		return "", err
	}
	for _, u := range units {
		if u.OrgUnit.Id == orgUnitID {
			return u.OrgUnit.Code, nil
		}
	}
	return "", fmt.Errorf("mock: org unit %d: %w", orgUnitID, ErrNotFound)
}

// readCourseJSON loads a fixture filed under the course's directory.
func (m *MockClient) readCourseJSON(ctx context.Context, orgUnitID int, name string, dst any) error {
	code, err := m.courseCode(ctx, orgUnitID)
	if err != nil {
		return err
	}
	return m.readJSON(path.Join("courses", code, name), dst)
}

func (m *MockClient) ContentRoot(ctx context.Context, orgUnitID int) ([]Module, error) {
	var mods []Module
	if err := m.readCourseJSON(ctx, orgUnitID, "content-root.json", &mods); err != nil {
		return nil, err
	}
	return mods, nil
}

func (m *MockClient) ModuleStructure(ctx context.Context, orgUnitID, moduleID int) ([]ContentObject, error) {
	root, err := m.ContentRoot(ctx, orgUnitID)
	if err != nil {
		return nil, err
	}
	if found, ok := findObject(root, moduleID); ok && found.IsModule() {
		return found.Structure, nil
	}
	return nil, fmt.Errorf("mock: module %d in org unit %d: %w", moduleID, orgUnitID, ErrNotFound)
}

func (m *MockClient) TopicFile(ctx context.Context, orgUnitID, topicID int) (io.ReadCloser, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	code, err := m.courseCode(ctx, orgUnitID)
	if err != nil {
		return nil, "", err
	}
	root, err := m.ContentRoot(ctx, orgUnitID)
	if err != nil {
		return nil, "", err
	}
	topic, ok := findObject(root, topicID)
	if !ok || topic.IsModule() {
		return nil, "", fmt.Errorf("mock: topic %d in org unit %d: %w", topicID, orgUnitID, ErrNotFound)
	}
	if topic.TopicType != TopicFile {
		return nil, "", fmt.Errorf("mock: topic %d (%s): %w", topicID, topic.Title, ErrNotFileTopic)
	}

	// A file topic's Url is a tenant-relative content path; only its basename
	// is meaningful to the fixture layout.
	name := path.Join("courses", code, "files", path.Base(topic.Url))
	f, err := m.fsys.Open(name)
	if err != nil {
		return nil, "", fmt.Errorf("mock: open %s: %w", name, err)
	}

	return f, mimeForExt(path.Ext(topic.Url)), nil
}

func (m *MockClient) MyGradeValues(ctx context.Context, orgUnitID int) ([]GradeValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var grades []GradeValue
	if err := m.readCourseJSON(ctx, orgUnitID, "grades.json", &grades); err != nil {
		return nil, err
	}
	return grades, nil
}

func (m *MockClient) MyFinalGrade(ctx context.Context, orgUnitID int) (*GradeValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var final GradeValue
	err := m.readCourseJSON(ctx, orgUnitID, "final-grade.json", &final)
	switch {
	// A missing fixture models a course that does not release a final grade,
	// which is common mid-semester. That is not an error condition, but it
	// must not be reported as a zero either.
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("mock: final grade for org unit %d: %w", orgUnitID, ErrNotFound)
	case err != nil:
		return nil, err
	}
	return &final, nil
}

func (m *MockClient) DropboxFolders(ctx context.Context, orgUnitID int) ([]DropboxFolder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var folders []DropboxFolder
	if err := m.readCourseJSON(ctx, orgUnitID, "dropbox.json", &folders); err != nil {
		return nil, err
	}
	return folders, nil
}

// MyEvents reads one cross-course fixture rather than per-course files,
// mirroring the live endpoint's single call over a CSV of org unit ids.
//
// The range test is StartDateTime in [start, end). That excludes an event that
// began before the window and is still running — fine for "what is due this
// week", possibly wrong for a multi-day event. Verify against a live tenant
// before relying on it.
func (m *MockClient) MyEvents(ctx context.Context, orgUnitIDs []int, start, end time.Time) ([]CalendarEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var all []CalendarEvent
	if err := m.readJSON("calendar-events.json", &all); err != nil {
		return nil, err
	}

	// Empty means every enrolled course.
	want := make(map[int]bool, len(orgUnitIDs))
	for _, id := range orgUnitIDs {
		want[id] = true
	}

	out := make([]CalendarEvent, 0, len(all))
	for _, e := range all {
		if len(want) > 0 && !want[e.OrgUnitId] {
			continue
		}
		if e.StartDateTime.Before(start) || !e.StartDateTime.Before(end) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartDateTime.Before(out[j].StartDateTime)
	})
	return out, nil
}

func (m *MockClient) NewsItems(ctx context.Context, orgUnitID int, since time.Time) ([]NewsItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var all []NewsItem
	if err := m.readCourseJSON(ctx, orgUnitID, "news.json", &all); err != nil {
		return nil, err
	}

	out := make([]NewsItem, 0, len(all))
	for _, n := range all {
		// A nil StartDate cannot be ruled out of the window, so keep it.
		if n.StartDate != nil && n.StartDate.Before(since) {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].sortKey().After(out[j].sortKey())
	})
	return out, nil
}

func (m *MockClient) DiscussionForums(ctx context.Context, orgUnitID int) ([]Forum, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var forums []Forum
	if err := m.readCourseJSON(ctx, orgUnitID, "forums.json", &forums); err != nil {
		return nil, err
	}
	return forums, nil
}

// forum ids, topic ids and post ids live in three separate fixtures because
// D2L returns them from three separate routes. Fixtures that must agree with
// each other eventually will not, so TestDiscussionFixtureConsistency asserts
// the references resolve.
func (m *MockClient) DiscussionTopics(ctx context.Context, orgUnitID, forumID int) ([]DiscussionTopic, error) {
	forums, err := m.DiscussionForums(ctx, orgUnitID)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(forums, func(f Forum) bool { return f.ForumId == forumID }) {
		return nil, fmt.Errorf("mock: forum %d in org unit %d: %w", forumID, orgUnitID, ErrNotFound)
	}

	var all []DiscussionTopic
	if err := m.readCourseJSON(ctx, orgUnitID, "discussion-topics.json", &all); err != nil {
		return nil, err
	}
	out := make([]DiscussionTopic, 0, len(all))
	for _, t := range all {
		if t.ForumId == forumID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *MockClient) DiscussionPosts(ctx context.Context, orgUnitID, forumID, topicID int) ([]Post, error) {
	topics, err := m.DiscussionTopics(ctx, orgUnitID, forumID)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(topics, func(t DiscussionTopic) bool { return t.TopicId == topicID }) {
		return nil, fmt.Errorf("mock: topic %d in forum %d: %w", topicID, forumID, ErrNotFound)
	}

	var all []Post
	if err := m.readCourseJSON(ctx, orgUnitID, "discussion-posts.json", &all); err != nil {
		return nil, err
	}
	out := make([]Post, 0, len(all))
	for _, p := range all {
		if p.ForumId == forumID && p.TopicId == topicID {
			out = append(out, p)
		}
	}
	// Oldest first: a discussion read in reverse is nonsense, and replies must
	// not appear before what they answer.
	sort.Slice(out, func(i, j int) bool {
		return out[i].DatePosted.Before(out[j].DatePosted)
	})
	return out, nil
}

func (m *MockClient) Quizzes(ctx context.Context, orgUnitID int) ([]Quiz, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var page QuizListPage
	if err := m.readCourseJSON(ctx, orgUnitID, "quizzes.json", &page); err != nil {
		return nil, err
	}
	out := page.Objects
	sort.Slice(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
	return out, nil
}

func (m *MockClient) Classlist(ctx context.Context, orgUnitID int) ([]ClasslistUser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var page ClasslistPage
	if err := m.readCourseJSON(ctx, orgUnitID, "classlist.json", &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (m *MockClient) SubmitToDropbox(ctx context.Context, orgUnitID, folderID int, comment string, files []Upload) (*Submission, error) {
	return nil, ErrNotImplemented
}

// findObject walks the content tree depth-first. Modules nest, so a flat scan
// of the root would miss anything filed one level down.
func findObject(objs []ContentObject, id int) (ContentObject, bool) {
	for _, o := range objs {
		if o.Id == id {
			return o, true
		}
		if found, ok := findObject(o.Structure, id); ok {
			return found, true
		}
	}
	return ContentObject{}, false
}
