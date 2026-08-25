// Package brightspace mirrors the D2L Valence REST API.
//
// The Client interface maps 1:1 onto documented Valence routes so that live
// responses unmarshal into these types with no translation layer. It has two
// implementations: MockClient (fixtures) and LiveClient (HTTP). Everything
// above this package is identical either way.
//
// Both are complete for the methods declared here. What LiveClient still lacks
// is a tenant to talk to: the routes and schemas come from D2L's docs and have
// never been run against a real Brightspace, and authentication is supplied by
// the caller's http.Client rather than built here.
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
	// and semesters, not only courses — filter on OrgUnit.Type.
	//
	// Returns the full entry rather than the bare org unit because the access
	// window is the only way to tell a class that is running now from one that
	// ended two years ago: a real tenant reports IsActive and CanAccess true
	// for every past enrollment, so the dates are the only usable signal.
	MyEnrollments(ctx context.Context) ([]MyOrgUnitInfo, error)

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

	// NewsItems returns course announcements whose StartDate is at or after
	// since. A zero since returns everything.
	//
	// No documented query parameter filters this route by date, so both
	// implementations filter locally. An item with no StartDate is included
	// rather than dropped: over-reporting an announcement is recoverable,
	// hiding one the student needed is not.
	//
	// Includes unpublished drafts. See the note on ContentRoot.
	NewsItems(ctx context.Context, orgUnitID int, since time.Time) ([]NewsItem, error)

	// DiscussionForums returns the course's discussion containers.
	DiscussionForums(ctx context.Context, orgUnitID int) ([]Forum, error)

	// DiscussionTopics returns one forum's threads. Absent from the v1 plan's
	// interface sketch, which listed forums and posts but nothing to get
	// between them, leaving DiscussionPosts unreachable.
	DiscussionTopics(ctx context.Context, orgUnitID, forumID int) ([]DiscussionTopic, error)

	// DiscussionPosts returns one topic's messages, oldest first. Replies are
	// linked to their parent by Post.ParentPostId rather than nested.
	//
	// Includes deleted posts, which carry IsDeleted with their Message intact.
	DiscussionPosts(ctx context.Context, orgUnitID, forumID, topicID int) ([]Post, error)

	// Quizzes returns the course's quizzes and exams, sorted by SortOrder.
	Quizzes(ctx context.Context, orgUnitID int) ([]Quiz, error)

	// Classlist returns everyone enrolled in the course — the only route
	// behind "who is my TA, and when are office hours?".
	//
	// Contact fields are org-configurable and commonly withheld; see
	// ClasslistUser. Callers must handle a name with no email.
	Classlist(ctx context.Context, orgUnitID int) ([]ClasslistUser, error)

	// SubmitToDropbox is v2 — declared now so submission lands as a fill-in
	// rather than a refactor. Both implementations return ErrNotImplemented.
	//
	// Two constraints that are not obvious from the signature. Submissions are
	// attributed to whoever owns the token, so this must run on the student's
	// own OAuth grant and never a service account. And a wrong-folder
	// submission is hard to undo inside someone's gradebook, so the caller must
	// gate this behind an explicit confirmation showing the resolved folder
	// name and due date.
	SubmitToDropbox(ctx context.Context, orgUnitID, folderID int, comment string, files []Upload) (*Submission, error)
}
