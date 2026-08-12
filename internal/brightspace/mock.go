package brightspace

// MockClient serves the fixtures under testdata/courses. It is load-bearing,
// not decorative: ~60% of this project is built and tested against it, so its
// responses must match the documented Valence schemas rather than whatever
// shape is convenient.
//
// Build step 1, fixtures in step 2.
