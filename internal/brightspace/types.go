package brightspace

import (
	"io"
	"time"
)

// Field names and JSON tags mirror D2L's Valence schemas so that live
// responses unmarshal with no translation layer.
//
// These structs deliberately cover a subset of what D2L returns. encoding/json
// ignores unknown fields, so a partial struct is forward-compatible and adding
// a field later breaks nothing. Model what the assistant reads, not the whole
// wire format.
//
// Build step 1.

// ObjectType discriminates entries in a content listing. D2L returns modules
// and topics in the same array.
type ObjectType int

const (
	ObjectModule ObjectType = 0
	ObjectTopic  ObjectType = 1
)

// TopicType says whether a topic has bytes behind it. Only TopicFile returns
// content from TopicFile(); see ErrNotFileTopic.
type TopicType int

const (
	TopicFile TopicType = 1
	TopicLink TopicType = 2
)

// RichText is D2L's paired plain/HTML block. Prefer Text — Html is authored
// content and arrives with whatever markup the instructor pasted in.
type RichText struct {
	Text string `json:"Text"`
	Html string `json:"Html"`
}

// OrgUnitTypeInfo distinguishes a course offering from a department, semester,
// or the org root. Enrollments include all of them, so Type is how you filter
// down to actual courses.
type OrgUnitTypeInfo struct {
	Id   int    `json:"Id"`
	Code string `json:"Code"`
	Name string `json:"Name"`
}

// CourseOfferingTypeID is the org unit type for a course offering, as opposed
// to a department, semester, or the org root.
//
// Org unit types are configurable per institution, so this is a default rather
// than a constant of the protocol. It matches what the tenant's own UI sends.
// Note that it does not exclude non-academic org units: onboarding tutorials
// and compliance trainings are also modelled as course offerings.
const CourseOfferingTypeID = 3

// OrgUnitInfo identifies a course. Id is what every other endpoint wants.
//
// Code and Name are both messier than they look on a real tenant. Code carries
// institution, term, and section around the catalog number
// ("BAR01_MTH_4360_1262_1_26545"), and Name has no fixed convention at all —
// three courses in one semester can each order term, title, section, and
// campus differently. Neither is safe to match on exactly; see the tool layer's
// course resolver.
type OrgUnitInfo struct {
	Id   int             `json:"Id"`
	Type OrgUnitTypeInfo `json:"Type"`
	Name string          `json:"Name"`
	Code string          `json:"Code"`
}

// ContentObject is one entry in a course's content tree. D2L returns modules
// and topics in the same array discriminated by Type, so this carries the
// union of both: module-only fields are zero on topics and vice versa.
//
// One struct rather than two because modules nest. A separate Module type
// embedding this one would need its own Structure field, which shadows rather
// than extends, so grandchildren would silently vanish on unmarshal.
type ContentObject struct {
	Id               int        `json:"Id"`
	Title            string     `json:"Title"`
	ShortTitle       string     `json:"ShortTitle"`
	Type             ObjectType `json:"Type"`
	Description      RichText   `json:"Description"`
	ParentModuleId   *int       `json:"ParentModuleId"`
	SortOrder        int        `json:"SortOrder"`
	LastModifiedDate time.Time  `json:"LastModifiedDate"`
	IsHidden         bool       `json:"IsHidden"`
	IsLocked         bool       `json:"IsLocked"`

	// Module only. Structure holds immediate children, which may themselves
	// be modules.
	Structure       []ContentObject `json:"Structure,omitempty"`
	ModuleStartDate *time.Time      `json:"ModuleStartDate,omitempty"`
	ModuleEndDate   *time.Time      `json:"ModuleEndDate,omitempty"`
	ModuleDueDate   *time.Time      `json:"ModuleDueDate,omitempty"`

	// Topic only.
	TopicType TopicType  `json:"TopicType,omitempty"`
	Url       string     `json:"Url,omitempty"`
	StartDate *time.Time `json:"StartDate,omitempty"`
	EndDate   *time.Time `json:"EndDate,omitempty"`
	DueDate   *time.Time `json:"DueDate,omitempty"`
}

// Module is a ContentObject with Type == ObjectModule. The alias keeps
// interface signatures readable about what they return without introducing a
// second struct that would have to stay in sync with this one.
type Module = ContentObject

// IsModule reports whether o is a folder rather than a leaf.
func (o ContentObject) IsModule() bool { return o.Type == ObjectModule }

// PagingInfo appears on D2L's bookmark-paged collections: enrollments and the
// classlist. LiveClient follows the bookmark until the server stops moving it;
// MockClient returns everything in one page, so a truncation bug would only
// ever show against a real tenant with a large course.
//
// This is not D2L's only paging shape — quizzes use QuizListPage instead.
type PagingInfo struct {
	Bookmark     string `json:"Bookmark"`
	HasMoreItems bool   `json:"HasMoreItems"`
}

// MyOrgUnitInfo is one entry from the enrollments endpoint, which wraps the
// org unit alongside access dates rather than returning it bare.
//
// The JSON key is "OrgUnit", not "OrgUnitInfo" — verified against a live
// tenant. Getting this wrong fails silently, because a missing key is not an
// unmarshal error: the caller gets the right number of enrollments back, every
// one of them zeroed, with no id, name, or code.
type MyOrgUnitInfo struct {
	OrgUnit OrgUnitInfo `json:"OrgUnit"`
	Access  struct {
		IsActive  bool       `json:"IsActive"`
		StartDate *time.Time `json:"StartDate"`
		EndDate   *time.Time `json:"EndDate"`
		CanAccess bool       `json:"CanAccess"`

		// ClasslistRoleName is the caller's own role in this course
		// ("Learner"). Unlike the numeric role ids on the classlist, it
		// arrives already named.
		ClasslistRoleName string `json:"ClasslistRoleName"`

		// LastAccessed is nil for a course the student has never opened.
		LastAccessed *time.Time `json:"LastAccessed"`
	} `json:"Access"`
}

// MyEnrollmentsResponse is the paged envelope around MyOrgUnitInfo.
type MyEnrollmentsResponse struct {
	PagingInfo PagingInfo      `json:"PagingInfo"`
	Items      []MyOrgUnitInfo `json:"Items"`
}

// GradeObjectType distinguishes how a grade is scored. It decides which
// fields on GradeValue carry meaning: only Numeric populates points, so a
// caller that assumes points exist will read zero for a pass/fail item and
// report it as a zero score.
type GradeObjectType int

const (
	GradeNumeric   GradeObjectType = 1
	GradePassFail  GradeObjectType = 2
	GradeSelectBox GradeObjectType = 3
	GradeText      GradeObjectType = 4
)

// GradeValue is one row of the caller's gradebook.
//
// D2L returns a base object for non-numeric items and a numeric subtype that
// adds points and weights. Go unmarshals both into this one struct; the
// numeric fields are pointers so "no score recorded" stays distinguishable
// from "scored zero". That distinction is the whole ballgame for the grades
// specialist — conflating them invents a failing grade that does not exist.
type GradeValue struct {
	DisplayedGrade        string          `json:"DisplayedGrade"`
	GradeObjectIdentifier string          `json:"GradeObjectIdentifier"`
	GradeObjectName       string          `json:"GradeObjectName"`
	GradeObjectType       GradeObjectType `json:"GradeObjectType"`
	GradeObjectTypeName   string          `json:"GradeObjectTypeName"`

	// Comments is instructor feedback visible to the student.
	// PrivateComments is not — it is instructor-only and must never reach a
	// student-facing answer.
	Comments        RichText `json:"Comments"`
	PrivateComments RichText `json:"PrivateComments"`

	// Numeric grades only.
	PointsNumerator     *float64 `json:"PointsNumerator"`
	PointsDenominator   *float64 `json:"PointsDenominator"`
	WeightedNumerator   *float64 `json:"WeightedNumerator"`
	WeightedDenominator *float64 `json:"WeightedDenominator"`
}

// Scored reports whether a numeric score was actually recorded.
func (g GradeValue) Scored() bool {
	return g.PointsNumerator != nil && g.PointsDenominator != nil
}

// Availability is D2L's start/end window, attached to dropbox folders and
// other released objects.
type Availability struct {
	StartDate *time.Time `json:"StartDate"`
	EndDate   *time.Time `json:"EndDate"`
}

// DropboxFolder is an assignment submission folder.
type DropboxFolder struct {
	Id                 int          `json:"Id"`
	CategoryId         *int         `json:"CategoryId"`
	Name               string       `json:"Name"`
	CustomInstructions RichText     `json:"CustomInstructions"`
	DueDate            *time.Time   `json:"DueDate"`
	Availability       Availability `json:"Availability"`
	IsHidden           bool         `json:"IsHidden"`
	GroupTypeId        *int         `json:"GroupTypeId"`

	// TotalFiles counts the caller's own submitted files.
	TotalFiles int `json:"TotalFiles"`
}

// CalendarEvent is one dated item. The calendar endpoint accepts a CSV of org
// unit ids alongside a date range, so "everything due next week across all my
// courses" is a single call rather than a fan-out across enrollments.
// The JSON keys are "IsAllDayEvent" and "LocationName", verified against a
// live tenant. The obvious spellings — "IsAllDay" and "Location" — are wrong,
// and wrong in the quiet way: a missing key is not an unmarshal error, so every
// event decodes as a timed event with no location and nothing reports a
// problem.
type CalendarEvent struct {
	CalendarEventId int       `json:"CalendarEventId"`
	OrgUnitId       int       `json:"OrgUnitId"`
	Title           string    `json:"Title"`
	Description     string    `json:"Description"`
	StartDateTime   time.Time `json:"StartDateTime"`
	EndDateTime     time.Time `json:"EndDateTime"`
	IsAllDay        bool      `json:"IsAllDayEvent"`
	Location        string    `json:"LocationName"`

	// OrgUnitCode and OrgUnitName come back on every event, which means a
	// calendar answer can name its course without a second lookup.
	OrgUnitCode string `json:"OrgUnitCode"`
	OrgUnitName string `json:"OrgUnitName"`

	// AssociatedEntity links the event to the quiz or assignment it was
	// generated from, so a due date and the thing that is due can be matched
	// up rather than reported twice.
	AssociatedEntity *AssociatedEntity `json:"AssociatedEntity"`
}

// AssociatedEntity identifies what a calendar event was generated from, e.g.
// AssociatedEntityType "D2L.LE.Quizzing.Quiz" with the quiz's id.
type AssociatedEntity struct {
	AssociatedEntityId   int    `json:"AssociatedEntityId"`
	AssociatedEntityType string `json:"AssociatedEntityType"`
	Link                 string `json:"Link"`
}

// NewsItem is one course announcement.
//
// IsPublished is the field that matters: an unpublished item is a draft the
// instructor has not released, and surfacing one is the same class of mistake
// as indexing a hidden content topic. Returned unfiltered here, like every
// other visibility flag in this package — callers filter.
type NewsItem struct {
	Id    int      `json:"Id"`
	Title string   `json:"Title"`
	Body  RichText `json:"Body"`

	// StartDate is when the announcement becomes visible; EndDate is when it
	// stops. Both are nullable, and an expired item still comes back.
	StartDate *time.Time `json:"StartDate"`
	EndDate   *time.Time `json:"EndDate"`

	// IsGlobal marks an org-wide notice rather than a course one.
	IsGlobal    bool `json:"IsGlobal"`
	IsPublished bool `json:"IsPublished"`
}

// sortKey orders announcements newest first, treating an undated item as
// oldest so it cannot displace a real one at the top of "what did I miss".
func (n NewsItem) sortKey() time.Time {
	if n.StartDate == nil {
		return time.Time{}
	}
	return *n.StartDate
}

// Forum is a discussion container. Topics live inside it, posts inside those.
type Forum struct {
	ForumId     int        `json:"ForumId"`
	Name        string     `json:"Name"`
	Description RichText   `json:"Description"`
	StartDate   *time.Time `json:"StartDate"`
	EndDate     *time.Time `json:"EndDate"`
	IsHidden    bool       `json:"IsHidden"`
	IsLocked    bool       `json:"IsLocked"`
}

// DiscussionTopic is a thread container inside a Forum. Named with the
// Discussion prefix because ContentObject already owns "topic" in this package
// and the two are unrelated — a collision that is otherwise very easy to make.
type DiscussionTopic struct {
	TopicId     int        `json:"TopicId"`
	ForumId     int        `json:"ForumId"`
	Name        string     `json:"Name"`
	Description RichText   `json:"Description"`
	StartDate   *time.Time `json:"StartDate"`
	EndDate     *time.Time `json:"EndDate"`
	IsHidden    bool       `json:"IsHidden"`
	IsLocked    bool       `json:"IsLocked"`
}

// Post is one message in a discussion topic.
//
// ParentPostId is nil for a thread starter and set for a reply, which is the
// only thing that makes "did anyone reply to my post?" answerable — the flat
// array carries the tree structure in this one field.
//
// IsRead is per-caller. Whether D2L returns it inline on this route or only
// through a separate read-status call is UNVERIFIED; if it turns out to be the
// latter, this field silently reads false for everything and the unread-posts
// question quietly stops working. Check it on first live connection.
type Post struct {
	PostId         int        `json:"PostId"`
	TopicId        int        `json:"TopicId"`
	ForumId        int        `json:"ForumId"`
	ParentPostId   *int       `json:"ParentPostId"`
	Subject        string     `json:"Subject"`
	Message        RichText   `json:"Message"`
	PostingUserId  int        `json:"PostingUserId"`
	DisplayName    string     `json:"DisplayName"`
	DatePosted     time.Time  `json:"DatePosted"`
	LastEditedDate *time.Time `json:"LastEditedDate"`
	IsAnonymous    bool       `json:"IsAnonymous"`
	IsDeleted      bool       `json:"IsDeleted"`
	IsRead         bool       `json:"IsRead"`
}

// Quiz is one quiz or exam. Deadlines reads DueDate; StartDate and EndDate are
// the availability window, which is not the same thing — a quiz can be due
// before it closes.
type Quiz struct {
	QuizId    int        `json:"QuizId"`
	Name      string     `json:"Name"`
	IsActive  bool       `json:"IsActive"`
	SortOrder int        `json:"SortOrder"`
	StartDate *time.Time `json:"StartDate"`
	EndDate   *time.Time `json:"EndDate"`
	DueDate   *time.Time `json:"DueDate"`

	// AttemptsAllowed is an object, not a count — verified against a live
	// tenant, where an int here fails to decode. Unlimited attempts is a flag
	// rather than a sentinel number, which is the reason for the wrapper.
	AttemptsAllowed *AttemptsAllowed `json:"AttemptsAllowed"`
}

// AttemptsAllowed is how many times a quiz may be taken.
//
// Unlimited is a flag rather than a sentinel count, so a nil pointer (the
// tenant reported nothing) and IsUnlimited (the tenant reported no limit) are
// different answers and must not be rendered the same way.
type AttemptsAllowed struct {
	IsUnlimited             bool `json:"IsUnlimited"`
	NumberOfAttemptsAllowed int  `json:"NumberOfAttemptsAllowed"`
}

// ObjectListPage is D2L's *other* paging envelope.
//
// Enrollments and the classlist page with a PagingInfo bookmark; quizzes and
// the calendar page with a Next link. Two conventions in one API is a wart,
// not a mistake here — modeling it as one shape would break against a real
// tenant.
//
// Verified against a live tenant for both quizzes and calendar events, which
// is worth stating because the calendar was previously decoded as a bare array
// and failed on first contact.
type ObjectListPage[T any] struct {
	Next    *string `json:"Next"`
	Objects []T     `json:"Objects"`
}

// QuizListPage names the shape quizzes actually return.
type QuizListPage = ObjectListPage[Quiz]

// CalendarEventPage names the shape the calendar actually returns.
type CalendarEventPage = ObjectListPage[CalendarEvent]

// ClasslistUser is one enrolled person — how "who is my TA?" gets answered.
//
// Email, UserName and OrgDefinedId are governed by the Classlist tool's
// per-org settings, so they arrive empty when the institution withholds them.
// That is a normal configuration, not an error: logistics must degrade to
// "here is their name, no contact details" rather than failing.
//
// RoleId is numeric and org-specific — there is no portable constant for
// "Instructor" or "TA". Resolving it needs the roles route or a configured
// mapping; do not hardcode a number.
type ClasslistUser struct {
	Identifier        string `json:"Identifier"`
	ProfileIdentifier string `json:"ProfileIdentifier"`
	DisplayName       string `json:"DisplayName"`
	FirstName         string `json:"FirstName"`
	LastName          string `json:"LastName"`

	// The JSON key is "Username", not "UserName" — verified against a live
	// tenant, where the camel-cased spelling silently decodes to empty.
	UserName string `json:"Username"`

	OrgDefinedId string `json:"OrgDefinedId"`
	Email        string `json:"Email"`
	Pronouns     string `json:"Pronouns"`

	// ClasslistRoleDisplayName is the role already spelled out by the tenant —
	// "Learner", "Instructor" — which is what makes "who is my TA?" answerable.
	// RoleId alone cannot answer it: the numbers are assigned per institution
	// and carry no portable meaning.
	ClasslistRoleDisplayName string `json:"ClasslistRoleDisplayName"`

	RoleId       *int       `json:"RoleId"`
	LastAccessed *time.Time `json:"LastAccessed"`
	IsOnline     bool       `json:"IsOnline"`
}

// ClasslistPage is the paged envelope around ClasslistUser.
//
// Despite the route being named "paged" and reading like enrollment data, it
// uses the Next-link envelope rather than the PagingInfo bookmark one — the
// Next value is itself a bookmark URL. Verified against a live tenant, where
// the bookmark shape decoded to zero people with no error on a 25-person page.
type ClasslistPage = ObjectListPage[ClasslistUser]

// Upload is one file bound for a dropbox folder. v2.
type Upload struct {
	Name        string
	ContentType string
	Data        io.Reader
}

// Submission is what comes back from a successful dropbox submission. v2.
type Submission struct {
	Id             int              `json:"Id"`
	SubmissionDate time.Time        `json:"SubmissionDate"`
	Comment        RichText         `json:"Comment"`
	Files          []SubmissionFile `json:"Files"`
}

type SubmissionFile struct {
	FileId   int    `json:"FileId"`
	FileName string `json:"FileName"`
	Size     int64  `json:"Size"`
}
