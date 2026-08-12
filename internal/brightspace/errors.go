package brightspace

import "errors"

var (
	// ErrNotImplemented marks surface that is gated on institutional OAuth
	// credentials that do not exist yet. LiveClient returns it from every
	// method; MockClient never does.
	ErrNotImplemented = errors.New("brightspace: not implemented")

	// ErrNotFound means the org unit, module, or topic does not exist. The
	// live API signals this with a 404.
	ErrNotFound = errors.New("brightspace: not found")

	// ErrNotFileTopic means the topic exists but has no bytes behind it.
	// Only file-type topics return content; link topics resolve to an
	// external URL, and publisher topics to a third-party system. This is a
	// documented Valence behavior, not an edge case, so ingest has to expect
	// it for a meaningful share of any real course.
	ErrNotFileTopic = errors.New("brightspace: topic is not a file topic")
)
