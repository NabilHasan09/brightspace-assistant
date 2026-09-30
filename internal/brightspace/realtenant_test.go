package brightspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in probe against a real Brightspace tenant.
//
// Every other test in this package answers from fixtures, which proves URL
// construction and decoding but cannot prove the routes exist or that the
// schemas match — both sides of that contract are written here, so a wrong
// path passes green. These tests close that gap, and are skipped unless
// D2L_HOST and D2L_COOKIE are both set:
//
//	D2L_HOST=https://brightspace.example.edu \
//	D2L_COOKIE='d2lSecureSessionVal=...; d2lSessionVal=...' \
//	go test ./internal/brightspace/ -run RealTenant -v
//
// Read-only by construction: nothing here submits, posts, or marks anything
// read. The output contains real course names and ids, so it belongs in a
// terminal rather than a CI log.

func realTenant(t *testing.T) (*LiveClient, context.Context) {
	t.Helper()

	host := os.Getenv("D2L_HOST")
	cookie, cookieFile := os.Getenv("D2L_COOKIE"), os.Getenv("D2L_COOKIE_FILE")

	switch {
	case host == "" && cookie == "" && cookieFile == "":
		t.Skip("set D2L_HOST and either D2L_COOKIE_FILE or D2L_COOKIE to probe a real tenant")
	case host == "":
		t.Skip("a cookie is set but D2L_HOST is not")
	case cookie == "" && cookieFile == "":
		// Failing loudly rather than skipping: a host with no credentials is a
		// misconfiguration, and silently skipping it looks like a pass.
		t.Fatal("D2L_HOST is set but no cookie is — run cmd/d2l-login and point D2L_COOKIE_FILE at the result")
	}

	// Generous because the sweep makes a dozen sequential calls and the paged
	// routes page. This is a backstop against a hung connection, not a latency
	// budget — a real tenant answers each of these in well under a second.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	// The file wins: a sweep long enough to outlive a session can then be fixed
	// by refreshing the file, rather than restarting the run.
	auth := CookieClient(cookie)
	if cookieFile != "" {
		auth = CookieFileClient(cookieFile)
	}
	return NewLiveClient(host, auth), ctx
}

// courseUnderTest reads D2L_ORG_UNIT, which the per-course probes need and the
// enrollments probe prints.
func courseUnderTest(t *testing.T) int {
	t.Helper()

	orgUnit := os.Getenv("D2L_ORG_UNIT")
	if orgUnit == "" {
		t.Skip("set D2L_ORG_UNIT to a course id from TestRealTenantEnrollments")
	}
	id, err := strconv.Atoi(orgUnit)
	if err != nil {
		t.Fatalf("D2L_ORG_UNIT must be a numeric course id, got %q: %v", orgUnit, err)
	}
	return id
}

// TestRealTenantVersions answers the question the fixtures cannot: which API
// versions this tenant actually serves. The defaults in this package are
// guesses, and a wrong version 404s in a way indistinguishable from a route
// that does not exist.
func TestRealTenantVersions(t *testing.T) {
	c, ctx := realTenant(t)

	versions, err := c.Versions(ctx)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) == 0 {
		t.Fatal("tenant reported no product versions")
	}

	supported := make(map[string][]string, len(versions))
	for _, v := range versions {
		supported[strings.ToLower(v.ProductCode)] = v.SupportedVersions
		t.Logf("%-6s latest %-8s supported %v", v.ProductCode, v.LatestVersion, v.SupportedVersions)
	}

	// The configured version has to be one the tenant will actually answer on.
	for _, tc := range []struct{ product, configured string }{
		{"lp", c.LPVersion},
		{"le", c.LEVersion},
	} {
		got, ok := supported[tc.product]
		if !ok {
			t.Errorf("tenant serves no %q product at all", tc.product)
			continue
		}
		if !contains(got, tc.configured) {
			t.Errorf("configured %s version %q is not supported by this tenant; supported: %v",
				tc.product, tc.configured, got)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestRealTenantEnrollments is the schema check the fixtures could not do.
//
// The "OrgUnit" vs "OrgUnitInfo" key is the specific failure this guards: a
// missing key is not an unmarshal error, so the wrong spelling returns the
// right number of enrollments with every field zeroed. Counting rows would
// have passed.
func TestRealTenantEnrollments(t *testing.T) {
	c, ctx := realTenant(t)

	got, err := c.MyEnrollments(ctx)
	if err != nil {
		t.Fatalf("MyEnrollments: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("tenant returned no enrollments — expected at least one course")
	}

	var zeroed int
	for _, u := range got {
		if u.OrgUnit == (OrgUnitInfo{}) {
			zeroed++
			continue
		}
		window := "no dates"
		if s, e := u.Access.StartDate, u.Access.EndDate; s != nil && e != nil {
			window = s.Format("2006-01-02") + " to " + e.Format("2006-01-02")
		}
		t.Logf("%7d  %-34s  %-10s  %s", u.OrgUnit.Id, u.OrgUnit.Code, window, u.OrgUnit.Name)
	}
	if zeroed > 0 {
		t.Errorf("%d of %d enrollments decoded to a zero org unit — the Items[].OrgUnit key does not match this tenant",
			zeroed, len(got))
	}

	// Every entry should be a course offering, since MyEnrollments asks the
	// tenant to filter. If departments come back anyway, this institution
	// numbers its org unit types differently and CourseOfferingTypeID is wrong.
	for _, u := range got {
		if u.OrgUnit.Type.Id != CourseOfferingTypeID {
			t.Errorf("enrollment %d is type %d (%q), not the course offering type %d — the orgUnitTypeId filter does not mean what this package assumes",
				u.OrgUnit.Id, u.OrgUnit.Type.Id, u.OrgUnit.Type.Code, CourseOfferingTypeID)
		}
	}
}

// TestRealTenantContentRoot exercises an LE route, which the enrollments probe
// cannot: LP and LE are versioned independently, so LP working says nothing
// about LE. Set D2L_ORG_UNIT to a course id from the enrollments output.
func TestRealTenantContentRoot(t *testing.T) {
	c, ctx := realTenant(t)
	id := courseUnderTest(t)

	modules, err := c.ContentRoot(ctx, id)
	if err != nil {
		t.Fatalf("ContentRoot(%d): %v", id, err)
	}
	t.Logf("%d top-level modules", len(modules))
	for _, m := range modules {
		t.Logf("  [%d] %-50s hidden=%v children=%d", m.Id, m.Title, m.IsHidden, len(m.Structure))
	}
}

// routeResult is one row of the sweep's report. Recording the outcome instead
// of asserting inline is the point: a sweep that stops at the first failure
// tells you about one route, when the useful signal is which routes work.
type routeResult struct {
	name    string
	product string // "lp" or "le" — which version pins this route
	summary string
	err     error

	// tolerated reports whether an error is an expected answer rather than a
	// defect, and says why. A route that is legitimately switched off for a
	// course must not turn the sweep red forever.
	tolerated func(error) (string, bool)
}

// TestRealTenantSweep calls every read route against one course and reports
// each result, rather than stopping at the first failure.
//
// This is what the narrower probes cannot answer. Twelve of the thirteen routes
// are LE, versioned independently of LP, so a wrong LEVersion 404s all twelve
// identically — and a 404 from a wrong version is indistinguishable, at the
// call site, from a course that simply has no discussions. The table separates
// them: one 404 is missing data, twelve is a wrong version.
//
// Read-only. Set D2L_ORG_UNIT to a course id from TestRealTenantEnrollments.
func TestRealTenantSweep(t *testing.T) {
	c, ctx := realTenant(t)
	id := courseUnderTest(t)

	var results []routeResult
	run := func(name, product string, tolerated func(error) (string, bool), fn func() (string, error)) {
		summary, err := fn()
		results = append(results, routeResult{
			name: name, product: product, summary: summary,
			err: err, tolerated: tolerated,
		})
	}

	// A course still in progress has released no final grade.
	noFinalGrade := func(err error) (string, bool) {
		return "no final grade released yet", errors.Is(err, ErrNotFound)
	}
	// An instructor can switch the Classlist tool off per course.
	rosterWithheld := func(err error) (string, bool) {
		return "roster not published for this course", errors.Is(err, ErrUnauthorized)
	}
	never := func(error) (string, bool) { return "", false }

	// Ids discovered by earlier routes and consumed by later ones. Discussions
	// nest three deep, and content two, so the sweep has to walk down rather
	// than call each route in isolation.
	var (
		moduleID, forumID, topicID int
		moduleTitle, forumTitle    string
	)

	run("MyEnrollments", "lp", never, func() (string, error) {
		units, err := c.MyEnrollments(ctx)
		return fmt.Sprintf("%d enrollments", len(units)), err
	})

	run("ContentRoot", "le", never, func() (string, error) {
		modules, err := c.ContentRoot(ctx, id)
		if err == nil && len(modules) > 0 {
			moduleID, moduleTitle = modules[0].Id, modules[0].Title
		}
		return fmt.Sprintf("%d top-level modules", len(modules)), err
	})

	run("ModuleStructure", "le", never, func() (string, error) {
		if moduleID == 0 {
			return "skipped: ContentRoot returned no module to descend into", nil
		}
		items, err := c.ModuleStructure(ctx, id, moduleID)
		return fmt.Sprintf("%d items under %q", len(items), moduleTitle), err
	})

	run("MyGradeValues", "le", never, func() (string, error) {
		grades, err := c.MyGradeValues(ctx, id)
		return fmt.Sprintf("%d graded items", len(grades)), err
	})

	// A course that has not released a final grade answers 404, which is the
	// documented behavior rather than a broken route.
	run("MyFinalGrade", "le", noFinalGrade, func() (string, error) {
		grade, err := c.MyFinalGrade(ctx, id)
		if err != nil || grade == nil {
			return "no final grade released", err
		}
		return "final grade: " + grade.DisplayedGrade, nil
	})

	run("DropboxFolders", "le", never, func() (string, error) {
		folders, err := c.DropboxFolders(ctx, id)
		return fmt.Sprintf("%d assignment folders", len(folders)), err
	})

	run("MyEvents", "le", never, func() (string, error) {
		// A wide window: the point is whether the route answers, and a course
		// with nothing due in the next week would otherwise read as a failure.
		start := time.Now().AddDate(0, -6, 0)
		events, err := c.MyEvents(ctx, []int{id}, start, start.AddDate(1, 0, 0))
		return fmt.Sprintf("%d calendar events in a 12-month window", len(events)), err
	})

	run("NewsItems", "le", never, func() (string, error) {
		news, err := c.NewsItems(ctx, id, time.Now().AddDate(0, -6, 0))
		return fmt.Sprintf("%d announcements in 6 months", len(news)), err
	})

	run("DiscussionForums", "le", never, func() (string, error) {
		forums, err := c.DiscussionForums(ctx, id)
		if err == nil && len(forums) > 0 {
			forumID, forumTitle = forums[0].ForumId, forums[0].Name
		}
		return fmt.Sprintf("%d forums", len(forums)), err
	})

	run("DiscussionTopics", "le", never, func() (string, error) {
		if forumID == 0 {
			return "skipped: no forum in this course", nil
		}
		topics, err := c.DiscussionTopics(ctx, id, forumID)
		if err == nil && len(topics) > 0 {
			topicID = topics[0].TopicId
		}
		return fmt.Sprintf("%d topics under %q", len(topics), forumTitle), err
	})

	run("DiscussionPosts", "le", never, func() (string, error) {
		if topicID == 0 {
			return "skipped: no topic to read posts from", nil
		}
		posts, err := c.DiscussionPosts(ctx, id, forumID, topicID)
		return fmt.Sprintf("%d posts", len(posts)), err
	})

	run("Quizzes", "le", never, func() (string, error) {
		quizzes, err := c.Quizzes(ctx, id)
		return fmt.Sprintf("%d quizzes", len(quizzes)), err
	})

	// Classlist is an LE route despite reading like enrollment data, and the
	// LP spelling 404s. Field visibility is configured per tenant, so a name
	// with no email is a correct answer here, not a partial one.
	run("Classlist", "le", rosterWithheld, func() (string, error) {
		users, err := c.Classlist(ctx, id)
		return fmt.Sprintf("%d people", len(users)), err
	})

	// Report before asserting, so a failing run still shows everything it
	// learned rather than only the first thing that broke.
	t.Logf("sweep of org unit %d — LP %s, LE %s", id, c.LPVersion, c.LEVersion)
	var answered, broken, unauthorized int
	for _, r := range results {
		why, ok := "", false
		if r.err != nil && r.tolerated != nil {
			why, ok = r.tolerated(r.err)
		}
		switch {
		case r.err == nil:
			answered++
			t.Logf("  ok    %-3s %-17s %s", r.product, r.name, r.summary)
		case ok:
			// Expected, so it is not a failure — but still worth printing, since
			// "the roster is withheld" is a fact about the tenant that callers
			// have to handle rather than something to fix here.
			answered++
			t.Logf("  n/a   %-3s %-17s %s", r.product, r.name, why)
		default:
			broken++
			if errors.Is(r.err, ErrUnauthorized) {
				unauthorized++
			}
			t.Logf("  FAIL  %-3s %-17s %v", r.product, r.name, r.err)
		}
	}
	t.Logf("%d of %d routes answered", answered, len(results))

	// Every route failing on auth is one problem, not thirteen: the session
	// expired. Saying so beats thirteen identical failures that look like the
	// client is broken.
	if unauthorized == len(results) {
		t.Fatal("every route was rejected — the session cookie is expired or wrong; copy a fresh Cookie header from a logged-in browser request")
	}

	for _, r := range results {
		if r.err == nil {
			continue
		}
		if r.tolerated != nil {
			if _, ok := r.tolerated(r.err); ok {
				continue
			}
		}
		switch {
		case errors.Is(r.err, ErrNotFound):
			t.Errorf("%s: 404. Either this tenant does not serve the route, or the configured %s version %q is wrong — check TestRealTenantVersions",
				r.name, r.product, map[string]string{"lp": c.LPVersion, "le": c.LEVersion}[r.product])
		case errors.Is(r.err, ErrUnauthorized):
			t.Errorf("%s: rejected. Other routes succeeded, so the session is valid and this route is restricted for this account: %v", r.name, r.err)
		default:
			t.Errorf("%s: %v", r.name, r.err)
		}
	}
}
