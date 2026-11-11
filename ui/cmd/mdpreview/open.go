package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
)

// errBlockedScheme reports an open request for a scheme the preview never
// hands to the system.
var errBlockedScheme = errors.New("only http, https and mailto links are opened")

// allowedScheme reports whether url starts with a scheme the preview opens.
func allowedScheme(url string) bool {
	lower := strings.ToLower(url)
	for _, prefix := range []string{"http://", "https://", "mailto:"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// openCommand is the system opener for goos and its argument list: the URL
// is always one argument and never part of a shell command line.
func openCommand(goos, url string) (name string, args []string) {
	if goos == "darwin" {
		return "open", []string{url}
	}
	return "xdg-open", []string{url}
}

// openURL hands an allowed URL to the system opener and does not wait for
// it.
func openURL(ctx context.Context, url string) error {
	if !allowedScheme(url) {
		return errBlockedScheme
	}
	name, args := openCommand(runtime.GOOS, url)
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the name is fixed per OS and the URL is one vetted argument
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	// The goroutine ends when the opener exits; it only reaps the process.
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Warn("mdpreview: opener failed", "opener", name, "err", err)
		}
	}()
	return nil
}
