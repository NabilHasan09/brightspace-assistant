package brightspace

import "time"

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

// OrgUnitInfo identifies a course. Code is what a student says out loud
// ("MTH1003"); Id is what every other endpoint wants.
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

// PagingInfo appears on D2L's paged collections. Not honored yet — the mock
// returns every item in one page. It matters against a live tenant with a
// large classlist or a busy discussion forum.
type PagingInfo struct {
	Bookmark     string `json:"Bookmark"`
	HasMoreItems bool   `json:"HasMoreItems"`
}

// MyOrgUnitInfo is one entry from the enrollments endpoint, which wraps
// OrgUnitInfo alongside access dates rather than returning it bare.
type MyOrgUnitInfo struct {
	OrgUnitInfo OrgUnitInfo `json:"OrgUnitInfo"`
	Access      struct {
		IsActive  bool       `json:"IsActive"`
		StartDate *time.Time `json:"StartDate"`
		EndDate   *time.Time `json:"EndDate"`
		CanAccess bool       `json:"CanAccess"`
	} `json:"Access"`
}

// MyEnrollmentsResponse is the paged envelope around MyOrgUnitInfo.
type MyEnrollmentsResponse struct {
	PagingInfo PagingInfo      `json:"PagingInfo"`
	Items      []MyOrgUnitInfo `json:"Items"`
}
