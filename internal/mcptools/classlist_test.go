package mcptools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
)

// fakeClasslist answers MyEnrollments from a fixture and Classlist from
// whatever the test wants, including an error.
type fakeClasslist struct {
	brightspace.Client
	units []brightspace.MyOrgUnitInfo
	users []brightspace.ClasslistUser
	err   error
}

func (f fakeClasslist) MyEnrollments(context.Context) ([]brightspace.MyOrgUnitInfo, error) {
	return f.units, nil
}

func (f fakeClasslist) Classlist(context.Context, int) ([]brightspace.ClasslistUser, error) {
	return f.users, f.err
}

func classlistServer(t *testing.T, users []brightspace.ClasslistUser, err error, opts ...Option) *Server {
	t.Helper()
	at := date(t, asOf)
	client := fakeClasslist{units: realWorldEnrollments(t), users: users, err: err}
	return New(client, append([]Option{WithClock(func() time.Time { return *at })}, opts...)...)
}

func callClasslist(t *testing.T, s *Server, course string) getClasslistOutput {
	t.Helper()
	_, out, err := s.getClasslist(context.Background(), nil, courseArgs{Course: course})
	if err != nil {
		t.Fatalf("getClasslist(%q): %v", course, err)
	}
	return out
}

// An instructor can switch the Classlist tool off for their course, and a live
// tenant answers 403. That is a configuration, not a fault: returning a tool
// error would make the model retry a call that can never succeed, so it has to
// come back as an empty roster carrying an explanation.
func TestGetClasslistDegradesWhenTheRosterIsWithheld(t *testing.T) {
	s := classlistServer(t, nil, brightspace.ErrUnauthorized)

	out := callClasslist(t, s, "CIS 3500")
	if len(out.People) != 0 {
		t.Errorf("got %d people from a withheld roster, want none", len(out.People))
	}
	if out.Note == "" {
		t.Fatal("withheld roster returned no note; the model has no way to explain the empty list")
	}
	if !strings.Contains(strings.ToLower(out.Note), "does not publish") {
		t.Errorf("note %q does not say the roster is unpublished", out.Note)
	}
}

// The tenant spells the role out on every person. Preferring it over the role
// id is what makes "who is my TA?" answerable without per-institution mapping,
// since the numbers mean nothing portable.
func TestGetClasslistPrefersTheTenantsRoleName(t *testing.T) {
	id := 110
	users := []brightspace.ClasslistUser{
		{DisplayName: "Ada Lovelace", ClasslistRoleDisplayName: "Instructor", RoleId: &id},
		{DisplayName: "Grace Hopper", ClasslistRoleDisplayName: "Learner", RoleId: &id},
	}
	// A mapping that would produce the wrong answer if it won.
	s := classlistServer(t, users, nil, WithRoleNames(map[int]string{110: "Learner"}))

	out := callClasslist(t, s, "CIS 3500")
	if len(out.People) != 2 {
		t.Fatalf("got %d people, want 2", len(out.People))
	}
	byName := map[string]string{}
	for _, p := range out.People {
		byName[p.Name] = p.Role
	}
	if byName["Ada Lovelace"] != "Instructor" {
		t.Errorf("Ada resolved to role %q, want Instructor from the tenant rather than the role-id mapping", byName["Ada Lovelace"])
	}
	if out.Note != "" {
		t.Errorf("roles were resolved, so no caveat is warranted; got note %q", out.Note)
	}
}

// Without either a role name or a mapping, saying so is the only honest
// answer — naming a random classmate as the instructor is worse than admitting
// the roles are unknown.
func TestGetClasslistAdmitsUnknownRoles(t *testing.T) {
	id := 110
	users := []brightspace.ClasslistUser{{DisplayName: "Grace Hopper", RoleId: &id}}
	s := classlistServer(t, users, nil)

	out := callClasslist(t, s, "CIS 3500")
	if len(out.People) != 1 {
		t.Fatalf("got %d people, want 1", len(out.People))
	}
	if out.People[0].Role != "" {
		t.Errorf("role = %q, want empty: nothing mapped id %d", out.People[0].Role, id)
	}
	if !strings.Contains(out.Note, "not mapped") {
		t.Errorf("note %q does not warn that roles are unresolved", out.Note)
	}
}
