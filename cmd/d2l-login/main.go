// Command d2l-login obtains a Brightspace session and writes it where the rest
// of the tools look for it.
//
// It opens a browser at your tenant, waits while you log in, and saves the
// resulting cookies. The next run usually needs no login at all, because the
// browser profile persists — so refreshing an expired session becomes running
// this again and clicking through a sign-in you are already signed in to.
//
// This replaces copying a Cookie header out of DevTools by hand. It does not
// make anything unattended: when the session expires, a person has to be here.
// Only OAuth fixes that, and it needs a Brightspace administrator.
//
//	d2l-login                              # prompts if needed, saves the session
//	d2l-login --check                      # is the saved session still good?
//	d2l-login --headless                   # reuse a profile that is already logged in
//	d2l-login --out ~/.d2l-cookie          # where to write it
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/NabilHasan09/brightspace-assistant/internal/brightspace"
	"github.com/NabilHasan09/brightspace-assistant/internal/browserauth"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "d2l-login: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("locating home directory: %w", err)
	}

	var (
		host     = flag.String("host", envOr("D2L_HOST", ""), "tenant root, e.g. https://brightspace.cuny.edu (or set D2L_HOST)")
		out      = flag.String("out", filepath.Join(home, ".d2l-cookie"), "where to write the session cookie")
		profile  = flag.String("profile", "", "browser profile directory (default ~/.brightspace-assistant/browser-profile)")
		headless = flag.Bool("headless", false, "run without a visible window; only works if the profile is already logged in")
		check    = flag.Bool("check", false, "test the saved session against the tenant and exit")
		timeout  = flag.Duration("timeout", 5*time.Minute, "how long to wait for the login to complete")
	)
	flag.Parse()

	if *host == "" {
		return errors.New("--host is required (or set D2L_HOST)")
	}

	// Ctrl-C should close the browser rather than orphan it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *check {
		return checkSession(ctx, *host, *out)
	}

	fmt.Fprintf(os.Stderr, "opening %s — log in if prompted, then leave the window alone\n", *host)
	header, err := browserauth.Login(ctx, browserauth.Options{
		Host:       *host,
		ProfileDir: *profile,
		Timeout:    *timeout,
		Headless:   *headless,
	})
	if err != nil {
		return err
	}
	if err := browserauth.Save(*out, header); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "session saved to %s\n", *out)

	// Verifying immediately, because a saved file that does not work is worse
	// than a failed login: it looks like success and fails later somewhere else.
	return checkSession(ctx, *host, *out)
}

// checkSession makes one real API call, which is the only way to know a session
// works. A cookie file can look perfectly well formed and be expired.
func checkSession(ctx context.Context, host, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client := brightspace.NewLiveClient(host, brightspace.CookieFileClient(path))
	units, err := client.MyEnrollments(ctx)
	if err != nil {
		if errors.Is(err, brightspace.ErrUnauthorized) {
			return fmt.Errorf("the session in %s is not valid — run d2l-login without --check to get a new one: %w", path, err)
		}
		return fmt.Errorf("checking the session: %w", err)
	}

	// Counting rather than listing: this is a status check, and course names on
	// a terminal someone might be sharing are nobody else's business.
	fmt.Fprintf(os.Stderr, "session works: %d enrollments visible\n", len(units))
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
