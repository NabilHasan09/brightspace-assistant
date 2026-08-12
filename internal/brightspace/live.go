package brightspace

// LiveClient talks to a real Brightspace tenant over OAuth2 (authorization
// code grant — submissions are attributed to the calling token, so it must be
// the student's own).
//
// Every method returns ErrNotImplemented until credentials exist. Two things
// must be resolved on first live connection: the LP and LE API versions are
// unpinned in D2L's docs, and no response shape here has been validated
// against a real tenant.
//
// Build step 1 (stubs); real implementation is gated on institutional access.
