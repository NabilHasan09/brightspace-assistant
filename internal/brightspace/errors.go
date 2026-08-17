package brightspace

import "errors"

var (
	// ErrNotImplemented marks surface that is declared but not built. Nothing
	// returns it today; it is what SubmitToDropbox will return in v1, so
	// submission lands as a fill-in rather than a refactor.
	ErrNotImplemented = errors.New("brightspace: not implemented")

	// ErrNotFound means the org unit, module, or topic does not exist, or that
	// the course releases no final grade. LiveClient derives it from a 404.
	ErrNotFound = errors.New("brightspace: not found")

	// ErrUnauthorized means the token is missing, expired, or lacks the scope
	// for this route (401 or 403). Worth distinguishing from ErrNotFound: a
	// scope that was never requested at registration cannot be widened without
	// going back to a Brightspace admin, so this is a configuration failure to
	// surface loudly, not a retry.
	ErrUnauthorized = errors.New("brightspace: unauthorized")

	// ErrNotFileTopic means the topic exists but has no bytes behind it.
	// Only file-type topics return content; link topics resolve to an
	// external URL, and publisher topics to a third-party system. This is a
	// documented Valence behavior, not an edge case, so ingest has to expect
	// it for a meaningful share of any real course.
	ErrNotFileTopic = errors.New("brightspace: topic is not a file topic")
)
