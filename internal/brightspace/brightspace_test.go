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
		if u.Type.Code == "Course Offering" {
			courses++
		} else {
			other++
		}
	}
	if courses != 2 || other != 1 {
		t.Errorf("got %d courses and %d non-courses, want 2 and 1", courses, other)
	}

	if got[0].Id != 6001 || got[0].Code != "MTH1003" || got[0].Name != "Calculus I" {
		t.Errorf("first enrollment = %+v", got[0])
	}
}

func TestContentRoot(t *testing.T) {
	root, err := newTestClient(t).ContentRoot(context.Background(), 6001)
	if err != nil {
		t.Fatalf("ContentRoot: %v", err)
	}
	if len(root) != 1 {
		t.Fatalf("got %d root modules, want 1", len(root))
	}

	mod := root[0]
	if !mod.IsModule() {
		t.Errorf("root entry Type = %d, want ObjectModule", mod.Type)
	}
	if mod.Id != 771 || mod.Title != "Week 8: Integration by Parts" {
		t.Errorf("module = {Id:%d Title:%q}", mod.Id, mod.Title)
	}
	if len(mod.Structure) != 3 {
		t.Errorf("got %d children, want 3", len(mod.Structure))
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
	if len(top) != 3 {
		t.Fatalf("got %d children of 771, want 3", len(top))
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

	// Empty org unit list means every enrolled course — one call covers the
	// cross-course planning case rather than fanning out per enrollment.
	all, err := c.MyEvents(ctx, nil, march, april)
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
	bounded, err := c.MyEvents(ctx, nil,
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
}

func TestLiveClientNotImplemented(t *testing.T) {
	c := &LiveClient{Host: "brightspace.cuny.edu"}
	ctx := context.Background()

	if _, err := c.MyEnrollments(ctx); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("MyEnrollments err = %v", err)
	}
	if _, err := c.ContentRoot(ctx, 6001); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("ContentRoot err = %v", err)
	}
	if _, err := c.ModuleStructure(ctx, 6001, 771); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("ModuleStructure err = %v", err)
	}
	if _, _, err := c.TopicFile(ctx, 6001, 8842); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("TopicFile err = %v", err)
	}
	if _, err := c.MyGradeValues(ctx, 6001); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("MyGradeValues err = %v", err)
	}
	if _, err := c.MyFinalGrade(ctx, 6001); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("MyFinalGrade err = %v", err)
	}
	if _, err := c.DropboxFolders(ctx, 6001); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("DropboxFolders err = %v", err)
	}
	if _, err := c.MyEvents(ctx, nil, time.Time{}, time.Now()); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("MyEvents err = %v", err)
	}
}
