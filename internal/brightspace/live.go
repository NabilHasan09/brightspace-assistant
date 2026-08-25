package brightspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// D2L versions its LP (Learning Platform) and LE (Learning Environment) APIs
// independently, and the docs write both as "(version)" rather than a number.
//
// These defaults are UNVERIFIED — a starting point, not a fact. A tenant
// publishes the versions it actually supports at /d2l/api/versions/, which
// needs no authentication. Check it on first connection and set the fields
// explicitly rather than trusting these.
const (
	DefaultLPVersion = "1.9"
	DefaultLEVersion = "1.9"
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
		return nil, fmt.Errorf("brightspace: GET %s: %w", rawURL, err)
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

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("brightspace: decode %s: %w", rawURL, err)
	}
	return nil
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

func (c *LiveClient) MyEvents(ctx context.Context, orgUnitIDs []int, start, end time.Time) ([]CalendarEvent, error) {
	q := url.Values{}
	q.Set("startDateTime", start.UTC().Format(time.RFC3339))
	q.Set("endDateTime", end.UTC().Format(time.RFC3339))
	if len(orgUnitIDs) > 0 {
		ids := make([]string, len(orgUnitIDs))
		for i, id := range orgUnitIDs {
			ids[i] = strconv.Itoa(id)
		}
		q.Set("orgUnitIdsCSV", strings.Join(ids, ","))
	}

	var events []CalendarEvent
	if err := c.getJSON(ctx, c.leURL("/calendar/events/myEvents/", q), &events); err != nil {
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
// are real; see QuizListPage.
func (c *LiveClient) Quizzes(ctx context.Context, orgUnitID int) ([]Quiz, error) {
	out := []Quiz{}
	next := c.leURL(fmt.Sprintf("/%d/quizzes/", orgUnitID), nil)

	for next != "" {
		var page QuizListPage
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
			// somewhere it should not go.
			if resolved := c.resolveNext(*page.Next); resolved != current {
				next = resolved
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
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

func (c *LiveClient) Classlist(ctx context.Context, orgUnitID int) ([]ClasslistUser, error) {
	out := []ClasslistUser{}
	q := url.Values{}

	for {
		var page ClasslistPage
		u := c.leURL(fmt.Sprintf("/%d/classlist/paged/", orgUnitID), q)
		if err := c.getJSON(ctx, u, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Items...)

		next := page.PagingInfo.Bookmark
		if !page.PagingInfo.HasMoreItems || next == "" || next == q.Get("bookmark") {
			return out, nil
		}
		q.Set("bookmark", next)
	}
}

func (c *LiveClient) SubmitToDropbox(ctx context.Context, orgUnitID, folderID int, comment string, files []Upload) (*Submission, error) {
	// v2. The request is a multipart POST, and it is the only write in this
	// package — see the constraints on Client.SubmitToDropbox before building
	// it.
	return nil, ErrNotImplemented
}
