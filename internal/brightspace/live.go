package brightspace

import (
	"context"
	"io"
	"time"
)

// LiveClient talks to a real Brightspace tenant over OAuth2 (authorization
// code grant — submissions are attributed to the calling token, so it must be
// the student's own, never a service account).
//
// Every method returns ErrNotImplemented until credentials exist. Two things
// must be resolved on first live connection:
//
//   - The LP and LE API versions are written "(version)" in D2L's docs and
//     version independently. Pin both before implementing anything here.
//   - No response shape in types.go has been validated against a real tenant.
//     Capture real responses and diff them against testdata/ rather than
//     trusting that the fixtures are right.
//
// Build step 1 (stubs). The implementation is gated on institutional access.
type LiveClient struct {
	// Host is the tenant, e.g. "brightspace.cuny.edu".
	Host string
}

func (c *LiveClient) MyEnrollments(ctx context.Context) ([]OrgUnitInfo, error) {
	return nil, ErrNotImplemented
}

func (c *LiveClient) ContentRoot(ctx context.Context, orgUnitID int) ([]Module, error) {
	return nil, ErrNotImplemented
}

func (c *LiveClient) ModuleStructure(ctx context.Context, orgUnitID, moduleID int) ([]ContentObject, error) {
	return nil, ErrNotImplemented
}

func (c *LiveClient) TopicFile(ctx context.Context, orgUnitID, topicID int) (io.ReadCloser, string, error) {
	return nil, "", ErrNotImplemented
}

func (c *LiveClient) MyGradeValues(ctx context.Context, orgUnitID int) ([]GradeValue, error) {
	return nil, ErrNotImplemented
}

func (c *LiveClient) MyFinalGrade(ctx context.Context, orgUnitID int) (*GradeValue, error) {
	return nil, ErrNotImplemented
}

func (c *LiveClient) DropboxFolders(ctx context.Context, orgUnitID int) ([]DropboxFolder, error) {
	return nil, ErrNotImplemented
}

func (c *LiveClient) MyEvents(ctx context.Context, orgUnitIDs []int, start, end time.Time) ([]CalendarEvent, error) {
	return nil, ErrNotImplemented
}
