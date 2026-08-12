package store

// Text extraction, per format:
//
//	PDF          → Claude document block at ingest, result cached by hash.
//	               (Pure-Go PDF parsers mangle multi-column layouts and tables;
//	               the good one needs cgo. Sidestep both.)
//	DOCX / PPTX  → archive/zip + encoding/xml. They are just ZIP+XML.
//	TXT / MD     → direct read.
//	Link topics  → metadata only; record the URL, flag as unreadable.
//
// Build step 3.
