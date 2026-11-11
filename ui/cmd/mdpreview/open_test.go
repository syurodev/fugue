package main

import (
	"errors"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreAnyFunction("github.com/dlclark/regexp2/v2.runClock"))
}

// TestOnlyHTTPHTTPSAndMailtoAreOpened pins the scheme allowlist.
func TestOnlyHTTPHTTPSAndMailtoAreOpened(t *testing.T) {
	t.Parallel()
	allowed := []string{"http://example.com", "https://example.com/a?b=c", "HTTPS://EXAMPLE.COM", "mailto:a@example.org"}
	blocked := []string{"javascript:alert(1)", "file:///etc/passwd", "data:text/html,hi", "ftp://example.com", "-flag", "./relative", "", "xdg-open https://example.com"}
	for _, u := range allowed {
		if !allowedScheme(u) {
			t.Errorf("%q is blocked, want opened", u)
		}
	}
	for _, u := range blocked {
		if allowedScheme(u) {
			t.Errorf("%q is allowed, want blocked", u)
		}
	}
}

// TestOpenCommandPassesTheURLAsOneArgument pins the opener per OS and that
// the URL is never split or joined into a command line.
func TestOpenCommandPassesTheURLAsOneArgument(t *testing.T) {
	t.Parallel()
	const url = "https://example.com/a b;rm -rf?x=$(id)"
	for goos, want := range map[string]string{"linux": "xdg-open", "darwin": "open"} {
		name, args := openCommand(goos, url)
		if name != want || len(args) != 1 || args[0] != url {
			t.Errorf("%s: command = %q %q, want %q with the URL as its single argument", goos, name, args, want)
		}
	}
}

// TestOpenURLRefusesBlockedSchemesWithoutStartingAProcess checks the guard
// runs before anything is executed.
func TestOpenURLRefusesBlockedSchemesWithoutStartingAProcess(t *testing.T) {
	t.Parallel()
	if err := openURL("javascript:alert(1)"); !errors.Is(err, errBlockedScheme) {
		t.Errorf("openURL error = %v, want errBlockedScheme", err)
	}
	if err := openURL("file:///etc/passwd"); !errors.Is(err, errBlockedScheme) {
		t.Errorf("openURL error = %v, want errBlockedScheme", err)
	}
}
