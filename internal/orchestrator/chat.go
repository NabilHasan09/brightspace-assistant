// Package orchestrator drives the Go chat app: specialists in-process, data
// tools over MCP.
//
// In-process rather than through the specialist MCP tools because that
// boundary costs streaming — MCP tool results return atomically, so a user
// would wait with no output while a specialist loops.
//
// Build step 9.
package orchestrator
