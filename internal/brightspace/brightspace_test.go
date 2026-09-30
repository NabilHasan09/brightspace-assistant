package brightspace

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// Conformance is a compile-time property, not a runtime one. If a method is
// added to Client and an implementation lags, the build breaks here rather
// than a test failing somewhere downstream.
var (
	_ Client = (*MockClient)(nil)
	_ Client = (*LiveClient)(nil)
)

func newTestClient(t *testing.T) *MockClient {
	t.Helper()
	return NewMockClient(os.DirFS("../../testdata"))
}

func TestMyEnrollments(t *testing.T) {
	got, err := newTestClient(t).MyEnrollments(context.Background())
	if err != nil {
		t.Fatalf("MyEnrollments: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d org units, want 3", len(got))
	}

	// Enrollments include non-course org units. Callers that assume every
	// entry is a course will try to fetch content for a department and get
	// nothing back, so the fixture keeps one around on purpose.
	var courses, other int
	for _, u := range got {
		if u.OrgUnit.Type.Code == "Course Offering" {
			courses++
		} else {
			other++
		}
	}
	if courses != 2 || other != 1 {
		t.Errorf("got %d courses and %d non-courses, want 2 and 1", courses, other)
	}

	first := got[0].OrgUnit
	if first.Id != 6001 || first.Code != "MTH1003" || first.Name != "Calculus I" {
		t.Errorf("first enrollment = %+v", first)
	}

	// The wrapper's JSON key is "OrgUnit". Spelling it "OrgUnitInfo" — as this
	// package did until a live response proved otherwise — is not an unmarshal
	// error, so the failure looks like three enrollments that all came back
	// blank. Assert a populated org unit, not just a count.
	if first == (OrgUnitInfo{}) {
		t.Error("first enrollment decoded to a zero org unit: the Items[].OrgUnit key is misspelled")
	}

	// Access travels with the enrollment because it is the only way to tell a
	// class running now from one that ended two years ago.
	if got[0].Access.StartDate == nil || got[0].Access.EndDate == nil {
		t.Error("access window missing: courses cannot be filtered to the current term without it")
	}
	if got[0].Access.ClasslistRoleName != "Learner" {
		t.Errorf("ClasslistRoleName = %q, want Learner", got[0].Access.ClasslistRoleName)
	}
}

func TestContentRoot(t *testing.T) {
	root, err := newTestClient(t).ContentRoot(context.Background(), 6001)
	if err != nil {
		t.Fatalf("ContentRoot: %v", err)
	}
	if len(root) != 2 {
		t.Fatalf("got %d root modules, want 2", len(root))
	}

	mod := root[0]
	if !mod.IsModule() {
		t.Errorf("root entry Type = %d, want ObjectModule", mod.Type)
	}
	if mod.Id != 771 || mod.Title != "Week 8: Integration by Parts" {
		t.Errorf("module = {Id:%d Title:%q}", mod.Id, mod.Title)
	}
	if len(mod.Structure) != 4 {
		t.Errorf("got %d children, want 4", len(mod.Structure))
	}

	// Withheld objects come back unfiltered, because this package mirrors the
	// API rather than deciding what a student may see. Module 773 is an
	// unreleased week and topic 8845 is a solution set; dropping them is the
	// caller's job, and internal/mcptools is where that happens.
	if !root[1].IsHidden {
		t.Errorf("module 773 IsHidden = false, want true — the hidden-module fixture is what proves callers must filter")
	}
	if hidden, ok := findObject(root, 8845); !ok || !hidden.IsHidden {
		t.Errorf("topic 8845 = {found:%t hidden:%t}, want a hidden topic", ok, hidden.IsHidden)
	}

	// LastModifiedDate gates every re-ingest decision, so a date that fails to
	// parse would silently mean "always stale" or "never stale".
	want := time.Date(2026, 3, 2, 14, 12, 3, 0, time.UTC)
	if !mod.LastModifiedDate.Equal(want) {
		t.Errorf("LastModifiedDate = %v, want %v", mod.LastModifiedDate, want)
	}
}

func TestContentRootEmptyCourse(t *testing.T) {
	root, err := newTestClient(t).ContentRoot(context.Background(), 6002)
	if err != nil {
		t.Fatalf("ContentRoot: %v", err)
	}
	if len(root) != 0 {
		t.Errorf("got %d modules, want 0", len(root))
	}
}

func TestModuleStructureNested(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	top, err := c.ModuleStructure(ctx, 6001, 771)
	if err != nil {
		t.Fatalf("ModuleStructure(771): %v", err)
	}
	if len(top) != 4 {
		t.Fatalf("got %d children of 771, want 4", len(top))
	}

	// Module 772 is nested inside 771. A flat scan of the root would miss it,
	// and real courses nest, so this is the case worth pinning.
	nested, err := c.ModuleStructure(ctx, 6001, 772)
	if err != nil {
		t.Fatalf("ModuleStructure(772): %v", err)
	}
	if len(nested) != 1 || nested[0].Id != 8844 {
		t.Errorf("children of 772 = %+v, want one topic 8844", nested)
	}
}

func TestTopicFile(t *testing.T) {
	rc, mimeType, err := newTestClient(t).TopicFile(context.Background(), 6001, 8842)
	if err != nil {
		t.Fatalf("TopicFile: %v", err)
	}
	defer rc.Close()

	if !strings.HasPrefix(mimeType, "text/plain") {
		t.Errorf("mime = %q, want text/plain", mimeType)
	}
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(b), "Integration by Parts") {
		t.Errorf("content does not look like the fixture: %.60q", b)
	}
}

func TestTopicFileNestedTopic(t *testing.T) {
	rc, _, err := newTestClient(t).TopicFile(context.Background(), 6001, 8844)
	if err != nil {
		t.Fatalf("TopicFile(8844): %v", err)
	}
	rc.Close()
}

func TestTopicFileErrors(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	tests := []struct {
		name      string
		orgUnitID int
		topicID   int
		want      error
	}{
		// Link topics have no bytes behind them. This is documented Valence
		// behavior and a meaningful share of any real course, so ingest has to
		// distinguish it from a genuine failure.
		{"link topic", 6001, 8843, ErrNotFileTopic},
		{"module id, not a topic", 6001, 771, ErrNotFound},
		{"unknown topic", 6001, 99999, ErrNotFound},
		{"unknown org unit", 9999, 8842, ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, _, err := c.TopicFile(ctx, tt.orgUnitID, tt.topicID)
			if rc != nil {
				rc.Close()
				t.Error("got a non-nil reader alongside an error")
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestMyGradeValues(t *testing.T) {
	got, err := newTestClient(t).MyGradeValues(context.Background(), 6001)
	if err != nil {
		t.Fatalf("MyGradeValues: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d grades, want 5", len(got))
	}

	byName := make(map[string]GradeValue, len(got))
	for _, g := range got {
		byName[g.GradeObjectName] = g
	}

	hw1 := byName["Homework 1"]
	if !hw1.Scored() {
		t.Error("Homework 1 should be scored")
	}
	if *hw1.PointsNumerator != 92 || *hw1.PointsDenominator != 100 {
		t.Errorf("Homework 1 points = %v/%v", *hw1.PointsNumerator, *hw1.PointsDenominator)
	}
	if !strings.Contains(hw1.Comments.Text, "substitution") {
		t.Errorf("Homework 1 comments = %q", hw1.Comments.Text)
	}

	// An ungraded numeric item still carries a denominator. Reading points
	// without checking Scored would report this as 0 out of 100 — inventing a
	// failing grade for work that has not been marked yet.
	exam2 := byName["Exam 2"]
	if exam2.Scored() {
		t.Error("Exam 2 has no score recorded and must not report as scored")
	}
	if exam2.PointsNumerator != nil {
		t.Errorf("Exam 2 numerator = %v, want nil", *exam2.PointsNumerator)
	}
	if exam2.PointsDenominator == nil || *exam2.PointsDenominator != 100 {
		t.Error("Exam 2 should still carry a denominator of 100")
	}

	// Pass/fail items have no points at all.
	lab := byName["Lab Participation"]
	if lab.GradeObjectType != GradePassFail {
		t.Errorf("Lab Participation type = %d, want GradePassFail", lab.GradeObjectType)
	}
	if lab.Scored() {
		t.Error("pass/fail item must not report as scored")
	}
	if lab.DisplayedGrade != "Pass" {
		t.Errorf("Lab Participation displayed = %q", lab.DisplayedGrade)
	}

	// PrivateComments are instructor-only. The client surfaces them because it
	// mirrors the API; nothing student-facing may pass them through.
	if byName["Homework 2"].PrivateComments.Text == "" {
		t.Error("fixture should carry a private comment so the distinction stays testable")
	}
}

func TestMyFinalGrade(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	final, err := c.MyFinalGrade(ctx, 6001)
	if err != nil {
		t.Fatalf("MyFinalGrade: %v", err)
	}
	if final.DisplayedGrade != "84 %" || !final.Scored() {
		t.Errorf("final = %+v", final)
	}

	// MTH3005 releases no final grade mid-semester. That is a normal state,
	// not a failure, and must not degrade into a zero.
	got, err := c.MyFinalGrade(ctx, 6002)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("MyFinalGrade(6002) err = %v, want ErrNotFound", err)
	}
	if got != nil {
		t.Errorf("MyFinalGrade(6002) = %+v, want nil", got)
	}
}

func TestDropboxFolders(t *testing.T) {
	got, err := newTestClient(t).DropboxFolders(context.Background(), 6001)
	if err != nil {
		t.Fatalf("DropboxFolders: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d folders, want 3", len(got))
	}

	hw3 := got[1]
	if hw3.Name != "Homework 3" {
		t.Fatalf("second folder = %q", hw3.Name)
	}
	if hw3.DueDate == nil {
		t.Fatal("Homework 3 has no due date")
	}
	want := time.Date(2026, 3, 20, 3, 59, 0, 0, time.UTC)
	if !hw3.DueDate.Equal(want) {
		t.Errorf("due = %v, want %v", hw3.DueDate, want)
	}
	if !strings.Contains(hw3.CustomInstructions.Text, "lastname-hw3.pdf") {
		t.Errorf("instructions = %q", hw3.CustomInstructions.Text)
	}

	// A folder with no due date is normal and must stay distinguishable from
	// one due at the zero time.
	if got[2].DueDate != nil {
		t.Error("draft folder should have a nil due date")
	}
	// Hidden folders come back unfiltered, matching the API. Callers filter.
	if !got[2].IsHidden {
		t.Error("fixture should include a hidden folder")
	}
}

func TestMyEvents(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	april := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// One call spanning several courses is what makes cross-course planning
	// cheap rather than a fan-out across enrollments. The ids are required:
	// a live tenant answers 400 for an unscoped query, so the mock refuses one
	// too rather than being more permissive than the API it stands in for.
	both := []int{6001, 6002}

	if _, err := c.MyEvents(ctx, nil, march, april); err == nil {
		t.Error("MyEvents accepted an empty org unit list; a live tenant answers 400")
	}

	all, err := c.MyEvents(ctx, both, march, april)
	if err != nil {
		t.Fatalf("MyEvents: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d events in March, want 4", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].StartDateTime.Before(all[i-1].StartDateTime) {
			t.Fatalf("events not sorted by start time: %v", all)
		}
	}

	oneCourse, err := c.MyEvents(ctx, []int{6001}, march, april)
	if err != nil {
		t.Fatalf("MyEvents(6001): %v", err)
	}
	if len(oneCourse) != 3 {
		t.Errorf("got %d MTH1003 events, want 3", len(oneCourse))
	}
	for _, e := range oneCourse {
		if e.OrgUnitId != 6001 {
			t.Errorf("event %d belongs to org unit %d", e.CalendarEventId, e.OrgUnitId)
		}
	}

	// Range is [start, end): an event exactly at start is in, one exactly at
	// end is out. Pinned so the boundary cannot drift silently.
	bounded, err := c.MyEvents(ctx, both,
		time.Date(2026, 3, 6, 4, 59, 0, 0, time.UTC),
		time.Date(2026, 3, 20, 3, 59, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("MyEvents bounded: %v", err)
	}
	if len(bounded) != 3 {
		t.Errorf("got %d events in the half-open range, want 3", len(bounded))
	}
	if len(bounded) > 0 && bounded[0].CalendarEventId != 55001 {
		t.Errorf("first event = %d, want 55001 (start is inclusive)", bounded[0].CalendarEventId)
	}
}

func TestNewsItems(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	all, err := c.NewsItems(ctx, 6001, time.Time{})
	if err != nil {
		t.Fatalf("NewsItems: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d announcements, want 4", len(all))
	}

	// Newest first — "anything I missed this week?" reads top-down.
	if all[0].Id != 3303 || all[1].Id != 3302 {
		t.Errorf("order = %d, %d; want 3303 then 3302", all[0].Id, all[1].Id)
	}
	// The undated item sorts last rather than displacing a real announcement.
	if all[len(all)-1].Id != 3304 {
		t.Errorf("last = %d, want the undated item 3304", all[len(all)-1].Id)
	}

	// Unpublished drafts come back unfiltered, like every other visibility
	// flag here. Nothing student-facing may pass one through.
	var draft bool
	for _, n := range all {
		if n.Id == 3303 && !n.IsPublished {
			draft = true
		}
	}
	if !draft {
		t.Error("fixture should carry an unpublished draft so the distinction stays testable")
	}

	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	recent, err := c.NewsItems(ctx, 6001, since)
	if err != nil {
		t.Fatalf("NewsItems(since): %v", err)
	}
	// 3302 and 3303 are in range; 3301 predates it; 3304 has no date and is
	// kept because it cannot be ruled out.
	if len(recent) != 3 {
		t.Fatalf("got %d items since March, want 3: %+v", len(recent), recent)
	}
	for _, n := range recent {
		if n.Id == 3301 {
			t.Error("February announcement leaked past the since filter")
		}
	}
}

func TestDiscussionForums(t *testing.T) {
	got, err := newTestClient(t).DiscussionForums(context.Background(), 6001)
	if err != nil {
		t.Fatalf("DiscussionForums: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d forums, want 2", len(got))
	}
	if got[0].ForumId != 5501 || got[0].Name != "Week 8 Discussion" {
		t.Errorf("first forum = %+v", got[0])
	}
	if !got[1].IsHidden {
		t.Error("fixture should include a hidden forum")
	}
}

func TestDiscussionTopics(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	got, err := c.DiscussionTopics(ctx, 6001, 5501)
	if err != nil {
		t.Fatalf("DiscussionTopics: %v", err)
	}
	// Forum 5502's topic must not leak into 5501's listing.
	if len(got) != 2 {
		t.Fatalf("got %d topics in forum 5501, want 2", len(got))
	}
	for _, top := range got {
		if top.ForumId != 5501 {
			t.Errorf("topic %d belongs to forum %d", top.TopicId, top.ForumId)
		}
	}

	if _, err := c.DiscussionTopics(ctx, 6001, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown forum err = %v, want ErrNotFound", err)
	}
}

func TestDiscussionPosts(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	got, err := c.DiscussionPosts(ctx, 6001, 5501, 6601)
	if err != nil {
		t.Fatalf("DiscussionPosts: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d posts, want 3", len(got))
	}

	// Oldest first, so a reply never appears before what it answers.
	for i := 1; i < len(got); i++ {
		if got[i].DatePosted.Before(got[i-1].DatePosted) {
			t.Fatalf("posts not in chronological order: %+v", got)
		}
	}

	// The flat array carries the thread shape in ParentPostId alone. Without
	// it, "did anyone reply to my post?" is unanswerable.
	if got[0].ParentPostId != nil {
		t.Error("first post should be a thread starter")
	}
	if got[1].ParentPostId == nil || *got[1].ParentPostId != 7701 {
		t.Errorf("second post should reply to 7701, got %v", got[1].ParentPostId)
	}
	if got[1].IsRead {
		t.Error("fixture should carry an unread reply")
	}
	// Deleted posts come back with their message intact. Callers filter.
	if !got[2].IsDeleted {
		t.Error("fixture should include a deleted post")
	}

	// A topic with no posts is a normal state, not an error.
	empty, err := c.DiscussionPosts(ctx, 6002, 5601, 6701)
	if err != nil {
		t.Fatalf("DiscussionPosts(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("got %d posts in an empty thread", len(empty))
	}

	if _, err := c.DiscussionPosts(ctx, 6001, 5501, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown topic err = %v, want ErrNotFound", err)
	}
	// A topic that exists but in a different forum must not resolve.
	if _, err := c.DiscussionPosts(ctx, 6001, 5501, 6603); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-forum topic err = %v, want ErrNotFound", err)
	}
}

// Forums, topics and posts arrive from three separate routes and so live in
// three separate fixtures. Fixtures that must agree with each other eventually
// will not, so the references are asserted rather than assumed.
func TestDiscussionFixtureConsistency(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	for _, orgUnitID := range []int{6001, 6002} {
		forums, err := c.DiscussionForums(ctx, orgUnitID)
		if err != nil {
			t.Fatalf("DiscussionForums(%d): %v", orgUnitID, err)
		}

		seenTopics := make(map[int]bool)
		for _, f := range forums {
			topics, err := c.DiscussionTopics(ctx, orgUnitID, f.ForumId)
			if err != nil {
				t.Fatalf("DiscussionTopics(%d, %d): %v", orgUnitID, f.ForumId, err)
			}
			for _, top := range topics {
				seenTopics[top.TopicId] = true
				if _, err := c.DiscussionPosts(ctx, orgUnitID, f.ForumId, top.TopicId); err != nil {
					t.Errorf("posts for topic %d: %v", top.TopicId, err)
				}
			}
		}

		var posts []Post
		if err := c.readCourseJSON(ctx, orgUnitID, "discussion-posts.json", &posts); err != nil {
			t.Fatalf("read posts fixture: %v", err)
		}
		for _, p := range posts {
			if !seenTopics[p.TopicId] {
				t.Errorf("org unit %d: post %d references topic %d, which no forum lists",
					orgUnitID, p.PostId, p.TopicId)
			}
		}
	}
}

func TestQuizzes(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	got, err := c.Quizzes(ctx, 6001)
	if err != nil {
		t.Fatalf("Quizzes: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d quizzes, want 3", len(got))
	}
	// The fixture is filed out of order, so this asserts the sort rather than
	// the file's layout.
	for i := 1; i < len(got); i++ {
		if got[i].SortOrder < got[i-1].SortOrder {
			t.Fatalf("quizzes not sorted by SortOrder: %+v", got)
		}
	}

	byName := make(map[string]Quiz, len(got))
	for _, q := range got {
		byName[q.Name] = q
	}
	// A practice quiz with no deadline must not read as due at the zero time.
	if practice := byName["Practice Quiz 8"]; practice.DueDate != nil {
		t.Errorf("Practice Quiz 8 DueDate = %v, want nil", practice.DueDate)
	}
	if exam := byName["Exam 2"]; exam.DueDate == nil {
		t.Error("Exam 2 should carry a due date")
	}
	if byName["Exam 1"].IsActive {
		t.Error("fixture should include an inactive quiz")
	}

	empty, err := c.Quizzes(ctx, 6002)
	if err != nil {
		t.Fatalf("Quizzes(6002): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("got %d quizzes for a course with none", len(empty))
	}
}

func TestClasslist(t *testing.T) {
	got, err := newTestClient(t).Classlist(context.Background(), 6001)
	if err != nil {
		t.Fatalf("Classlist: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d people, want 3", len(got))
	}

	if got[0].DisplayName != "Prof. Amara Okonkwo" || got[0].Email == "" {
		t.Errorf("instructor = %+v", got[0])
	}
	if got[0].RoleId == nil {
		t.Error("instructor should carry a RoleId")
	}

	// Contact visibility is an org setting, so JSON null is the normal case,
	// not a broken record. logistics must answer with a name and no email
	// rather than failing.
	withheld := got[2]
	if withheld.DisplayName == "" {
		t.Error("a person with withheld contact details must still have a name")
	}
	if withheld.Email != "" || withheld.UserName != "" || withheld.OrgDefinedId != "" {
		t.Errorf("null contact fields should decode empty, got %+v", withheld)
	}
	if withheld.LastAccessed != nil {
		t.Error("a null LastAccessed must stay nil, not become the zero time")
	}
}

func TestSubmitToDropboxNotImplemented(t *testing.T) {
	ctx := context.Background()
	clients := map[string]Client{
		"mock": newTestClient(t),
		"live": NewLiveClient("https://example.invalid", nil),
	}
	for name, c := range clients {
		if _, err := c.SubmitToDropbox(ctx, 6001, 4401, "", nil); !errors.Is(err, ErrNotImplemented) {
			t.Errorf("%s SubmitToDropbox err = %v, want ErrNotImplemented", name, err)
		}
	}
}

func TestMimeForExt(t *testing.T) {
	tests := []struct{ ext, want string }{
		// Not in Go's builtin table and not reliably in the system files.
		// Without an override this resolves to octet-stream, and the
		// extractor would route a text format down the binary branch.
		{".md", "text/markdown"},
		{".MD", "text/markdown"},
		{".pdf", "application/pdf"},
		{".docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{".pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{".txt", "text/plain"},
		{".sadhjk", "application/octet-stream"},
	}
	for _, tt := range tests {
		if got := mimeForExt(tt.ext); !strings.HasPrefix(got, tt.want) {
			t.Errorf("mimeForExt(%q) = %q, want prefix %q", tt.ext, got, tt.want)
		}
	}
}

// The mock reads local files, so honoring cancellation is not needed for its
// own sake. It matters because everything above this package is tested against
// the mock: if the mock ignores ctx, a caller that leaks a cancelled context
// passes here and fails against the live client.
func TestContextCancellation(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.MyEnrollments(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("MyEnrollments err = %v, want context.Canceled", err)
	}
	if _, err := c.ContentRoot(ctx, 6001); !errors.Is(err, context.Canceled) {
		t.Errorf("ContentRoot err = %v, want context.Canceled", err)
	}
	if _, err := c.ModuleStructure(ctx, 6001, 771); !errors.Is(err, context.Canceled) {
		t.Errorf("ModuleStructure err = %v, want context.Canceled", err)
	}
	if _, _, err := c.TopicFile(ctx, 6001, 8842); !errors.Is(err, context.Canceled) {
		t.Errorf("TopicFile err = %v, want context.Canceled", err)
	}
	if _, err := c.MyGradeValues(ctx, 6001); !errors.Is(err, context.Canceled) {
		t.Errorf("MyGradeValues err = %v, want context.Canceled", err)
	}
	if _, err := c.MyFinalGrade(ctx, 6001); !errors.Is(err, context.Canceled) {
		t.Errorf("MyFinalGrade err = %v, want context.Canceled", err)
	}
	if _, err := c.DropboxFolders(ctx, 6001); !errors.Is(err, context.Canceled) {
		t.Errorf("DropboxFolders err = %v, want context.Canceled", err)
	}
	if _, err := c.MyEvents(ctx, nil, time.Time{}, time.Now()); !errors.Is(err, context.Canceled) {
		t.Errorf("MyEvents err = %v, want context.Canceled", err)
	}
	if _, err := c.NewsItems(ctx, 6001, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Errorf("NewsItems err = %v, want context.Canceled", err)
	}
	if _, err := c.DiscussionForums(ctx, 6001); !errors.Is(err, context.Canceled) {
		t.Errorf("DiscussionForums err = %v, want context.Canceled", err)
	}
	if _, err := c.DiscussionTopics(ctx, 6001, 5501); !errors.Is(err, context.Canceled) {
		t.Errorf("DiscussionTopics err = %v, want context.Canceled", err)
	}
	if _, err := c.DiscussionPosts(ctx, 6001, 5501, 6601); !errors.Is(err, context.Canceled) {
		t.Errorf("DiscussionPosts err = %v, want context.Canceled", err)
	}
	if _, err := c.Quizzes(ctx, 6001); !errors.Is(err, context.Canceled) {
		t.Errorf("Quizzes err = %v, want context.Canceled", err)
	}
	if _, err := c.Classlist(ctx, 6001); !errors.Is(err, context.Canceled) {
		t.Errorf("Classlist err = %v, want context.Canceled", err)
	}
}

// LiveClient's behavior is covered in live_test.go, against an httptest server
// serving these same fixtures.
