package retrieve

// The narrow-then-search pipeline:
//
//	1. Structural narrowing — plain Go over the manifest. Filter candidates by
//	   course, date range, module ordering, topic type.
//	2. Vector search over chunks within that candidate set → top-K passages.
//	3. Caller synthesizes from passages plus document metadata, citing by
//	   title and slide/page.
//
// Over-fetch top-K generously and filter the results; at 0.3 ms per query
// that is free.
//
// No reranking step in v1: structural narrowing already limits candidates to
// roughly 20–40, and Claude acts as the reranker with full metadata access.
// Add voyage rerank-2.5 only on a specific signal — recall@20 high while
// recall@5 is low. If recall@20 is also low, the problem is upstream and a
// reranker will not fix it.
//
// Build step 4.
