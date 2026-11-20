//go:build unix

package main

import (
	"context"
	"os"
	"syscall"
	"testing"

	"github.com/syurodev/fugue/internal/config"
)

// TestServeExitCodeForSignals pins the signal-to-exit-code mapping (cli §5.2)
// over the real process signal handlers: it is the only test whose serve
// installs them, so the process-wide signals it sends cannot stop another
// parallel test's host.
func TestServeExitCodeForSignals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		sig  syscall.Signal
		want int
	}{
		{syscall.SIGINT, exitInterrupted},
		{syscall.SIGTERM, exitOK},
	} {
		dir := serveDir(t)
		cfg := config.Config{DataDir: dir}
		ctx, cancel := context.WithCancel(t.Context())
		ready := make(chan struct{})
		results := startServe(t, ctx, cfg, serveOpts{ready: ready})
		awaitReady(t, ctx, ready, results)

		if err := syscall.Kill(os.Getpid(), tc.sig); err != nil {
			t.Fatalf("send %v: %v", tc.sig, err)
		}
		res := <-results
		if res.code != tc.want {
			t.Fatalf("serve exits %d on %v, want %d", res.code, tc.sig, tc.want)
		}
		if res.err != nil {
			t.Fatalf("serve on %v reports %v", tc.sig, res.err)
		}
		cancel()
	}
}
