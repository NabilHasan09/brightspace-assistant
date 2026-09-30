package brightspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// D2L versions its LP (Learning Platform) and LE (Learning Environment) APIs
// independently, and the docs write both as "(version)" rather than a number.
// Note these are not decimals: 1.9 is minor version 9, far older than 1.30.
//
// Verified against brightspace.cuny.edu, which serves LP through 1.63 and LE
// through 1.97 and still supports every version back to 1.0.
//
// Version choice is not cosmetic, because routes were added over time and an
// absent one answers 404 — indistinguishable, at the call site, from a course
// that simply has no quizzes. Measured floors, below which a route 404s:
//
//	calendar/events/myEvents/   LE 1.18
//	{orgUnit}/classlist/paged/  LE 1.26
//	{orgUnit}/quizzes/          LE 1.28
//	everything else             LE 1.9, LP 1.9
//
// So LE 1.9 silently returned no quizzes and no calendar events at all. The
// pin is the tenant's latest rather than that 1.28 floor because the types in
// this package are written from D2L's current docs, and asking an old version
// for a current schema is how fields come back zeroed with no error. Newer
// versions are strictly additive here — LP 1.63 adds HomeUrl, ImageUrl, and
// PinDate to the enrollments response and removes nothing.
//
// Both are overridable per client. A tenant on older Brightspace can drop to
// LE 1.28 and lose nothing this package reads; /d2l/api/versions/ reports what
// any tenant supports and needs no authentication.
const (
	DefaultLPVersion = "1.63"
	DefaultLEVersion = "1.97"
)

// maxErrorBody caps how much of a failed response is kept for the message.
// D2L returns short JSON errors, but a misconfigured proxy can return an
// endless HTML page, and this runs unattended.
const maxErrorBody = 4 << 10

// LiveClient talks to a real Brightspace tenant.
//
// Nothing here has been run against a live tenant. The routes and response
// shapes come from D2L's published docs, so treat first connection as a
// verification pass: capture real responses and diff them against testdata/
// rather than assuming the fixtures are right.
type LiveClient struct {
	// BaseURL is the tenant root with scheme and no trailing slash, e.g.
	// "https://brightspace.cuny.edu".
	BaseURL string

	// HTTPClient carries authentication. LiveClient never touches tokens
	// itself — OAuth2 belongs in a RoundTripper, so refresh happens beneath
	// this type and every method stays a plain GET. Build one with
	// oauth2.Config.Client, or StaticTokenClient for a short-lived token.
	//
	// Retry and rate-limit backoff belong in the same place for the same
	// reason. Neither is implemented yet.
	HTTPClient *http.Client

	// LPVersion and LEVersion default to the constants above when empty.
	LPVersion string
	LEVersion string
}

func NewLiveClient(baseURL string, httpClient *http.Client) *LiveClient {
	if httpClient == nil {
		httpClient = DefaultHTTPClient()
	}
	return &LiveClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: httpClient,
		LPVersion:  DefaultLPVersion,
		LEVersion:  DefaultLEVersion,
	}
}

// DefaultHTTPClient bounds connection setup and the wait for response headers,
// but deliberately leaves http.Client.Timeout unset. That field caps the whole
// exchange including reading the body, which would abort a large lecture
// download partway through and report it as a timeout rather than a partial
// file. Per-call deadlines belong on the context.
func DefaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}

// StaticTokenClient attaches a fixed bearer token to every request. Useful for
// poking at a tenant by hand with a token copied from a browser session; real
// use wants oauth2.Config.Client, which refreshes when the token expires.
func StaticTokenClient(token string) *http.Client {
	c := DefaultHTTPClient()
	c.Transport = &bearerTransport{token: token, next: c.Transport}
	return c
}

type bearerTransport struct {
	token string
	next  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify the request it is handed.
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(clone)
}

// CookieClient authenticates as a logged-in browser session rather than as an
// OAuth client, by replaying the Cookie header from one.
//
// This exists because the tenant's own web UI calls these same API routes with
// nothing but a session cookie, which is a door a student can open without an
// admin registering an OAuth client. Pass the raw header value, exactly as
// copied from a browser's request:
//
//	brightspace.CookieClient("d2lSecureSessionVal=...; d2lSessionVal=...")
//
// The tradeoff against a bearer token is lifetime and failure mode. Sessions
// expire in hours, and an expired one does not come back 401 — the tenant
// redirects to single sign-on and serves an HTML login page with status 200.
// getJSON checks the content type for exactly that reason.
//
// A long-running process wants CookieFileClient instead, so a fresh session can
// be supplied without a restart.
func CookieClient(cookie string) *http.Client {
	return cookieClient(func() (string, error) {
		return validCookie(cookie, "the cookie passed to CookieClient")
	})
}

// CookieFileClient reads the cookie from path before every request rather than
// capturing it once at construction.
//
// Sessions expire in hours, and a server that captured the string at startup has
// to be restarted to pick up a new one — which over MCP stdio tears down the
// client's session too. Reading per request makes refreshing a session an
// overwrite of one file, and the next call succeeds with no restart.
//
// The read costs a few microseconds against a page the OS has cached, next to a
// network round trip, so it is not worth hiding behind an mtime check.
func CookieFileClient(path string) *http.Client {
	return cookieClient(func() (string, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("brightspace: reading session cookie: %w", err)
		}
		return validCookie(string(raw), path)
	})
}

func cookieClient(read func() (string, error)) *http.Client {
	c := DefaultHTTPClient()
	c.Transport = &cookieTransport{read: read, next: c.Transport}
	return c
}

// sessionCookieName is the cookie that actually authenticates. It is HttpOnly,
// so document.cookie in a browser console cannot see it — the value has to come
// from a request's headers or from the browser's own cookie store.
const sessionCookieName = "d2lSecureSessionVal"

// validCookie rejects a cookie that cannot possibly authenticate, and says which
// way it is wrong.
//
// Worth checking rather than letting the tenant answer: an empty cookie sends an
// unauthenticated request, which comes back 403, and a 403 reads as "this route
// is restricted for you" rather than "you sent no credentials". That misreading
// costs more time than the check does.
func validCookie(cookie, source string) (string, error) {
	// pbpaste and editors both leave a trailing newline, which is harmless in a
	// header but makes an otherwise empty file look like it has content.
	cookie = strings.TrimSpace(cookie)
	switch {
	case cookie == "":
		return "", fmt.Errorf("brightspace: %s is empty — copy the Cookie header from a logged-in browser request: %w",
			source, ErrUnauthorized)
	case !strings.Contains(cookie, sessionCookieName):
		return "", fmt.Errorf("brightspace: %s carries no %s, so it was copied from a request to some other host: %w",
			source, sessionCookieName, ErrUnauthorized)
	}
	return cookie, nil
}

type cookieTransport struct {
	read func() (string, error)
	next http.RoundTripper
}

func (t *cookieTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cookie, err := t.read()
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Cookie", cookie)
	return t.next.RoundTrip(clone)
}

// APIError is any non-200 response.
//
// It carries a prefix of the body because "status 500" alone is unactionable
// against someone else's tenant, which is exactly where this gets debugged.
// Statuses with a package sentinel unwrap to it, so callers keep writing
// errors.Is(err, ErrNotFound) without knowing which implementation answered.
type APIError struct {
	StatusCode int
	URL        string
	Body       string

	sentinel error
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("brightspace: %s: status %d", e.URL, e.StatusCode)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

func (e *APIError) Unwrap() error { return e.sentinel }

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (c *LiveClient) apiURL(product, version, path string, q url.Values) string {
	u := c.BaseURL + "/d2l/api/" + product + "/" + version + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (c *LiveClient) lpURL(path string, q url.Values) string {
	return c.apiURL("lp", orDefault(c.LPVersion, DefaultLPVersion), path, q)
}

func (c *LiveClient) leURL(path string, q url.Values) string {
	return c.apiURL("le", orDefault(c.LEVersion, DefaultLEVersion), path, q)
}

func (c *LiveClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// do issues the request and returns the response only on 200, with its body
// still open. Every other status becomes an APIError with the body drained and
// closed, so callers never have to decide whether they own it.
func (c *LiveClient) do(ctx context.Context, rawURL, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("brightspace: build request for %s: %w", rawURL, err)
	}
	req.Header.Set("Accept", accept)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		// No URL prefix of our own: http.Client already wraps transport errors
		// in a *url.Error that names the method and URL, so adding one prints
		// the same URL twice. That noise lands on the most common failure of
		// cookie auth, an expired session, which is where a legible message is
		// worth the most.
		return nil, fmt.Errorf("brightspace: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	return nil, statusError(resp, rawURL)
}

func statusError(resp *http.Response, rawURL string) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))

	e := &APIError{
		StatusCode: resp.StatusCode,
		URL:        rawURL,
		Body:       strings.TrimSpace(string(body)),
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		e.sentinel = ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		e.sentinel = ErrUnauthorized
	}
	return e
}

func (c *LiveClient) getJSON(ctx context.Context, rawURL string, dst any) error {
	resp, err := c.do(ctx, rawURL, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// An expired session is not a 401. The tenant redirects to single sign-on
	// and serves an HTML login page with status 200, so the only symptom is a
	// JSON syntax error pointing at "<" — which sends you hunting for a schema
	// bug when the real fix is to log in again.
	//
	// Matching HTML specifically rather than "not JSON": a server that omits
	// the header entirely is sloppy, not broken, and its body still decodes.
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/html") {
		return fmt.Errorf("brightspace: %s answered with HTML instead of JSON, which usually means the session expired and the request was redirected to a login page: %w",
			rawURL, ErrUnauthorized)
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("brightspace: decode %s: %w", rawURL, err)
	}
	return nil
}

// ProductVersion is one entry from /d2l/api/versions/, which reports the API
// versions a tenant actually serves.
type ProductVersion struct {
	ProductCode       string   `json:"ProductCode"`
	LatestVersion     string   `json:"LatestVersion"`
	SupportedVersions []string `json:"SupportedVersions"`
}

// Versions reports every product code the tenant serves and the versions it
// supports for each.
//
// This is the one route that is not itself versioned, which makes it the right
// first call against an unfamiliar tenant: LPVersion and LEVersion are guesses
// until something authoritative sets them, and a wrong version fails as a 404
// that looks identical to a route that does not exist.
//
// Not part of Client — it answers a question about the tenant rather than
// about a course, and MockClient has no meaningful answer to give.
func (c *LiveClient) Versions(ctx context.Context) ([]ProductVersion, error) {
	var out []ProductVersion
	if err := c.getJSON(ctx, c.BaseURL+"/d2l/api/versions/", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MyEnrollments is the one paged route in this set. A student has few
// enrollments, but the bookmark loop is the same everywhere D2L pages, and the
// classlist and discussion routes coming later will need it.
func (c *LiveClient) MyEnrollments(ctx context.Context) ([]MyOrgUnitInfo, error) {
	// Non-nil so an empty result compares equal to the mock's.
	out := []MyOrgUnitInfo{}

	// The tenant's own UI sends these three, and unfiltered the route returns
	// every org unit the student belongs to — semesters, departments, and the
	// org root alongside actual classes. isActive and canAccess do not narrow
	// to the current term; they stay true for enrollments years in the past.
	q := url.Values{}
	q.Set("orgUnitTypeId", strconv.Itoa(CourseOfferingTypeID))
	q.Set("isActive", "true")
	q.Set("canAccess", "true")

	for {
		var page MyEnrollmentsResponse
		if err := c.getJSON(ctx, c.lpURL("/enrollments/myenrollments/", q), &page); err != nil {
			return nil, err
		}
		out = append(out, page.Items...)

		// Continue only if the server claims more AND hands back a bookmark
		// that actually moved. A server that repeats a bookmark, or sets
		// HasMoreItems with none, would otherwise spin forever inside a job
		// that runs unattended every few hours.
		next := page.PagingInfo.Bookmark
		if !page.PagingInfo.HasMoreItems || next == "" || next == q.Get("bookmark") {
			return out, nil
		}
		q.Set("bookmark", next)
	}
}

func (c *LiveClient) ContentRoot(ctx context.Context, orgUnitID int) ([]Module, error) {
	var mods []Module
	u := c.leURL(fmt.Sprintf("/%d/content/root/", orgUnitID), nil)
	if err := c.getJSON(ctx, u, &mods); err != nil {
		return nil, err
	}
	return mods, nil
}

func (c *LiveClient) ModuleStructure(ctx context.Context, orgUnitID, moduleID int) ([]ContentObject, error) {
	var kids []ContentObject
	u := c.leURL(fmt.Sprintf("/%d/content/modules/%d/structure/", orgUnitID, moduleID), nil)
	if err := c.getJSON(ctx, u, &kids); err != nil {
		return nil, err
	}
	return kids, nil
}

// TopicFile streams the response body straight to the caller rather than
// buffering — a slide deck can be hundreds of megabytes.
//
// It cannot return ErrNotFileTopic. MockClient knows a topic is a link because
// it can see TopicType; over HTTP a link topic just fails, and which status D2L
// uses is unverified. This divergence is survivable because the only caller
// walks the content tree first and already has TopicType in hand — check it
// there rather than calling this and interpreting the failure.
func (c *LiveClient) TopicFile(ctx context.Context, orgUnitID, topicID int) (io.ReadCloser, string, error) {
	u := c.leURL(fmt.Sprintf("/%d/content/topics/%d/file", orgUnitID, topicID), nil)

	// Accept anything: this is the one route that returns bytes, not JSON.
	resp, err := c.do(ctx, u, "*/*")
	if err != nil {
		return nil, "", err
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return resp.Body, mimeType, nil
}

func (c *LiveClient) MyGradeValues(ctx context.Context, orgUnitID int) ([]GradeValue, error) {
	var grades []GradeValue
	u := c.leURL(fmt.Sprintf("/%d/grades/values/myGradeValues/", orgUnitID), nil)
	if err := c.getJSON(ctx, u, &grades); err != nil {
		return nil, err
	}
	return grades, nil
}

// MyFinalGrade returns ErrNotFound when the course releases no calculated
// final, which the tenant signals with a 404. That is a normal mid-semester
// state, not a failure, and must never degrade into a zero.
func (c *LiveClient) MyFinalGrade(ctx context.Context, orgUnitID int) (*GradeValue, error) {
	var final GradeValue
	u := c.leURL(fmt.Sprintf("/%d/grades/final/values/myGradeValue", orgUnitID), nil)
	if err := c.getJSON(ctx, u, &final); err != nil {
		return nil, err
	}
	return &final, nil
}

func (c *LiveClient) DropboxFolders(ctx context.Context, orgUnitID int) ([]DropboxFolder, error) {
	var folders []DropboxFolder
	u := c.leURL(fmt.Sprintf("/%d/dropbox/folders/", orgUnitID), nil)
	if err := c.getJSON(ctx, u, &folders); err != nil {
		return nil, err
	}
	return folders, nil
}

// d2lTimestamp is the only timestamp format the calendar route accepts.
//
// It is RFC 3339 with the milliseconds always present, including when they are
// zero. time.RFC3339 omits them and time.RFC3339Nano drops trailing zeros, so
// both produce "...T00:00:00Z" — which this route rejects as 400 Invalid
// Parameters. Verified against a live tenant; the docs do not mention it.
const d2lTimestamp = "2006-01-02T15:04:05.000Z"

func (c *LiveClient) MyEvents(ctx context.Context, orgUnitIDs []int, start, end time.Time) ([]CalendarEvent, error) {
	// The docs describe orgUnitIdsCSV as optional, but a live tenant answers
	// 400 without it. Refusing here beats sending a request that cannot
	// succeed, and the caller always knows which courses it means.
	if len(orgUnitIDs) == 0 {
		return nil, fmt.Errorf("brightspace: MyEvents needs at least one org unit id: the calendar route rejects an unscoped query")
	}

	ids := make([]string, len(orgUnitIDs))
	for i, id := range orgUnitIDs {
		ids[i] = strconv.Itoa(id)
	}

	q := url.Values{}
	q.Set("startDateTime", start.UTC().Format(d2lTimestamp))
	q.Set("endDateTime", end.UTC().Format(d2lTimestamp))
	q.Set("orgUnitIdsCSV", strings.Join(ids, ","))

	// Paged in the same Next-link envelope as quizzes, not a bare array. A
	// course with more events than one page would otherwise silently lose the
	// tail, which for a deadline list means missing exactly the work furthest
	// out.
	events, err := pageObjects[CalendarEvent](ctx, c, c.leURL("/calendar/events/myEvents/", q))
	if err != nil {
		return nil, err
	}

	// Re-apply [start, end) and sort locally. The server filters too, but
	// whether its end is inclusive is unverified and the route promises no
	// ordering. Doing both here is what makes LiveClient and MockClient answer
	// identically — which is the only thing that makes the mock worth building
	// everything else against.
	out := make([]CalendarEvent, 0, len(events))
	for _, e := range events {
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

func (c *LiveClient) NewsItems(ctx context.Context, orgUnitID int, since time.Time) ([]NewsItem, error) {
	var all []NewsItem
	u := c.leURL(fmt.Sprintf("/%d/news/", orgUnitID), nil)
	if err := c.getJSON(ctx, u, &all); err != nil {
		return nil, err
	}

	// Filtered here rather than in a query parameter because the route has
	// none. Same reasoning as MyEvents: the interface promises the behavior, so
	// both implementations owe it.
	out := make([]NewsItem, 0, len(all))
	for _, n := range all {
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

func (c *LiveClient) DiscussionForums(ctx context.Context, orgUnitID int) ([]Forum, error) {
	var forums []Forum
	u := c.leURL(fmt.Sprintf("/%d/discussions/forums/", orgUnitID), nil)
	if err := c.getJSON(ctx, u, &forums); err != nil {
		return nil, err
	}
	return forums, nil
}

func (c *LiveClient) DiscussionTopics(ctx context.Context, orgUnitID, forumID int) ([]DiscussionTopic, error) {
	var topics []DiscussionTopic
	u := c.leURL(fmt.Sprintf("/%d/discussions/forums/%d/topics/", orgUnitID, forumID), nil)
	if err := c.getJSON(ctx, u, &topics); err != nil {
		return nil, err
	}
	return topics, nil
}

func (c *LiveClient) DiscussionPosts(ctx context.Context, orgUnitID, forumID, topicID int) ([]Post, error) {
	var posts []Post
	u := c.leURL(fmt.Sprintf("/%d/discussions/forums/%d/topics/%d/posts/", orgUnitID, forumID, topicID), nil)
	if err := c.getJSON(ctx, u, &posts); err != nil {
		return nil, err
	}

	// Non-nil so an empty thread compares equal to the mock's.
	if posts == nil {
		posts = []Post{}
	}
	sort.Slice(posts, func(i, j int) bool {
		return posts[i].DatePosted.Before(posts[j].DatePosted)
	})
	return posts, nil
}

// Quizzes follows D2L's Next-link paging rather than the bookmark convention
// used by enrollments and the classlist. Both live in this file because both
// are real; see ObjectListPage.
func (c *LiveClient) Quizzes(ctx context.Context, orgUnitID int) ([]Quiz, error) {
	out, err := pageObjects[Quiz](ctx, c, c.leURL(fmt.Sprintf("/%d/quizzes/", orgUnitID), nil))
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
	return out, nil
}

// pageObjects walks a Next-linked list to the end and returns every object.
//
// A free function rather than a method because Go methods cannot take type
// parameters, and quizzes and the calendar both need this — the calendar
// arrives in the same envelope, which is only known because decoding it as a
// bare array failed against a live tenant.
func pageObjects[T any](ctx context.Context, c *LiveClient, first string) ([]T, error) {
	out := []T{}

	for next := first; next != ""; {
		var page ObjectListPage[T]
		if err := c.getJSON(ctx, next, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Objects...)

		current := next
		next = ""
		if page.Next != nil && *page.Next != "" {
			// Next is a link, absolute or tenant-relative. Resolving it against
			// BaseURL rather than trusting it whole keeps a malformed or
			// off-host value from redirecting an authenticated request
			// somewhere it should not go. Equal to the current URL means the
			// tenant is pointing at itself, which would loop forever.
			if resolved := c.resolveNext(*page.Next); resolved != current {
				next = resolved
			}
		}
	}
	return out, nil
}

// resolveNext forces a paging link back onto the tenant host. A link that
// points elsewhere is dropped rather than followed, because following it would
// send the caller's bearer token to another origin.
func (c *LiveClient) resolveNext(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return ""
	}
	if u.Host != "" && u.Host != base.Host {
		return ""
	}
	return base.ResolveReference(u).String()
}

// Classlist pages 25 people at a time. Walking every page matters more here
// than elsewhere: a large lecture puts the instructor and TAs on an arbitrary
// page, so stopping at the first one answers "who is my TA?" with 25 students
// and no teacher.
//
// A course whose instructor has disabled the Classlist tool answers 403. That
// is a per-course configuration rather than a broken route or an expired
// session — callers should degrade to "not published for this course".
func (c *LiveClient) Classlist(ctx context.Context, orgUnitID int) ([]ClasslistUser, error) {
	return pageObjects[ClasslistUser](ctx, c, c.leURL(fmt.Sprintf("/%d/classlist/paged/", orgUnitID), nil))
}

func (c *LiveClient) SubmitToDropbox(ctx context.Context, orgUnitID, folderID int, comment string, files []Upload) (*Submission, error) {
	// v2. The request is a multipart POST, and it is the only write in this
	// package — see the constraints on Client.SubmitToDropbox before building
	// it.
	return nil, ErrNotImplemented
}
