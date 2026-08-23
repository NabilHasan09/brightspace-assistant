// Command brightspace-mcp serves the assistant over MCP on stdio.
//
// Three exposure modes, all backed by the same registry:
//
//	--expose=data          raw data tools, for MCP clients that orchestrate
//	                       themselves (Claude Desktop, Claude Code, Cursor)
//	--expose=specialists   the eight ask_*_specialist agent-as-tool wrappers
//	--expose=both          the whole surface, for debugging
//
// Only --expose=data is built (build step 6). Specialists land at build step 8
// and need their own Anthropic API key, because each specialist handler runs a
// full Claude loop — which inverts the usual "MCP servers are dumb data access"
// assumption.
//
// With no Brightspace credentials, which is the normal case, the server reads
// the fixture course data under testdata/. Registering it with Claude Desktop
// looks like this — note the absolute paths, because the client launches this
// binary from an arbitrary working directory:
//
//	{
//	  "mcpServers": {
//	    "brightspace": {
//	      "command": "/absolute/path/to/bin/brightspace-mcp",
//	      "args": [
//	        "--fixtures=/absolute/path/to/testdata",
//	        "--now=2026-03-10T09:00:00Z"
//	      ]
//	    }
//	  }
//	}
//
// --now is what makes the fixtures answerable. They describe a spring 2026
// semester, so asking "what's due this week" against the real clock correctly
// reports nothing at all.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
	"github.com/NabilHasan09/brightspace-assistant/internal/mcptools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version identifies this server to MCP clients.
const version = "0.1.0"

func main() {
	expose := flag.String("expose", "data", "tool surface to serve: data, specialists, or both")
	fixtures := flag.String("fixtures", "testdata", "directory of fixture course data, used when no Brightspace credentials are set")
	nowFlag := flag.String("now", "", "RFC3339 timestamp to treat as the current time; the fixtures describe a spring 2026 semester")
	tz := flag.String("timezone", "America/New_York", "IANA timezone for reporting dates")
	roles := flag.String("roles", "", "classlist role id to name mapping, e.g. 103=Instructor,104=Teaching Assistant")
	flag.Parse()

	// Stdout is the MCP transport. Anything written there that is not a
	// JSON-RPC frame corrupts the session, so every diagnostic goes to stderr.
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	log.SetPrefix("brightspace-mcp: ")

	if err := run(*expose, *fixtures, *nowFlag, *tz, *roles); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(expose, fixtures, nowFlag, tz, roles string) error {
	switch expose {
	case "data":
	case "specialists", "both":
		return fmt.Errorf("--expose=%s is not built yet: specialists land at build step 8", expose)
	default:
		return fmt.Errorf("unknown --expose value %q: want data, specialists, or both", expose)
	}

	opts, err := serverOptions(nowFlag, tz, roles)
	if err != nil {
		return err
	}

	client, source, err := newClient(fixtures)
	if err != nil {
		return err
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "brightspace",
		Version: version,
		Title:   "Brightspace course data",
	}, nil)
	mcptools.New(client, opts...).Register(srv)

	// Shut down on the signals a supervising client sends when it closes the
	// session, so the process does not outlive the client that spawned it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("serving data tools over stdio (%s)", source)
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("serving: %w", err)
	}
	return nil
}

// newClient picks the data source. Fixtures are the normal case: there is no
// student path to registering a Valence OAuth client, so credentials are the
// exception rather than the default.
//
// D2L_ACCESS_TOKEN is a token obtained by hand, which is how a real tenant gets
// probed the first time. The full authorization code flow belongs with the rest
// of the OAuth work, not here.
func newClient(fixtures string) (brightspace.Client, string, error) {
	host := strings.TrimSpace(os.Getenv("D2L_HOST"))
	token := strings.TrimSpace(os.Getenv("D2L_ACCESS_TOKEN"))

	if host != "" && token != "" {
		return brightspace.NewLiveClient(host, brightspace.StaticTokenClient(token)), "live: " + host, nil
	}
	if host != "" {
		return nil, "", fmt.Errorf("D2L_HOST is set but D2L_ACCESS_TOKEN is not: refusing to fall back to fixtures silently when a tenant was configured")
	}

	info, err := os.Stat(fixtures)
	if err != nil {
		return nil, "", fmt.Errorf("fixtures: %w (pass --fixtures with an absolute path)", err)
	}
	if !info.IsDir() {
		return nil, "", fmt.Errorf("fixtures: %s is not a directory", fixtures)
	}
	return brightspace.NewMockClient(os.DirFS(fixtures)), "fixtures: " + fixtures, nil
}

func serverOptions(nowFlag, tz, roles string) ([]mcptools.Option, error) {
	var opts []mcptools.Option

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("--timezone %q: %w", tz, err)
	}
	opts = append(opts, mcptools.WithLocation(loc))

	if nowFlag != "" {
		at, err := time.Parse(time.RFC3339, nowFlag)
		if err != nil {
			return nil, fmt.Errorf("--now %q: want an RFC3339 timestamp like 2026-03-10T09:00:00Z: %w", nowFlag, err)
		}
		// Frozen rather than offset from the real clock: a demo that drifts out
		// of the fixture window while you are using it is worse than one that
		// never moves.
		opts = append(opts, mcptools.WithClock(func() time.Time { return at }))
	}

	if roles != "" {
		m, err := parseRoles(roles)
		if err != nil {
			return nil, err
		}
		opts = append(opts, mcptools.WithRoleNames(m))
	}
	return opts, nil
}

// parseRoles reads "103=Instructor,104=Teaching Assistant" into a map. Role ids
// are institution-specific, so there is no default worth shipping.
func parseRoles(s string) (map[int]string, error) {
	out := make(map[int]string)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		idStr, name, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("--roles %q: want id=name pairs, e.g. 103=Instructor", pair)
		}
		id, err := strconv.Atoi(strings.TrimSpace(idStr))
		if err != nil {
			return nil, fmt.Errorf("--roles %q: %q is not a role id", pair, idStr)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("--roles %q: no name given for role %d", pair, id)
		}
		out[id] = name
	}
	return out, nil
}
