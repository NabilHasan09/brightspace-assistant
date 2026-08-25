package mcptools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// courseCache memoizes the enrollments call. Every single-course tool resolves
// a code before it can do anything, so without this a three-tool answer costs
// three extra round trips.
//
// Cached for the process lifetime, which for a stdio server is one client
// session. Enrollments changing mid-session is not a case worth invalidating
// for. Only successful loads are cached, so a transient failure does not
// poison the process.
type courseCache struct {
	mu    sync.Mutex
	units []brightspace.MyOrgUnitInfo
	ok    bool
}

// enrollments returns every org unit the student belongs to, courses and
// otherwise.
func (s *Server) enrollments(ctx context.Context) ([]brightspace.MyOrgUnitInfo, error) {
	s.courses.mu.Lock()
	defer s.courses.mu.Unlock()

	if s.courses.ok {
		return s.courses.units, nil
	}
	units, err := s.client.MyEnrollments(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing enrollments: %w", err)
	}
	s.courses.units, s.courses.ok = units, true
	return units, nil
}

// isCourse filters enrollments down to actual classes. Students are also
// enrolled in departments and semesters, and offering those as courses would
// send every other tool looking for a gradebook that cannot exist.
func isCourse(u brightspace.MyOrgUnitInfo) bool {
	return u.OrgUnit.Type.Id == brightspace.CourseOfferingTypeID ||
		strings.Contains(strings.ToLower(u.OrgUnit.Type.Code), "course")
}

// inSession reports whether a course is running as of now.
//
// This is not the same question as Access.IsActive, which a real tenant
// reports true for every enrollment a student has ever had. Asking "what's due
// this week" against that answer fans every cross-course tool out over years of
// finished classes, so the dates are what "my courses" has to mean.
//
// A course with no dates is treated as running: self-paced org units — the
// onboarding tutorial, the annual compliance training — are open indefinitely
// and genuinely have no term.
func inSession(u brightspace.MyOrgUnitInfo, now time.Time) bool {
	if !u.Access.IsActive || !u.Access.CanAccess {
		return false
	}
	if start := u.Access.StartDate; start != nil && now.Before(*start) {
		return false
	}
	if end := u.Access.EndDate; end != nil && now.After(*end) {
		return false
	}
	return true
}

// allCourses returns every class the student has ever been enrolled in.
//
// If the type filter matches nothing but enrollments did come back, everything
// is returned instead. That is the deliberate degradation for
// CourseOfferingTypeID being wrong on an institution that numbers its org unit
// types differently: showing a department alongside two real courses is a
// cosmetic problem, while showing nothing makes every tool in this package
// unusable and looks identical to "you aren't enrolled in anything".
func (s *Server) allCourses(ctx context.Context) ([]brightspace.OrgUnitInfo, error) {
	units, err := s.enrollments(ctx)
	if err != nil {
		return nil, err
	}
	courses := make([]brightspace.OrgUnitInfo, 0, len(units))
	for _, u := range units {
		if isCourse(u) {
			courses = append(courses, u.OrgUnit)
		}
	}
	if len(courses) == 0 {
		bare := make([]brightspace.OrgUnitInfo, 0, len(units))
		for _, u := range units {
			bare = append(bare, u.OrgUnit)
		}
		return bare, nil
	}
	return courses, nil
}

// courseList returns the classes currently in session — what a student means
// by "my courses".
//
// Falls back to every course when nothing is running, because that is the
// ordinary state of a student between semesters, and an empty course list
// leaves every other tool with nothing to resolve against.
func (s *Server) courseList(ctx context.Context) ([]brightspace.OrgUnitInfo, error) {
	units, err := s.enrollments(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	current := make([]brightspace.OrgUnitInfo, 0, len(units))
	for _, u := range units {
		if isCourse(u) && inSession(u, now) {
			current = append(current, u.OrgUnit)
		}
	}
	if len(current) == 0 {
		return s.allCourses(ctx)
	}
	return current, nil
}

// normalizeCourseKey reduces a course code or title to letters and digits,
// upper-cased, so that the many ways one course is written all collapse to the
// same key: "MTH 4360", "mth-4360", and the catalog number buried inside
// "BAR01_MTH_4360_1262_1_26545" all become MTH4360.
func normalizeCourseKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}

// matchCourses finds the courses a student's phrasing could mean.
//
// Exact hits win outright: a course whose code normalizes to exactly the query
// is never reported as ambiguous just because the query also appears inside
// some other course's title. Only when nothing matches exactly does this fall
// back to substring matching, which is what makes a bare catalog number find
// the institution-prefixed code a real tenant actually returns.
func matchCourses(courses []brightspace.OrgUnitInfo, query string) []brightspace.OrgUnitInfo {
	q := normalizeCourseKey(query)
	if q == "" {
		return nil
	}
	var exact, partial []brightspace.OrgUnitInfo
	for _, c := range courses {
		code, name := normalizeCourseKey(c.Code), normalizeCourseKey(c.Name)
		switch {
		case code == q || name == q:
			exact = append(exact, c)
		case strings.Contains(code, q) || strings.Contains(name, q):
			partial = append(partial, c)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

// resolveCourse turns what a student says into what the API wants.
//
// Accepts a catalog number in any spelling ("MTH 4360", "mth4360"), the course
// title or a distinctive part of it, or a raw org unit id. Matching is
// restricted to courses, so a department does not resolve.
//
// Courses in session are searched first. A finished course still resolves —
// "what did I get in Calculus II" is a fair question — but a current course
// always wins a tie against a past one, which is what a student means when two
// terms share a catalog number.
func (s *Server) resolveCourse(ctx context.Context, code string) (brightspace.OrgUnitInfo, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return brightspace.OrgUnitInfo{}, fmt.Errorf("no course given: pass a course code from list_courses")
	}

	current, err := s.courseList(ctx)
	if err != nil {
		return brightspace.OrgUnitInfo{}, err
	}
	all, err := s.allCourses(ctx)
	if err != nil {
		return brightspace.OrgUnitInfo{}, err
	}

	known := make([]string, 0, len(current))
	for _, c := range current {
		known = append(known, c.Code)
	}

	if id, err := strconv.Atoi(code); err == nil {
		for _, c := range all {
			if c.Id == id {
				return c, nil
			}
		}
		return brightspace.OrgUnitInfo{}, errNoCourse(code, known)
	}

	for _, pool := range [][]brightspace.OrgUnitInfo{current, all} {
		matches := matchCourses(pool, code)
		if len(matches) == 0 {
			continue
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		// Several enrollments share the catalog number: multiple sections, or
		// the same class taken in different terms. Guessing picks the wrong
		// gradebook silently, so make the caller choose.
		labels := make([]string, 0, len(matches))
		for _, m := range matches {
			labels = append(labels, fmt.Sprintf("%s (id %d)", m.Name, m.Id))
		}
		return brightspace.OrgUnitInfo{}, fmt.Errorf(
			"%q matches more than one enrollment: %s — call this tool again with the numeric id",
			code, strings.Join(labels, ", "))
	}
	return brightspace.OrgUnitInfo{}, errNoCourse(code, known)
}

type courseSummary struct {
	Code string `json:"code" jsonschema:"course code, what every other tool takes"`
	Name string `json:"name" jsonschema:"full course title"`

	// Id is what disambiguates when a catalog number resolves to more than one
	// enrollment. The resolver's error tells the caller to retry with the
	// numeric id, so that id has to be reachable from somewhere.
	Id int `json:"id" jsonschema:"org unit id, for when a code matches more than one course"`
}

type listCoursesOutput struct {
	Courses []courseSummary `json:"courses"`
}

func (s *Server) listCourses(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listCoursesOutput, error) {
	courses, err := s.courseList(ctx)
	if err != nil {
		return nil, listCoursesOutput{}, err
	}
	out := listCoursesOutput{Courses: make([]courseSummary, 0, len(courses))}
	for _, c := range courses {
		out.Courses = append(out.Courses, courseSummary{Code: c.Code, Name: c.Name, Id: c.Id})
	}
	return nil, out, nil
}
