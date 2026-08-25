package mcptools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
)

// fakeEnrollments implements only the one method the course resolver calls.
// Embedding the interface leaves every other method nil, so a test that
// wanders into unrelated code panics loudly instead of quietly passing.
type fakeEnrollments struct {
	brightspace.Client
	units []brightspace.MyOrgUnitInfo
}

func (f fakeEnrollments) MyEnrollments(context.Context) ([]brightspace.MyOrgUnitInfo, error) {
	return f.units, nil
}

func date(t *testing.T, s string) *time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad date %q in test fixture: %v", s, err)
	}
	return &parsed
}

// course builds an enrollment shaped the way a real tenant returns one, rather
// than the way the fixtures used to: an institution-prefixed code with term and
// section numbers wrapped around the catalog number, and a title following no
// particular convention.
func course(id int, code, name string, start, end *time.Time) brightspace.MyOrgUnitInfo {
	var u brightspace.MyOrgUnitInfo
	u.OrgUnit = brightspace.OrgUnitInfo{
		Id:   id,
		Code: code,
		Name: name,
		Type: brightspace.OrgUnitTypeInfo{
			Id:   brightspace.CourseOfferingTypeID,
			Code: "Course Offering",
			Name: "Course Offering",
		},
	}
	u.Access.IsActive = true
	u.Access.CanAccess = true
	u.Access.StartDate = start
	u.Access.EndDate = end
	return u
}

const asOf = "2026-08-25T12:00:00Z"

// realWorldEnrollments mirrors the shape of a live tenant's response: several
// finished terms, one running course, a self-paced training with no dates, two
// sections of one class, and a department the student is also enrolled in.
//
// Codes and titles are synthetic, but the three different title conventions and
// the code's prefix/term/section padding are copied from a real response —
// those are exactly what the old exact-match resolver could not handle.
func realWorldEnrollments(t *testing.T) []brightspace.MyOrgUnitInfo {
	t.Helper()
	var (
		fallStart   = date(t, "2026-08-14T04:01:00Z")
		fallEnd     = date(t, "2027-01-21T04:59:00Z")
		springStart = date(t, "2026-01-12T05:01:00Z")
		springEnd   = date(t, "2026-06-26T03:59:00Z")
		oldStart    = date(t, "2024-08-14T04:01:00Z")
		oldEnd      = date(t, "2025-01-21T04:59:00Z")
	)

	dept := brightspace.MyOrgUnitInfo{}
	dept.OrgUnit = brightspace.OrgUnitInfo{
		Id: 101, Code: "MATH-DEPT", Name: "Mathematics",
		Type: brightspace.OrgUnitTypeInfo{Id: 5, Code: "Department", Name: "Department"},
	}
	dept.Access.IsActive = true
	dept.Access.CanAccess = true

	return []brightspace.MyOrgUnitInfo{
		course(1291894, "XYZ01_CIS_3500_1269_1_37297",
			"2026 FA [1] CIS 3500 DMWB [37297] Computer Networking [Lecture] [Example College]",
			fallStart, fallEnd),
		course(1105147, "XYZ01_MTH_4360_1262_1_26545",
			"2026 SP [1] MTH 4360 KTRA [26545] Complexity and Computational M [Lecture] [Example College]",
			springStart, springEnd),
		course(1104163, "XYZ01_ENG_2800_1262_1_27824",
			"Storytelling: Great Works of Literature I ENG 2800 TTRA [27824]",
			springStart, springEnd),
		course(1104259, "XYZ01_ENV_1003_1262_1_27208",
			"2026 Spring - ENV 1003 Ecology [STRL 27208]",
			springStart, springEnd),
		course(414458, "XYZ01_CIS_3500_1249_1_11111",
			"2024 FA [1] CIS 3500 SMWA [11111] Computer Networking [Lecture] [Example College]",
			oldStart, oldEnd),
		course(1099079, "XYZ01_CYB_STU_2026_2027",
			"Example College Student Cyber Security Awareness Training", nil, nil),
		course(900001, "XYZ01_ART_1000_1269_1_44001",
			"2026 FA [1] ART 1000 AMWA [44001] Drawing [Lecture] [Example College]",
			fallStart, fallEnd),
		course(900002, "XYZ01_ART_1000_1269_2_44002",
			"2026 FA [2] ART 1000 AMWA [44002] Drawing [Lab] [Example College]",
			fallStart, fallEnd),
		dept,
	}
}

func testServer(t *testing.T, units []brightspace.MyOrgUnitInfo, now string) *Server {
	t.Helper()
	at := date(t, now)
	return New(fakeEnrollments{units: units}, WithClock(func() time.Time { return *at }))
}

// A student says "MTH 4360". The tenant says "XYZ01_MTH_4360_1262_1_26545".
// Exact matching on either code or title resolves neither.
func TestResolveCourseAcceptsRealWorldSpellings(t *testing.T) {
	s := testServer(t, realWorldEnrollments(t), asOf)

	tests := []struct {
		query string
		want  int
	}{
		{"CIS 3500", 1291894},
		{"cis3500", 1291894},
		{"CIS-3500", 1291894},
		{"XYZ01_CIS_3500_1269_1_37297", 1291894},

		// Title matching, across all three conventions the fixture carries.
		{"Computer Networking", 1291894},
		{"Ecology", 1104259},
		{"Storytelling", 1104163},

		// A finished course still resolves: asking what you got in a class you
		// already took is a fair question.
		{"MTH 4360", 1105147},
		{"ENG 2800", 1104163},

		// A raw org unit id is what the ambiguity error tells callers to retry
		// with, so it has to work for past courses too.
		{"1105147", 1105147},
		{"414458", 414458},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got, err := s.resolveCourse(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("resolveCourse(%q): %v", tt.query, err)
			}
			if got.Id != tt.want {
				t.Errorf("resolveCourse(%q) = %d (%s), want %d", tt.query, got.Id, got.Name, tt.want)
			}
		})
	}
}

// The same catalog number in two terms is the common case, not an edge case.
// Silently picking the older one would answer grade questions from the wrong
// gradebook, so the course in session has to win.
func TestResolveCoursePrefersTheCourseInSession(t *testing.T) {
	s := testServer(t, realWorldEnrollments(t), asOf)

	got, err := s.resolveCourse(context.Background(), "CIS 3500")
	if err != nil {
		t.Fatalf("resolveCourse: %v", err)
	}
	if got.Id != 1291894 {
		t.Errorf("resolved to %d (%s), want the in-session section 1291894", got.Id, got.Name)
	}
}

// Two sections of one class in the same term is genuinely ambiguous. Guessing
// is worse than asking, so this must be an error the model can recover from —
// naming both candidates and their ids.
func TestResolveCourseRefusesToGuessBetweenSections(t *testing.T) {
	s := testServer(t, realWorldEnrollments(t), asOf)

	_, err := s.resolveCourse(context.Background(), "ART 1000")
	if err == nil {
		t.Fatal("resolveCourse resolved an ambiguous code instead of erroring")
	}
	for _, want := range []string{"900001", "900002"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q omits candidate id %s", err, want)
		}
	}
}

func TestResolveCourseRejectsNonCourses(t *testing.T) {
	s := testServer(t, realWorldEnrollments(t), asOf)

	for _, query := range []string{"MATH-DEPT", "Mathematics", "101", "PHI 9999", ""} {
		if got, err := s.resolveCourse(context.Background(), query); err == nil {
			t.Errorf("resolveCourse(%q) = %d (%s), want an error", query, got.Id, got.Name)
		}
	}
}

// "My courses" means the ones running now. A real tenant reports IsActive and
// CanAccess true for every enrollment a student has ever had, so without date
// filtering this list is years of finished classes and every cross-course tool
// fans out over all of them.
func TestCourseListFiltersToTheCurrentTerm(t *testing.T) {
	s := testServer(t, realWorldEnrollments(t), asOf)

	got, err := s.courseList(context.Background())
	if err != nil {
		t.Fatalf("courseList: %v", err)
	}

	want := map[int]bool{
		1291894: true, // fall 2026, running
		900001:  true, // fall 2026, running
		900002:  true, // fall 2026, running
		1099079: true, // undated self-paced training, always open
	}
	if len(got) != len(want) {
		t.Fatalf("got %d current courses, want %d: %+v", len(got), len(want), got)
	}
	for _, c := range got {
		if !want[c.Id] {
			t.Errorf("course %d (%s) is not running as of %s", c.Id, c.Name, asOf)
		}
	}
}

// Between semesters nothing is in session. Returning an empty list there would
// leave every other tool with nothing to resolve against, which reads to the
// model as "you aren't enrolled in anything" rather than "you're on break".
func TestCourseListFallsBackWhenNothingIsInSession(t *testing.T) {
	units := []brightspace.MyOrgUnitInfo{
		course(1105147, "XYZ01_MTH_4360_1262_1_26545", "Complexity and Computational M",
			date(t, "2026-01-12T05:01:00Z"), date(t, "2026-06-26T03:59:00Z")),
		course(1104259, "XYZ01_ENV_1003_1262_1_27208", "2026 Spring - ENV 1003 Ecology [STRL 27208]",
			date(t, "2026-01-12T05:01:00Z"), date(t, "2026-06-26T03:59:00Z")),
	}
	s := testServer(t, units, "2026-07-15T12:00:00Z")

	got, err := s.courseList(context.Background())
	if err != nil {
		t.Fatalf("courseList: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d courses between terms, want both as a fallback: %+v", len(got), got)
	}
}

func TestInSession(t *testing.T) {
	now := *date(t, asOf)

	running := course(1, "C", "running", date(t, "2026-08-14T00:00:00Z"), date(t, "2027-01-21T00:00:00Z"))
	ended := course(2, "C", "ended", date(t, "2026-01-12T00:00:00Z"), date(t, "2026-06-26T00:00:00Z"))
	notStarted := course(3, "C", "not started", date(t, "2026-09-01T00:00:00Z"), date(t, "2027-01-21T00:00:00Z"))
	undated := course(4, "C", "self-paced", nil, nil)

	inactive := course(5, "C", "inactive", nil, nil)
	inactive.Access.IsActive = false

	noAccess := course(6, "C", "no access", nil, nil)
	noAccess.Access.CanAccess = false

	tests := []struct {
		unit brightspace.MyOrgUnitInfo
		want bool
	}{
		{running, true},
		{ended, false},
		{notStarted, false},
		{undated, true},
		{inactive, false},
		{noAccess, false},
	}

	for _, tt := range tests {
		t.Run(tt.unit.OrgUnit.Name, func(t *testing.T) {
			if got := inSession(tt.unit, now); got != tt.want {
				t.Errorf("inSession(%s) = %v, want %v", tt.unit.OrgUnit.Name, got, tt.want)
			}
		})
	}
}

func TestNormalizeCourseKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"MTH 4360", "MTH4360"},
		{"mth-4360", "MTH4360"},
		{"  mth4360  ", "MTH4360"},
		{"XYZ01_MTH_4360_1262_1_26545", "XYZ01MTH43601262126545"},
		{"2026 Spring - ENV 1003 Ecology [STRL 27208]", "2026SPRINGENV1003ECOLOGYSTRL27208"},
		{"", ""},
		{"---", ""},
	}
	for _, tt := range tests {
		if got := normalizeCourseKey(tt.in); got != tt.want {
			t.Errorf("normalizeCourseKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
