// Package specialist holds the eight domain agents and the single registry
// that defines them.
//
// The registry is the single source of truth. It generates both entry points:
// the in-process dispatcher used by cmd/chat, and the MCP tool list served by
// cmd/brightspace-mcp --expose=specialists. Adding a Specialist must appear in
// both with no second edit — that invariant has its own test.
//
// Specialists exist rather than one agent with every tool because tool
// selection degrades with tool count, because the prompts genuinely differ
// (grades must refuse to guess at numbers; materials should synthesize freely;
// logistics should quote policy verbatim), and because effort can then scale
// with the question.
//
// Build step 7.
package specialist
