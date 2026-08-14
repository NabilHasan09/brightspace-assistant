// Package brightspace mirrors the D2L Valence REST API.
//
// The Client interface maps 1:1 onto documented Valence routes so that live
// responses unmarshal into these types with no translation layer. It has two
// implementations: MockClient (fixtures, works today) and LiveClient (OAuth2,
// blocked on institutional credentials). Everything above this package is
// identical either way.
//
// The interface grows one build step at a time rather than landing all at
// once, so that every method here has a working mock and a test behind it.
// Currently: enrollments and the content tree, which is what the materials
// specialist needs. Grades, deadlines, announcements, and discussions follow.
//
// Build step 1.
package brightspace

import (
	"context"
	"io"
	"time"
)

type Client interface {
	// MyEnrollments lists the caller's org units. This includes departments
	// and semesters, not only courses — filter on OrgUnitInfo.Type.
	MyEnrollments(ctx context.Context) ([]OrgUnitInfo, error)

	// ContentRoot returns the course's top-level modules, each with its
	// immediate children inline in Structure.
	//
	// Results include objects with IsHidden or IsLocked set — this interface
	// mirrors the API rather than filtering it. Callers must decide: ingest
	// MUST skip hidden objects, because indexing one would let the assistant
	// surface an unreleased exam or draft material the instructor
	// deliberately withheld. Tracked for build step 5.
	ContentRoot(ctx context.Context, orgUnitID int) ([]Module, error)

	// ModuleStructure returns one module's immediate children.
	ModuleStructure(ctx context.Context, orgUnitID, moduleID int) ([]ContentObject, error)

	// TopicFile returns the bytes of a file topic and its MIME type. Returns
	// ErrNotFileTopic for link and publisher topics, which have no content to
	// download. The caller owns closing the reader.
	TopicFile(ctx context.Context, orgUnitID, topicID int) (io.ReadCloser, string, error)

	// MyGradeValues returns the caller's gradebook for one course, including
	// instructor Comments. Rows may exist with no score recorded — check
	// GradeValue.Scored rather than reading points directly.
	MyGradeValues(ctx context.Context, orgUnitID int) ([]GradeValue, error)

	// MyFinalGrade returns the caller's calculated final grade, or
	// ErrNotFound when the course does not release one.
	MyFinalGrade(ctx context.Context, orgUnitID int) (*GradeValue, error)

	// DropboxFolders returns the course's assignment submission folders.
	// Includes hidden folders; see the note on ContentRoot.
	DropboxFolders(ctx context.Context, orgUnitID int) ([]DropboxFolder, error)

	// MyEvents returns dated items across several courses at once. The live
	// endpoint takes a CSV of org unit ids with a date range, so cross-course
	// planning costs one call rather than one per enrollment.
	//
	// An empty orgUnitIDs means every course the caller is enrolled in.
	MyEvents(ctx context.Context, orgUnitIDs []int, start, end time.Time) ([]CalendarEvent, error)
}
