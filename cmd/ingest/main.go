// Command ingest syncs course content into the store and vector index.
//
// One pass by default; --watch runs it as a daemon. Because it runs
// unattended, the failure modes have to be designed out rather than noticed:
// a lockfile so overlapping runs cannot corrupt the manifest, atomic rename so
// an interrupted write cannot leave one truncated, and --verify to catch
// orphaned vectors whose source text no longer exists.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	watch := flag.Bool("watch", false, "watch the content folder and ingest on change")
	verify := flag.Bool("verify", false, "check store and index consistency, then exit")
	flag.Parse()

	mode := "one-shot"
	switch {
	case *verify:
		mode = "verify"
	case *watch:
		mode = "watch"
	}

	fmt.Fprintf(os.Stderr, "ingest (%s) is not implemented yet (build step 5)\n", mode)
	os.Exit(1)
}
