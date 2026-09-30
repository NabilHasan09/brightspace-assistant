package brightspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Deliberately different so a route that reaches for the wrong product's
// version fails loudly instead of coincidentally working.
const (
	testLPVersion = "1.9"
	testLEVersion = "1.67"
)

const fixtureDir = "../../testdata"

// tenant is a fake Brightspace that answers documented D2L routes out of the
// same testdata/ fixtures MockClient reads.
//
// What this proves: URL construction, status-to-error mapping, the paging
// loop, streaming downloads, JSON decoding of real fixture bytes, and that
// LiveClient and MockClient give the same answers.
//
// What it cannot prove: that these are the routes and schemas CUNY actually
// serves. Both sides of the contract are written here, so a wrong path or a
// misread schema passes green. Only a real tenant closes that gap — which is
// why LiveClient's doc comment calls first connection a verification pass.
type tenant struct {
	*httptest.Server
	mock *MockClient

	mu   sync.Mutex
	reqs []string
}

func newTenant(t *testing.T) *tenant {
	t.Helper()
	tn := &tenant{mock: NewMockClient(os.DirFS(fixtureDir))}

	lp := "/d2l/api/lp/" + testLPVersion
	le := "/d2l/api/le/" + testLEVersion

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+lp+"/enrollments/myenrollments/", tn.serveFile("enrollments.json"))
	mux.HandleFunc("GET "+le+"/calendar/events/myEvents/", tn.serveFile("calendar-events.json"))
	mux.HandleFunc("GET "+le+"/{ou}/content/root/", tn.serveCourseFile("content-root.json"))
	mux.HandleFunc("GET "+le+"/{ou}/grades/values/myGradeValues/", tn.serveCourseFile("grades.json"))
	mux.HandleFunc("GET "+le+"/{ou}/grades/final/values/myGradeValue", tn.serveCourseFile("final-grade.json"))
	mux.HandleFunc("GET "+le+"/{ou}/dropbox/folders/", tn.serveCourseFile("dropbox.json"))
	mux.HandleFunc("GET "+le+"/{ou}/content/modules/{mid}/structure/", tn.serveModuleStructure)
	mux.HandleFunc("GET "+le+"/{ou}/content/topics/{tid}/file", tn.serveTopicFile)
	mux.HandleFunc("GET "+le+"/{ou}/news/", tn.serveCourseFile("news.json"))
	mux.HandleFunc("GET "+le+"/{ou}/discussions/forums/", tn.serveCourseFile("forums.json"))
	mux.HandleFunc("GET "+le+"/{ou}/quizzes/", tn.serveCourseFile("quizzes.json"))
	mux.HandleFunc("GET "+le+"/{ou}/classlist/paged/", tn.serveCourseFile("classlist.json"))

	// Topics and posts are filtered server-side by the real API, so the fake
	// has to filter too — serving the whole fixture would let LiveClient pass
	// while returning another forum's threads.
	mux.HandleFunc("GET "+le+"/{ou}/discussions/forums/{fid}/topics/", tn.serveDiscussionTopics)
	mux.HandleFunc("GET "+le+"/{ou}/discussions/forums/{fid}/topics/{tid}/posts/", tn.serveDiscussionPosts)

	tn.Server = httptest.NewServer(tn.record(mux))
	t.Cleanup(tn.Close)
	return tn
}

func (tn *tenant) client() *LiveClient {
	c := NewLiveClient(tn.URL, tn.Server.Client())
	c.LPVersion, c.LEVersion = testLPVersion, testLEVersion
	return c
}

func (tn *tenant) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tn.mu.Lock()
		tn.reqs = append(tn.reqs, r.URL.RequestURI())
		tn.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (tn *tenant) requests() []string {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	return append([]string(nil), tn.reqs...)
}

func (tn *tenant) lastRequest(t *testing.T) string {
	t.Helper()
	reqs := tn.requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	return reqs[len(reqs)-1]
}

const notFoundBody = `{"Errors":[{"Message":"not found"}]}`

// serveFile writes fixture bytes verbatim rather than re-encoding through the
// Go structs, so the decode path sees real JSON — including the fields these
// structs deliberately ignore.
func (tn *tenant) serveFile(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { tn.writeFixture(w, name) }
}

func (tn *tenant) writeFixture(w http.ResponseWriter, name string) {
	b, err := os.ReadFile(filepath.Join(fixtureDir, filepath.FromSlash(name)))
	if err != nil {
		http.Error(w, notFoundBody, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// serveCourseFile resolves the org unit id to a fixture directory the way the
// mock does. The real API needs no such step — fixtures are filed under
// "MTH1003" rather than "6001" because a human has to read them.
func (tn *tenant) serveCourseFile(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("ou"))
		if err != nil {
			http.Error(w, notFoundBody, http.StatusNotFound)
			return
		}
		code, err := tn.mock.courseCode(r.Context(), id)
		if err != nil {
			http.Error(w, notFoundBody, http.StatusNotFound)
			return
		}
		tn.writeFixture(w, path.Join("courses", code, name))
	}
}

func (tn *tenant) serveModuleStructure(w http.ResponseWriter, r *http.Request) {
	ou, _ := strconv.Atoi(r.PathValue("ou"))
	mid, _ := strconv.Atoi(r.PathValue("mid"))

	kids, err := tn.mock.ModuleStructure(r.Context(), ou, mid)
	if err != nil {
		http.Error(w, notFoundBody, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(kids)
}

func (tn *tenant) serveDiscussionTopics(w http.ResponseWriter, r *http.Request) {
	ou, _ := strconv.Atoi(r.PathValue("ou"))
	fid, _ := strconv.Atoi(r.PathValue("fid"))

	topics, err := tn.mock.DiscussionTopics(r.Context(), ou, fid)
	if err != nil {
		http.Error(w, notFoundBody, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(topics)
}

func (tn *tenant) serveDiscussionPosts(w http.ResponseWriter, r *http.Request) {
	ou, _ := strconv.Atoi(r.PathValue("ou"))
	fid, _ := strconv.Atoi(r.PathValue("fid"))
	tid, _ := strconv.Atoi(r.PathValue("tid"))

	posts, err := tn.mock.DiscussionPosts(r.Context(), ou, fid, tid)
	if err != nil {
		http.Error(w, notFoundBody, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(posts)
}

func (tn *tenant) serveTopicFile(w http.ResponseWriter, r *http.Request) {
	ou, _ := strconv.Atoi(r.PathValue("ou"))
	tid, _ := strconv.Atoi(r.PathValue("tid"))

	rc, mimeType, err := tn.mock.TopicFile(r.Context(), ou, tid)
	if err != nil {
		// Covers link topics too. Which status a real tenant returns for one is
		// unverified; 404 is the assumption, and it is precisely why LiveClient
		// cannot reconstruct ErrNotFileTopic.
		http.Error(w, notFoundBody, http.StatusNotFound)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", mimeType)
	io.Copy(w, rc)
}

// sameJSON compares through the wire format instead of reflect.DeepEqual,
// which is unreliable on time.Time: two instants that are equal can differ in
// their internal location pointer and monotonic reading.
func sameJSON(t *testing.T, label string, live, mock any) {
	t.Helper()
	a, err := json.Marshal(live)
	if err != nil {
		t.Fatalf("%s: marshal live: %v", label, err)
	}
	b, err := json.Marshal(mock)
	if err != nil {
		t.Fatalf("%s: marshal mock: %v", label, err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("%s: live and mock disagree\nlive: %s\nmock: %s", label, a, b)
	}
}

// TestLiveMatchesMock is the point of the whole harness. Everything above this
// package is built against MockClient, so the only thing that makes that safe
// is the two implementations answering identically.
func TestLiveMatchesMock(t *testing.T) {
	tn := newTenant(t)
	live, mock := tn.client(), tn.mock
	ctx := context.Background()

	liveEnroll, err := live.MyEnrollments(ctx)
	if err != nil {
		t.Fatalf("live MyEnrollments: %v", err)
	}
	mockEnroll, err := mock.MyEnrollments(ctx)
	if err != nil {
		t.Fatalf("mock MyEnrollments: %v", err)
	}
	sameJSON(t, "MyEnrollments", liveEnroll, mockEnroll)

	if len(liveEnroll) != 3 {
		t.Fatalf("got %d enrollments over HTTP, want 3", len(liveEnroll))
	}

	liveRoot, err := live.ContentRoot(ctx, 6001)
	if err != nil {
		t.Fatalf("live ContentRoot: %v", err)
	}
	mockRoot, _ := mock.ContentRoot(ctx, 6001)
	sameJSON(t, "ContentRoot", liveRoot, mockRoot)

	// Nesting survives the round trip: module 772 sits inside 771.
	if len(liveRoot) != 2 || len(liveRoot[0].Structure) != 4 {
		t.Fatalf("content tree did not survive HTTP: %+v", liveRoot)
	}

	liveKids, err := live.ModuleStructure(ctx, 6001, 772)
	if err != nil {
		t.Fatalf("live ModuleStructure: %v", err)
	}
	mockKids, _ := mock.ModuleStructure(ctx, 6001, 772)
	sameJSON(t, "ModuleStructure", liveKids, mockKids)

	liveGrades, err := live.MyGradeValues(ctx, 6001)
	if err != nil {
		t.Fatalf("live MyGradeValues: %v", err)
	}
	mockGrades, _ := mock.MyGradeValues(ctx, 6001)
	sameJSON(t, "MyGradeValues", liveGrades, mockGrades)

	// The null-numerator row is the one that matters most over the wire: if
	// JSON null decoded to 0 instead of nil, an unmarked exam would read as a
	// zero and the grades specialist would report a failure that never
	// happened.
	for _, g := range liveGrades {
		if g.GradeObjectName == "Exam 2" && g.Scored() {
			t.Error("Exam 2 decoded as scored over HTTP")
		}
	}

	liveFinal, err := live.MyFinalGrade(ctx, 6001)
	if err != nil {
		t.Fatalf("live MyFinalGrade: %v", err)
	}
	mockFinal, _ := mock.MyFinalGrade(ctx, 6001)
	sameJSON(t, "MyFinalGrade", liveFinal, mockFinal)

	liveDropbox, err := live.DropboxFolders(ctx, 6001)
	if err != nil {
		t.Fatalf("live DropboxFolders: %v", err)
	}
	mockDropbox, _ := mock.DropboxFolders(ctx, 6001)
	sameJSON(t, "DropboxFolders", liveDropbox, mockDropbox)

	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	april := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Both courses explicitly: the calendar route rejects an unscoped query, so
	// neither implementation accepts a nil id list.
	courses := []int{6001, 6002}
	liveEvents, err := live.MyEvents(ctx, courses, march, april)
	if err != nil {
		t.Fatalf("live MyEvents: %v", err)
	}
	mockEvents, _ := mock.MyEvents(ctx, courses, march, april)
	sameJSON(t, "MyEvents", liveEvents, mockEvents)

	liveNews, err := live.NewsItems(ctx, 6001, time.Time{})
	if err != nil {
		t.Fatalf("live NewsItems: %v", err)
	}
	mockNews, _ := mock.NewsItems(ctx, 6001, time.Time{})
	sameJSON(t, "NewsItems", liveNews, mockNews)
	if len(liveNews) != 4 {
		t.Fatalf("got %d announcements over HTTP, want 4", len(liveNews))
	}

	liveForums, err := live.DiscussionForums(ctx, 6001)
	if err != nil {
		t.Fatalf("live DiscussionForums: %v", err)
	}
	mockForums, _ := mock.DiscussionForums(ctx, 6001)
	sameJSON(t, "DiscussionForums", liveForums, mockForums)

	liveTopics, err := live.DiscussionTopics(ctx, 6001, 5501)
	if err != nil {
		t.Fatalf("live DiscussionTopics: %v", err)
	}
	mockTopics, _ := mock.DiscussionTopics(ctx, 6001, 5501)
	sameJSON(t, "DiscussionTopics", liveTopics, mockTopics)

	livePosts, err := live.DiscussionPosts(ctx, 6001, 5501, 6601)
	if err != nil {
		t.Fatalf("live DiscussionPosts: %v", err)
	}
	mockPosts, _ := mock.DiscussionPosts(ctx, 6001, 5501, 6601)
	sameJSON(t, "DiscussionPosts", livePosts, mockPosts)

	// ParentPostId is what carries the thread shape, and it is the field most
	// likely to be quietly lost: a JSON null decoding to 0 would turn a thread
	// starter into a reply to post 0.
	if len(livePosts) != 3 || livePosts[0].ParentPostId != nil {
		t.Fatalf("thread structure did not survive HTTP: %+v", livePosts)
	}
	if livePosts[1].ParentPostId == nil || *livePosts[1].ParentPostId != 7701 {
		t.Errorf("reply lost its parent over HTTP: %+v", livePosts[1])
	}

	liveQuizzes, err := live.Quizzes(ctx, 6001)
	if err != nil {
		t.Fatalf("live Quizzes: %v", err)
	}
	mockQuizzes, _ := mock.Quizzes(ctx, 6001)
	sameJSON(t, "Quizzes", liveQuizzes, mockQuizzes)
	if len(liveQuizzes) != 3 {
		t.Fatalf("got %d quizzes over HTTP, want 3", len(liveQuizzes))
	}

	liveRoster, err := live.Classlist(ctx, 6001)
	if err != nil {
		t.Fatalf("live Classlist: %v", err)
	}
	mockRoster, _ := mock.Classlist(ctx, 6001)
	sameJSON(t, "Classlist", liveRoster, mockRoster)
	if len(liveRoster) != 3 {
		t.Fatalf("got %d people over HTTP, want 3", len(liveRoster))
	}
}

// The routes are the part most likely to be wrong, since nothing has confirmed
// them against a tenant. Pinning them exactly means a refactor cannot quietly
// change a URL, and makes the diff to check on first live connection obvious.
func TestLiveRequestPaths(t *testing.T) {
	tn := newTenant(t)
	c := tn.client()
	ctx := context.Background()

	tests := []struct {
		name string
		call func() error
		want string
	}{
		// The filter is part of the contract, not incidental: unfiltered, this
		// route returns departments, semesters, and the org root alongside
		// actual classes. Query keys sort alphabetically in the encoded URL.
		{"enrollments", func() error { _, err := c.MyEnrollments(ctx); return err },
			"/d2l/api/lp/1.9/enrollments/myenrollments/?canAccess=true&isActive=true&orgUnitTypeId=3"},
		{"content root", func() error { _, err := c.ContentRoot(ctx, 6001); return err },
			"/d2l/api/le/1.67/6001/content/root/"},
		{"module structure", func() error { _, err := c.ModuleStructure(ctx, 6001, 771); return err },
			"/d2l/api/le/1.67/6001/content/modules/771/structure/"},
		{"grades", func() error { _, err := c.MyGradeValues(ctx, 6001); return err },
			"/d2l/api/le/1.67/6001/grades/values/myGradeValues/"},
		{"final grade", func() error { _, err := c.MyFinalGrade(ctx, 6001); return err },
			"/d2l/api/le/1.67/6001/grades/final/values/myGradeValue"},
		{"dropbox", func() error { _, err := c.DropboxFolders(ctx, 6001); return err },
			"/d2l/api/le/1.67/6001/dropbox/folders/"},
		{"topic file", func() error {
			rc, _, err := c.TopicFile(ctx, 6001, 8842)
			if rc != nil {
				rc.Close()
			}
			return err
		}, "/d2l/api/le/1.67/6001/content/topics/8842/file"},
		{"news", func() error { _, err := c.NewsItems(ctx, 6001, time.Time{}); return err },
			"/d2l/api/le/1.67/6001/news/"},
		{"forums", func() error { _, err := c.DiscussionForums(ctx, 6001); return err },
			"/d2l/api/le/1.67/6001/discussions/forums/"},
		{"discussion topics", func() error { _, err := c.DiscussionTopics(ctx, 6001, 5501); return err },
			"/d2l/api/le/1.67/6001/discussions/forums/5501/topics/"},
		{"discussion posts", func() error { _, err := c.DiscussionPosts(ctx, 6001, 5501, 6601); return err },
			"/d2l/api/le/1.67/6001/discussions/forums/5501/topics/6601/posts/"},
		{"quizzes", func() error { _, err := c.Quizzes(ctx, 6001); return err },
			"/d2l/api/le/1.67/6001/quizzes/"},
		{"classlist", func() error { _, err := c.Classlist(ctx, 6001); return err },
			"/d2l/api/le/1.67/6001/classlist/paged/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got := tn.lastRequest(t); got != tt.want {
				t.Errorf("requested %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLiveEventsQuery(t *testing.T) {
	tn := newTenant(t)
	c := tn.client()

	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	got, err := c.MyEvents(context.Background(), []int{6001, 6002}, start, end)
	if err != nil {
		t.Fatalf("MyEvents: %v", err)
	}

	req := tn.lastRequest(t)
	for _, want := range []string{
		// Milliseconds are mandatory even when zero. A live tenant answers 400
		// for "...T00:00:00Z", which is what time.RFC3339 produces, so this
		// assertion is the contract rather than an incidental formatting choice.
		"startDateTime=2026-03-01T00%3A00%3A00.000Z",
		"endDateTime=2026-04-01T00%3A00%3A00.000Z",
		"orgUnitIdsCSV=6001%2C6002",
	} {
		if !strings.Contains(req, want) {
			t.Errorf("query %q missing %q", req, want)
		}
	}

	// This tenant returns every event regardless of the range it was sent, so
	// what passes here is LiveClient's own [start, end) filter. That is
	// intentional: the server's boundary behavior is unverified, and the
	// interface promises the half-open range either way.
	if len(got) != 4 {
		t.Fatalf("got %d events, want 4 — the April event must be excluded", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].StartDateTime.Before(got[i-1].StartDateTime) {
			t.Fatal("events not sorted by start time")
		}
	}
}

func TestLiveNotFound(t *testing.T) {
	tn := newTenant(t)
	c := tn.client()
	ctx := context.Background()

	tests := []struct {
		name string
		err  error
	}{
		{"unknown org unit", func() error { _, err := c.ContentRoot(ctx, 9999); return err }()},
		{"unknown module", func() error { _, err := c.ModuleStructure(ctx, 6001, 99999); return err }()},
		{"course with no final grade", func() error { _, err := c.MyFinalGrade(ctx, 6002); return err }()},
		{"link topic has no bytes", func() error {
			rc, _, err := c.TopicFile(ctx, 6001, 8843)
			if rc != nil {
				rc.Close()
			}
			return err
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", tt.err)
			}
			// The sentinel is for control flow; the detail is for whoever has
			// to debug this against a tenant they cannot see.
			var apiErr *APIError
			if !errors.As(tt.err, &apiErr) {
				t.Fatalf("err = %v, want an *APIError", tt.err)
			}
			if apiErr.StatusCode != http.StatusNotFound {
				t.Errorf("StatusCode = %d, want 404", apiErr.StatusCode)
			}
			if !strings.Contains(apiErr.Body, "not found") {
				t.Errorf("Body = %q, want the tenant's message preserved", apiErr.Body)
			}
			if !strings.Contains(apiErr.URL, "/d2l/api/") {
				t.Errorf("URL = %q, want the failing route", apiErr.URL)
			}
		})
	}
}

// A 403 from a scope that was never granted at registration looks nothing like
// a missing course, and cannot be fixed without going back to an admin.
func TestLiveUnauthorized(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid token", status)
		}))
		t.Cleanup(srv.Close)

		_, err := NewLiveClient(srv.URL, srv.Client()).MyGradeValues(context.Background(), 6001)
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("status %d: err = %v, want ErrUnauthorized", status, err)
		}
		if errors.Is(err, ErrNotFound) {
			t.Errorf("status %d must not read as ErrNotFound", status)
		}
	}
}

// An unmapped status must still be a usable error rather than an empty one.
func TestLiveServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "backend unavailable", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	_, err := NewLiveClient(srv.URL, srv.Client()).ContentRoot(context.Background(), 6001)
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized) {
		t.Errorf("502 must not map to a sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "backend unavailable") {
		t.Errorf("err = %v, want the status and body in the message", err)
	}
}

func TestLiveTopicFileStreams(t *testing.T) {
	tn := newTenant(t)

	rc, mimeType, err := tn.client().TopicFile(context.Background(), 6001, 8842)
	if err != nil {
		t.Fatalf("TopicFile: %v", err)
	}
	defer rc.Close()

	// The MIME type comes off the response header here, not from guessing at a
	// file extension the way the mock has to.
	if !strings.HasPrefix(mimeType, "text/plain") {
		t.Errorf("mime = %q, want text/plain", mimeType)
	}
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(b), "Integration by Parts") {
		t.Errorf("body does not look like the fixture: %.60q", b)
	}
}

// enrollmentPager serves n single-item pages, so the bookmark loop is exercised
// by something other than a one-page fixture.
func enrollmentPager(t *testing.T, pages int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := 0
		if b := r.URL.Query().Get("bookmark"); b != "" {
			page, _ = strconv.Atoi(b)
		}
		resp := MyEnrollmentsResponse{
			Items: []MyOrgUnitInfo{{OrgUnit: OrgUnitInfo{Id: 7000 + page, Code: "PAGE" + strconv.Itoa(page)}}},
		}
		resp.PagingInfo.HasMoreItems = page+1 < pages
		if resp.PagingInfo.HasMoreItems {
			resp.PagingInfo.Bookmark = strconv.Itoa(page + 1)
		}
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLiveEnrollmentsPaging(t *testing.T) {
	srv := enrollmentPager(t, 3)

	got, err := NewLiveClient(srv.URL, srv.Client()).MyEnrollments(context.Background())
	if err != nil {
		t.Fatalf("MyEnrollments: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d org units across 3 pages, want 3", len(got))
	}
	for i, u := range got {
		if u.OrgUnit.Id != 7000+i {
			t.Errorf("page %d returned %+v — pages arrived out of order or were dropped", i, u)
		}
	}
}

// A tenant that claims more items while handing back the same bookmark would
// otherwise spin forever inside an unattended ingest run.
func TestLiveEnrollmentsPagingTerminates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(MyEnrollmentsResponse{
			PagingInfo: PagingInfo{Bookmark: "stuck", HasMoreItems: true},
			Items:      []MyOrgUnitInfo{{OrgUnit: OrgUnitInfo{Id: 1}}},
		})
	}))
	t.Cleanup(srv.Close)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := NewLiveClient(srv.URL, srv.Client()).MyEnrollments(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("MyEnrollments did not terminate on a repeated bookmark")
	}
}

func TestLiveContextCancellation(t *testing.T) {
	tn := newTenant(t)
	c := tn.client()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.MyEnrollments(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("MyEnrollments err = %v, want context.Canceled", err)
	}
	if _, err := c.ContentRoot(ctx, 6001); !errors.Is(err, context.Canceled) {
		t.Errorf("ContentRoot err = %v, want context.Canceled", err)
	}
	if rc, _, err := c.TopicFile(ctx, 6001, 8842); !errors.Is(err, context.Canceled) {
		if rc != nil {
			rc.Close()
		}
		t.Errorf("TopicFile err = %v, want context.Canceled", err)
	}
	if _, err := c.MyGradeValues(ctx, 6001); !errors.Is(err, context.Canceled) {
		t.Errorf("MyGradeValues err = %v, want context.Canceled", err)
	}
}

// LiveClient sends no Authorization header of its own — auth rides on the
// http.Client's Transport so that token refresh happens beneath this type.
func TestStaticTokenClient(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	hc := StaticTokenClient("test-token")
	// httptest servers are plain HTTP, so the default transport reaches them.
	if _, err := NewLiveClient(srv.URL, hc).DropboxFolders(context.Background(), 6001); err != nil {
		t.Fatalf("DropboxFolders: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-token")
	}
}

func TestCookieClient(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	const session = "d2lSecureSessionVal=abc; d2lSessionVal=def"
	if _, err := NewLiveClient(srv.URL, CookieClient(session)).DropboxFolders(context.Background(), 6001); err != nil {
		t.Fatalf("DropboxFolders: %v", err)
	}
	if gotCookie != session {
		t.Errorf("Cookie = %q, want %q", gotCookie, session)
	}
}

// The point of reading the file per request: a session that expires mid-run is
// replaced by overwriting one file, and the next call succeeds. Capturing the
// string at construction would mean restarting the process, which over MCP
// stdio takes the client's session down with it.
func TestCookieFileClientPicksUpARefreshedSession(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "cookie")
	write := func(cookie string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(cookie), 0o600); err != nil {
			t.Fatalf("writing cookie: %v", err)
		}
	}

	// A trailing newline, because pbpaste and every editor leave one.
	const stale = "d2lSecureSessionVal=stale; d2lSessionVal=one\n"
	const fresh = "d2lSecureSessionVal=fresh; d2lSessionVal=two"
	write(stale)

	c := NewLiveClient(srv.URL, CookieFileClient(path))
	if _, err := c.DropboxFolders(context.Background(), 6001); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if want := strings.TrimSpace(stale); gotCookie != want {
		t.Errorf("first call sent %q, want %q with the newline stripped", gotCookie, want)
	}

	// Same client, no restart.
	write(fresh)
	if _, err := c.DropboxFolders(context.Background(), 6001); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if gotCookie != fresh {
		t.Errorf("second call sent %q, want the refreshed %q", gotCookie, fresh)
	}
}

// An empty or wrong-host cookie must fail before the request goes out. Left to
// the tenant it comes back 403, and a 403 reads as "this route is restricted"
// rather than "you sent no credentials" — which is the wrong thing to debug.
func TestCookieClientRejectsUnusableCookies(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Analytics cookies only, as if copied from a request to some other host.
	wrongHost := filepath.Join(dir, "wrong-host")
	if err := os.WriteFile(wrongHost, []byte("_ga=GA1.2.123; _fbp=fb.1.456"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		client *http.Client
		want   string
	}{
		{"empty string", CookieClient(""), "is empty"},
		{"whitespace-only file", CookieFileClient(empty), "is empty"},
		{"no session cookie", CookieFileClient(wrongHost), "carries no " + sessionCookieName},
		{"missing file", CookieFileClient(filepath.Join(dir, "absent")), "reading session cookie"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached = false
			_, err := NewLiveClient(srv.URL, tt.client).DropboxFolders(context.Background(), 6001)
			if err == nil {
				t.Fatal("request succeeded with an unusable cookie")
			}
			if reached {
				t.Error("an unusable cookie still reached the tenant")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// An expired session is the ordinary failure of cookie auth, and it does not
// arrive as a 401: the tenant answers 200 with a single sign-on page. Reported
// as a decode error it reads as a schema bug, which is the wrong thing to go
// looking at when the fix is to log in again.
func TestLiveHTMLLoginPageIsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!doctype html><html><body>Sign in</body></html>`))
	}))
	t.Cleanup(srv.Close)

	_, err := NewLiveClient(srv.URL, srv.Client()).MyEnrollments(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

// A server that sets no content type at all is sloppy, not signed out. Its
// body still decodes, so the HTML guard must not reject it.
func TestLiveMissingContentTypeStillDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(MyEnrollmentsResponse{
			Items: []MyOrgUnitInfo{{OrgUnit: OrgUnitInfo{Id: 6001, Code: "MTH1003"}}},
		})
	}))
	t.Cleanup(srv.Close)

	got, err := NewLiveClient(srv.URL, srv.Client()).MyEnrollments(context.Background())
	if err != nil {
		t.Fatalf("MyEnrollments: %v", err)
	}
	if len(got) != 1 || got[0].OrgUnit.Code != "MTH1003" {
		t.Errorf("got %+v, want one MTH1003 enrollment", got)
	}
}

// Quizzes pages with a Next link rather than a bookmark. Two paging
// conventions in one API is D2L's, not ours, and getting the second one wrong
// silently truncates a course's exam list.
func TestLiveQuizzesNextPaging(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := 0
		if p := r.URL.Query().Get("page"); p != "" {
			page, _ = strconv.Atoi(p)
		}
		out := QuizListPage{Objects: []Quiz{{QuizId: 100 + page, SortOrder: page}}}
		if page < 2 {
			// Relative on the first hop, absolute on the second: both shapes
			// appear in the wild and both must resolve.
			next := "/d2l/api/le/1.9/6001/quizzes/?page=" + strconv.Itoa(page+1)
			if page == 1 {
				next = srv.URL + next
			}
			out.Next = &next
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)

	got, err := NewLiveClient(srv.URL, srv.Client()).Quizzes(context.Background(), 6001)
	if err != nil {
		t.Fatalf("Quizzes: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d quizzes across 3 pages, want 3", len(got))
	}
	for i, q := range got {
		if q.QuizId != 100+i {
			t.Errorf("page %d returned %+v — pages dropped or out of order", i, q)
		}
	}
}

// A Next link pointing at another host must not be followed: doing so would
// send the caller's bearer token to whatever origin the response named.
func TestLiveQuizzesRejectsOffHostNext(t *testing.T) {
	var leaked bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = true
		json.NewEncoder(w).Encode(QuizListPage{})
	}))
	t.Cleanup(attacker.Close)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next := attacker.URL + "/d2l/api/le/1.9/6001/quizzes/"
		json.NewEncoder(w).Encode(QuizListPage{
			Next:    &next,
			Objects: []Quiz{{QuizId: 1}},
		})
	}))
	t.Cleanup(srv.Close)

	got, err := NewLiveClient(srv.URL, srv.Client()).Quizzes(context.Background(), 6001)
	if err != nil {
		t.Fatalf("Quizzes: %v", err)
	}
	if leaked {
		t.Error("followed a paging link to another host")
	}
	if len(got) != 1 {
		t.Errorf("got %d quizzes, want the one page that was served", len(got))
	}
}

func TestLiveClasslistPaging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The route is named "paged" and takes a bookmark, but hands that
		// bookmark back inside a Next link rather than a PagingInfo block —
		// verified against a live tenant, where the bookmark shape decoded to
		// zero people on a full page and reported no error.
		page := 0
		if b := r.URL.Query().Get("bookmark"); b != "" {
			page, _ = strconv.Atoi(b)
		}
		out := ClasslistPage{
			Objects: []ClasslistUser{{Identifier: strconv.Itoa(500 + page)}},
		}
		if page+1 < 3 {
			next := fmt.Sprintf("/d2l/api/le/%s/6001/classlist/paged/?bookmark=%d", testLEVersion, page+1)
			out.Next = &next
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)

	got, err := NewLiveClient(srv.URL, srv.Client()).Classlist(context.Background(), 6001)
	if err != nil {
		t.Fatalf("Classlist: %v", err)
	}
	// A large course is exactly where truncation would go unnoticed — the
	// answer still looks like a roster, just missing people.
	if len(got) != 3 {
		t.Fatalf("got %d people across 3 pages, want 3", len(got))
	}
}

// A zero-value LiveClient must still build valid URLs. Left unhandled, an empty
// version produces "/d2l/api/le//6001/..." and every call 404s for a reason
// that looks like a missing course.
func TestLiveVersionDefaults(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	c := &LiveClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, err := c.ContentRoot(context.Background(), 6001); err != nil {
		t.Fatalf("ContentRoot: %v", err)
	}
	want := "/d2l/api/le/" + DefaultLEVersion + "/6001/content/root/"
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}
