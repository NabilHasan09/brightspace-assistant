// Package store is the content-addressed document store: manifest.json as the
// document database, blobs/ for originals, text/ for extracted markdown, all
// keyed by SHA-256.
//
// Content addressing buys free dedupe and idempotent ingest. Writes go through
// a lockfile and an atomic rename so unattended runs cannot corrupt the
// manifest.
//
// Build step 3.
package store
