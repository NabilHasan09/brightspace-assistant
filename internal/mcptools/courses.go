package mcptools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// courseOfferingTypeID is D2L's conventional org unit type for a course
// offering, as opposed to a department, semester, or the org root.
//
// UNVERIFIED against a real tenant. Org unit types are configurable per
// institution, so a tenant may use a different id or a custom type name — see
// isCourse for how that failure degrades.
const courseOfferingTypeID = 3

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
	units []brightspace.OrgUnitInfo
	ok    bool
}

// enrollments returns every org unit the student belongs to, courses and
// otherwise.
func (s *Server) enrollments(ctx context.Context) ([]brightspace.OrgUnitInfo, error) {
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
func isCourse(u brightspace.OrgUnitInfo) bool {
	return u.Type.Id == courseOfferingTypeID ||
		strings.Contains(strings.ToLower(u.Type.Code), "course")
}

// courseList returns the student's classes.
//
// If the type filter matches nothing but enrollments did come back, everything
// is returned instead. That is the deliberate degradation for courseOfferingTypeID
// being wrong on a tenant we have never run against: showing a department
// alongside two real courses is a cosmetic problem, while showing nothing makes
// every tool in this package unusable and looks identical to "you aren't
// enrolled in anything".
func (s *Server) courseList(ctx context.Context) ([]brightspace.OrgUnitInfo, error) {
	units, err := s.enrollments(ctx)
	if err != nil {
		return nil, err
	}
	courses := make([]brightspace.OrgUnitInfo, 0, len(units))
	for _, u := range units {
		if isCourse(u) {
			courses = append(courses, u)
		}
	}
	if len(courses) == 0 {
		return units, nil
	}
	return courses, nil
}

// resolveCourse turns what a student says into what the API wants.
//
// Accepts the course code case-insensitively ("mth1003"), the full course name
// ("Calculus I"), or a raw org unit id for callers that already have one.
// Matching is restricted to courses, so "MATH-DEPT" does not resolve.
func (s *Server) resolveCourse(ctx context.Context, code string) (brightspace.OrgUnitInfo, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return brightspace.OrgUnitInfo{}, fmt.Errorf("no course given: pass a course code such as MTH1003, from list_courses")
	}

	courses, err := s.courseList(ctx)
	if err != nil {
		return brightspace.OrgUnitInfo{}, err
	}

	known := make([]string, 0, len(courses))
	for _, c := range courses {
		known = append(known, c.Code)
	}

	if id, err := strconv.Atoi(code); err == nil {
		for _, c := range courses {
			if c.Id == id {
				return c, nil
			}
		}
		return brightspace.OrgUnitInfo{}, errNoCourse(code, known)
	}

	var matches []brightspace.OrgUnitInfo
	for _, c := range courses {
		if strings.EqualFold(c.Code, code) || strings.EqualFold(c.Name, code) {
			matches = append(matches, c)
		}
	}

	switch len(matches) {
	case 0:
		return brightspace.OrgUnitInfo{}, errNoCourse(code, known)
	case 1:
		return matches[0], nil
	default:
		// Two enrollments sharing a code means multiple sections or terms.
		// Guessing picks the wrong gradebook silently, so make the caller choose.
		labels := make([]string, 0, len(matches))
		for _, m := range matches {
			labels = append(labels, fmt.Sprintf("%s (id %d)", m.Name, m.Id))
		}
		return brightspace.OrgUnitInfo{}, fmt.Errorf(
			"%q matches more than one enrollment: %s — call this tool again with the numeric id",
			code, strings.Join(labels, ", "))
	}
}

type courseSummary struct {
	Code string `json:"code" jsonschema:"course code, what every other tool takes"`
	Name string `json:"name" jsonschema:"full course title"`
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
		out.Courses = append(out.Courses, courseSummary{Code: c.Code, Name: c.Name})
	}
	return nil, out, nil
}
