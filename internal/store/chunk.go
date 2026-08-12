package store

// Structure-aware chunking: per slide for PPTX, per heading for markdown and
// DOCX, else ~512-token windows with overlap. Boundaries landing on real
// structure is what makes citation ("slide 12 of Lecture 8") possible.
//
// Each chunk carries topic_id in its metadata so a search hit resolves back to
// a title, URL, and page.
//
// Build step 3.
