package retrieve

// chromem-go backing for Index: pure Go, no cgo, optional disk persistence.
//
// Its metadata filtering is exact-match only — no range queries. So each
// course gets its own collection, and date/module filtering happens in Go
// against the manifest either side of the vector query. Confirmed against
// v0.7.0, where the filter argument is a map[string]string:
//
//	func (c *Collection) Query(ctx context.Context, queryText string,
//	    nResults int, where, whereDocument map[string]string) ([]Result, error)
//
// String-valued metadata also means topic_id must be stringified on the way
// in and parsed on the way out.
//
// Build step 4.
