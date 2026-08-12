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
}
