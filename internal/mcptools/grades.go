package mcptools

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// gradeItem is one gradebook row.
//
// Scored is load-bearing and separate from Points on purpose. An ungraded item
// arrives with a null numerator and a real denominator — "worth 100 points, not
// marked yet". Flattening that to "0 / 100" invents a failing grade the student
// does not have, which is the single worst thing this tool could do.
type gradeItem struct {
	Name string `json:"name"`
	Type string `json:"type" jsonschema:"how the item is scored: Numeric, PassFail, SelectBox, or Text"`

	// Scored is specifically about points, not about whether the item has been
	// marked. A PassFail item is graded "Pass" with no numerator at all, so
	// this reads false while grade reads Pass.
	Scored bool `json:"scored" jsonschema:"whether a numeric score is recorded. False on an ungraded item, and also on pass/fail and text items that are graded but carry no points - read grade before concluding anything is unmarked, and never treat false as a zero"`

	Grade    string `json:"grade,omitempty" jsonschema:"the grade as Brightspace displays it"`
	Points   string `json:"points,omitempty" jsonschema:"earned over possible, only present when scored"`
	OutOf    string `json:"out_of,omitempty" jsonschema:"points the item is worth, present even when ungraded"`
	Weight   string `json:"weight,omitempty" jsonschema:"how much this contributes to the final grade, earned over possible"`
	Feedback string `json:"feedback,omitempty" jsonschema:"instructor feedback written for the student"`
}

type getGradesOutput struct {
	Course string      `json:"course"`
	Items  []gradeItem `json:"items"`

	FinalGrade *gradeItem `json:"final_grade,omitempty" jsonschema:"the course's calculated final grade, absent when the course does not release one"`
	Note       string     `json:"note,omitempty"`
}

func (s *Server) getGrades(ctx context.Context, _ *mcp.CallToolRequest, args courseArgs) (*mcp.CallToolResult, getGradesOutput, error) {
	course, err := s.resolveCourse(ctx, args.Course)
	if err != nil {
		return nil, getGradesOutput{}, err
	}

	values, err := s.client.MyGradeValues(ctx, course.Id)
	if err != nil {
		return nil, getGradesOutput{}, fmt.Errorf("reading %s gradebook: %w", course.Code, err)
	}

	out := getGradesOutput{Course: course.Code, Items: make([]gradeItem, 0, len(values))}
	for _, v := range values {
		out.Items = append(out.Items, toGradeItem(v))
	}

	final, err := s.client.MyFinalGrade(ctx, course.Id)
	switch {
	case errors.Is(err, brightspace.ErrNotFound):
		// Common mid-semester and not an error. Say so explicitly, because a
		// missing final grade and a final grade of zero must not look alike.
		out.Note = "This course does not release a calculated final grade."
	case err != nil:
		return nil, getGradesOutput{}, fmt.Errorf("reading %s final grade: %w", course.Code, err)
	default:
		item := toGradeItem(*final)
		out.FinalGrade = &item
	}

	return nil, out, nil
}

// toGradeItem projects a GradeValue for student consumption.
//
// PrivateComments is dropped here and must stay dropped: it is instructor-only
// annotation that D2L returns on the same object as student-visible feedback,
// and the only thing keeping it out of an answer is this function not copying
// it. TestGradesNeverLeakPrivateComments guards that.
func toGradeItem(v brightspace.GradeValue) gradeItem {
	item := gradeItem{
		Name:     v.GradeObjectName,
		Type:     v.GradeObjectTypeName,
		Scored:   v.Scored(),
		Grade:    v.DisplayedGrade,
		Feedback: richText(v.Comments),
	}

	if v.Scored() {
		item.Points = fmt.Sprintf("%s / %s", num(*v.PointsNumerator), num(*v.PointsDenominator))
	} else if v.PointsDenominator != nil {
		item.OutOf = num(*v.PointsDenominator)
	}

	if v.WeightedNumerator != nil && v.WeightedDenominator != nil {
		item.Weight = fmt.Sprintf("%s / %s", num(*v.WeightedNumerator), num(*v.WeightedDenominator))
	}

	return item
}

// num renders a score without the trailing zeros a float carries. "92" reads
// as a grade; "92.000000" reads as a machine leaking through.
func num(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
