// Package retrieve provides hybrid search over course content: structural
// metadata narrowing, then dense vector search over chunk embeddings.
//
// Neither half is sufficient alone. Vector search finds passages whose
// vocabulary differs from the query ("eigenvalues" under a module titled
// "Linear Transformations"). Structural filtering answers what embeddings
// provably cannot ("exam 2 covers the modules between exam 1 and now" is an
// ordering operation, not a similarity one).
//
// The Index interface exists because chromem-go is pre-v1.0. If it needs
// replacing — sqlite-vec for SQL range filtering, pgvector at scale — the swap
// stays contained to chromem.go.
//
// Build step 4.
package retrieve
