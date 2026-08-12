// Command chat is the Go REPL front end: specialists run in-process so their
// output can stream, while data tools are consumed over MCP.
//
// Prints the selected specialist, retrieved chunk titles, and tool calls in
// dim text — the retrieval path should be visible, not magic.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "chat is not implemented yet (build step 9)")
	os.Exit(1)
}
