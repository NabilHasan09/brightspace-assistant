// Package browserauth obtains a Brightspace session by letting a person log in
// to a real browser, then reading the cookies back out of it.
//
// Why a browser at all: the /d2l/api/ routes accept the session cookies the web
// UI already holds, and there is no student path to registering an OAuth client.
// Getting those cookies means being logged in, and being logged in at CUNY means
// Oracle Access Manager single sign-on with multi-factor — a flow that is meant
// to resist automation and does. So this does not automate the login. It hosts
// it, and collects the result.
//
// What that buys over copying a Cookie header out of DevTools:
//
//   - The browser keeps its own profile, so the next run usually needs no login
//     at all, and an expired session is a click rather than a paste.
//   - No password is ever stored or typed by this program. The credential it
//     ends up with is a session, exactly as if you had logged in by hand.
//   - HttpOnly cookies are readable, which document.cookie cannot do.
//
// What it does not buy: unattended operation. When the session expires, a person
// has to be present. Only OAuth fixes that, and it needs an administrator.
package browserauth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// sessionCookie is the cookie that actually authenticates an API call. Waiting
// for this specific one is what distinguishes "the login page finished loading"
// from "the login succeeded".
const sessionCookie = "d2lSecureSessionVal"

// Options configures a login.
type Options struct {
	// Host is the tenant root, e.g. "https://brightspace.cuny.edu".
	Host string

	// ProfileDir is where Chrome keeps its user data between runs. A dedicated
	// directory rather than the everyday profile, for two reasons: Chrome locks
	// its cookie database while running, so sharing would mean the tool and the
	// browser fighting over it, and a profile this program created is one the
	// user can delete without touching their real browsing session.
	ProfileDir string

	// Timeout bounds how long to wait for the login to complete. Generous by
	// default: single sign-on plus a multi-factor prompt on a phone is not a
	// fast path, and timing out halfway through is worse than waiting.
	Timeout time.Duration

	// Headless runs without a visible window. Useful only when the profile
	// already holds a live session — a login cannot be completed in a window
	// nobody can see.
	Headless bool
}

func (o Options) withDefaults() (Options, error) {
	if o.Host == "" {
		return o, errors.New("browserauth: Host is required")
	}
	o.Host = strings.TrimRight(o.Host, "/")
	if _, err := url.Parse(o.Host); err != nil {
		return o, fmt.Errorf("browserauth: Host %q is not a URL: %w", o.Host, err)
	}
	if o.ProfileDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return o, fmt.Errorf("browserauth: locating home directory: %w", err)
		}
		o.ProfileDir = filepath.Join(home, ".brightspace-assistant", "browser-profile")
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Minute
	}
	return o, nil
}

// Login opens a browser at the tenant, waits until a session exists, and returns
// the cookies as a Cookie header value ready for brightspace.CookieClient.
//
// If the profile already holds a live session the browser closes again almost
// immediately, which is the common case after the first run.
func Login(ctx context.Context, opts Options) (string, error) {
	opts, err := opts.withDefaults()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(opts.ProfileDir, 0o700); err != nil {
		return "", fmt.Errorf("browserauth: creating profile directory: %w", err)
	}

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.UserDataDir(opts.ProfileDir),
		chromedp.Flag("headless", opts.Headless),
		// DefaultExecAllocatorOptions disables these for scraping; a person is
		// about to use this window, so give them a usable browser.
		chromedp.Flag("hide-scrollbars", false),
		chromedp.Flag("mute-audio", false),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	timeoutCtx, cancelTimeout := context.WithTimeout(browserCtx, opts.Timeout)
	defer cancelTimeout()

	var header string
	err = chromedp.Run(timeoutCtx,
		chromedp.Navigate(opts.Host+"/d2l/home"),
		waitForSession(opts.Host, &header),
	)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("browserauth: no %s after %s — the login did not complete", sessionCookie, opts.Timeout)
		}
		return "", fmt.Errorf("browserauth: %w", err)
	}
	return header, nil
}

// waitForSession polls the browser's cookie store until the session cookie
// appears, then formats every cookie for the tenant as a Cookie header.
//
// Polling rather than watching for a navigation, because there is no single
// event that means "logged in": the identity provider may redirect several
// times, and which hop sets the cookie is not something to depend on. The
// cookie's existence is the only reliable signal.
func waitForSession(host string, out *string) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			cookies, err := network.GetCookies().Do(ctx)
			if err != nil {
				return fmt.Errorf("reading cookies: %w", err)
			}
			if header, ok := cookieHeader(cookies, host); ok {
				*out = header
				return nil
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	}
}

// cookieHeader renders the tenant's cookies as a Cookie header, reporting
// whether a session is present.
//
// Every cookie for the host goes in, not just the two D2L ones. The browser
// sends them all, and this is meant to reproduce a request the tenant already
// accepts rather than a minimal one that might work — the analytics cookies are
// harmless and guessing at which are load-bearing is not worth the risk.
func cookieHeader(cookies []*network.Cookie, host string) (string, bool) {
	tenant, err := url.Parse(host)
	if err != nil {
		return "", false
	}

	var (
		pairs     []string
		haveToken bool
	)
	for _, c := range cookies {
		if !domainMatches(c.Domain, tenant.Hostname()) {
			continue
		}
		if c.Name == sessionCookie && c.Value != "" {
			haveToken = true
		}
		pairs = append(pairs, c.Name+"="+c.Value)
	}
	if !haveToken {
		return "", false
	}

	// Sorted so the same session produces the same header, which makes a
	// diff between two runs readable.
	sort.Strings(pairs)
	return strings.Join(pairs, "; "), true
}

// domainMatches applies cookie-domain rules: a leading dot means the cookie also
// belongs to subdomains.
func domainMatches(cookieDomain, hostname string) bool {
	cookieDomain = strings.TrimPrefix(cookieDomain, ".")
	return hostname == cookieDomain || strings.HasSuffix(hostname, "."+cookieDomain)
}

// Save writes a Cookie header to path with owner-only permissions, creating
// parent directories as needed.
//
// Owner-only because this is a live session: anyone who can read the file is
// logged in as the user until it expires.
func Save(path, header string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("browserauth: creating directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(header), 0o600); err != nil {
		return fmt.Errorf("browserauth: writing %s: %w", path, err)
	}
	return nil
}
