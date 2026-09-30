package mcptools

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
)

// maxClasslist caps how many people one call returns. A 300-seat lecture would
// otherwise spend most of the model's context on classmates nobody asked about.
const maxClasslist = 200

type person struct {
	Name string `json:"name"`

	// Role is filled in only when the institution's role ids have been mapped
	// (see WithRoleNames). RoleID is always present, and is the only honest
	// answer without that mapping: the numbers are assigned per institution and
	// carry no portable meaning, so 103 is not "Instructor" anywhere except
	// where someone has said it is.
	Role   string `json:"role,omitempty"`
	RoleID *int   `json:"role_id,omitempty" jsonschema:"institution-specific role number; people sharing one share a role, but the number itself does not identify instructors or TAs"`

	Email    string `json:"email,omitempty" jsonschema:"empty when the institution does not publish it, which is common and not an error"`
	Username string `json:"username,omitempty"`
	Online   bool   `json:"online,omitempty"`
}

type getClasslistOutput struct {
	Course string   `json:"course"`
	People []person `json:"people"`
	Note   string   `json:"note,omitempty"`
}

func (s *Server) getClasslist(ctx context.Context, _ *mcp.CallToolRequest, args courseArgs) (*mcp.CallToolResult, getClasslistOutput, error) {
	course, err := s.resolveCourse(ctx, args.Course)
	if err != nil {
		return nil, getClasslistOutput{}, err
	}

	users, err := s.client.Classlist(ctx, course.Id)
	if errors.Is(err, brightspace.ErrUnauthorized) {
		// An instructor who turns the Classlist tool off for their course is a
		// normal configuration, verified against a live tenant. Reporting it as
		// an empty roster with a note beats a tool error: the model can say
		// "that course does not publish its roster" instead of retrying a call
		// that will never succeed.
		return nil, getClasslistOutput{
			Course: course.Code,
			People: []person{},
			Note: "This course does not publish its class roster — the instructor has " +
				"the Classlist tool turned off. Say so rather than guessing who is in the course.",
		}, nil
	}
	if err != nil {
		return nil, getClasslistOutput{}, fmt.Errorf("reading %s classlist: %w", course.Code, err)
	}

	out := getClasslistOutput{Course: course.Code, People: make([]person, 0, len(users))}
	mapped := 0
	for _, u := range users {
		p := person{
			Name:     u.DisplayName,
			RoleID:   u.RoleId,
			Email:    u.Email,
			Username: u.UserName,
			Online:   u.IsOnline,
		}
		// The tenant's own spelling wins. WithRoleNames exists for tenants that
		// send only a number; a live one sends "Learner" or "Instructor"
		// outright, which is both more accurate and needs no configuration.
		switch {
		case u.ClasslistRoleDisplayName != "":
			p.Role = u.ClasslistRoleDisplayName
			mapped++
		case u.RoleId != nil:
			if name, ok := s.roles[*u.RoleId]; ok {
				p.Role = name
				mapped++
			}
		}
		out.People = append(out.People, p)
	}

	sort.SliceStable(out.People, func(i, j int) bool { return out.People[i].Name < out.People[j].Name })

	truncated := false
	if len(out.People) > maxClasslist {
		out.People, truncated = out.People[:maxClasslist], true
	}

	switch {
	case truncated:
		out.Note = fmt.Sprintf("Showing %d of %d people.", maxClasslist, len(users))
	case mapped == 0 && len(out.People) > 0:
		// Without a mapping the assistant genuinely cannot answer "who is my
		// TA". Saying so is the right outcome; guessing from a role number
		// would name the wrong person with total confidence.
		out.Note = "Role numbers are not mapped to role names for this institution, " +
			"so this tool cannot say who is an instructor or a TA. Report the names and " +
			"say the roles are not published rather than inferring them."
	}
	return nil, out, nil
}
