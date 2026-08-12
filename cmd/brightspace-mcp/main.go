// Command brightspace-mcp serves the assistant over MCP on stdio.
//
// Three exposure modes, all backed by the same registry:
//
//	--expose=data          raw data tools, for MCP clients that orchestrate
//	                       themselves (Claude Desktop, Claude Code, Cursor)
//	--expose=specialists   the eight ask_*_specialist agent-as-tool wrappers
//	--expose=both          the whole surface, for debugging
//
// Note that --expose=specialists needs its own Anthropic API key: each
// specialist handler runs a full Claude loop, which inverts the usual
// "MCP servers are dumb data access" assumption.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	expose := flag.String("expose", "data", "tool surface to serve: data, specialists, or both")
	flag.Parse()

	switch *expose {
	case "data", "specialists", "both":
	default:
		fmt.Fprintf(os.Stderr, "unknown --expose value %q: want data, specialists, or both\n", *expose)
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "brightspace-mcp --expose=%s is not implemented yet (build steps 6 and 8)\n", *expose)
	os.Exit(1)
}
