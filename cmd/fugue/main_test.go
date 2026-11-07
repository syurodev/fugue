package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/hostapi"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/storage"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// testVersion is the host version the tests serve; the real binary takes it
// from the ldflags-injectable variable.
const testVersion = "test"

// serveResult is what a serve run reports back to its test: the exit code and
// the startup error, if any.
type serveResult struct {
	code int
	err  error
}

// startServe runs serve in a supervised task and returns the result channel.
// The test reads ready before it may observe the serving state or send a
// signal: ready closes after the signal handlers are installed and the host
// serves the socket. sigs nil uses the process signal handlers; the other
// tests inject a never-signaled channel, so the process-wide signals of the
// signal test cannot stop another parallel test's host.
func startServe(t *testing.T, ctx context.Context, cfg config.Config, opts serveOpts) chan serveResult {
	t.Helper()
	results := make(chan serveResult, 1)
	supervise.Go(ctx, "test.serve", func(taskCtx context.Context) error {
		code, err := serve(taskCtx, io.Discard, cfg, testVersion, opts)
		results <- serveResult{code: code, err: err}
		return nil
	})
	return results
}

// serveDir returns a short data directory so the socket path fits sun_path
// and binds inside the data directory (host-api §3), like the hostapi tests.
func serveDir(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("fugue-cmd-%x", sum[:4]))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			return
		}
	})
	return dir
}

func socketPath(dir string) string { return filepath.Join(dir, hostapi.SocketFileName) }

// mustNotExist fails when path still exists; err is the stat result.
func mustNotExist(t *testing.T, what, path string, err error) {
	t.Helper()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s still present at %s: %v", what, path, err)
	}
}

func TestSecondServeOnTheSameDataDirectoryFailsTyped(t *testing.T) {
	t.Parallel()
	dir := serveDir(t)
	cfg := config.Config{DataDir: dir}
	winnerCtx, stopWinner := context.WithCancel(t.Context())
	ready := make(chan struct{})
	results := startServe(t, winnerCtx, cfg, serveOpts{ready: ready, sigs: make(chan os.Signal, 1)})
	<-ready

	hostJSON, err := os.ReadFile(hostapi.HostFilePath(dir))
	if err != nil {
		t.Fatalf("read the winner's host.json: %v", err)
	}

	// The loser must fail typed without touching the winner: the socket is
	// built before the lock, so the live-host probe refuses the start and
	// nothing is unlinked (host-api §3, plan scope decision 5).
	code, err := serve(t.Context(), io.Discard, cfg, testVersion, serveOpts{sigs: make(chan os.Signal, 1)})
	if code != exitFailure {
		t.Fatalf("second serve on a locked data directory exits %d, want %d", code, exitFailure)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("second serve fails untyped: %v", err)
	}
	if !errors.Is(err, hostapi.ErrSocketInUse) {
		t.Fatalf("second serve fails with %v, want the typed socket-in-use error", err)
	}
	if errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("second serve fails with code %q, want %q", errcode.CodeOf(err), errcode.FailedPrecondition)
	}

	after, err := os.ReadFile(hostapi.HostFilePath(dir))
	if err != nil {
		t.Fatalf("the loser touched the winner's host.json: %v", err)
	}
	if !bytes.Equal(hostJSON, after) {
		t.Fatalf("the loser rewrote the winner's host.json")
	}
	if _, err := os.Stat(socketPath(dir)); err != nil {
		t.Fatalf("the loser disturbed the winner's socket: %v", err)
	}

	stopWinner()
	res := <-results
	if res.code != exitOK {
		t.Fatalf("winner exits %d after cancel, want %d", res.code, exitOK)
	}
	if res.err != nil {
		t.Fatalf("winner on a clean stop reports %v", res.err)
	}
}

func TestServeStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	dir := serveDir(t)
	cfg := config.Config{DataDir: dir}
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	results := startServe(t, ctx, cfg, serveOpts{ready: ready, sigs: make(chan os.Signal, 1)})
	<-ready

	cancel()
	res := <-results
	if res.code != exitOK {
		t.Fatalf("serve exits %d on context cancel, want %d", res.code, exitOK)
	}
	if res.err != nil {
		t.Fatalf("serve on a clean stop reports %v", res.err)
	}
	_, err := os.Stat(hostapi.HostFilePath(dir))
	mustNotExist(t, "host.json", hostapi.HostFilePath(dir), err)
	_, err = os.Stat(socketPath(dir))
	mustNotExist(t, "socket", socketPath(dir), err)

	// The lock is the liveness authority: after the drain a follow-up store
	// open must succeed.
	clk := clock.NewFake(time.Unix(0, 0))
	store, err := storage.OpenStore(t.Context(), config.Config{DataDir: dir}, clk, ids.NewUUIDv7(clk, ids.NewDeterministic("probe")))
	if err != nil {
		t.Fatalf("open the store after the host stopped: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close the probe store: %v", err)
	}
}

func TestServeUsageRejectsUnknownSubcommand(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if code := runMain(t.Context(), []string{"bogus"}, &out); code != exitUsage {
		t.Fatalf("unknown subcommand exits %d, want %d", code, exitUsage)
	}
	if !strings.Contains(out.String(), "usage") {
		t.Fatalf("usage not written to out: %q", out.String())
	}
}

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
		<-ready

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
