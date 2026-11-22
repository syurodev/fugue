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

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/hostapi"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/storage"
)

func TestMain(m *testing.M) {
	if playFakeVendor(os.Args) {
		os.Exit(0)
	}
	if runCrashServeSubprocess(os.Args) {
		os.Exit(0)
	}
	cleanup, err := buildMigratedTemplate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	goleak.VerifyTestMain(m, goleak.Cleanup(cleanup))
}

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

// readyTimeout bounds the wait for serve to signal ready when the caller's
// context carries no deadline of its own.
const readyTimeout = 30 * time.Second

// awaitReady blocks until serve signals ready. A serve that fails or exits
// first reports its exit code and error instead of leaving the test blocked
// until the package timeout, which would hide the startup error.
func awaitReady(t *testing.T, ctx context.Context, ready <-chan struct{}, results <-chan serveResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	select {
	case <-ready:
	case res := <-results:
		t.Fatalf("serve ended before it was ready: exit %d, error: %v", res.code, res.err)
	case <-ctx.Done():
		t.Fatalf("serve was not ready before the context ended: %v", context.Cause(ctx))
	}
}

// serveDir returns a short data directory so the socket path fits sun_path
// and binds inside the data directory (host-api §3), like the hostapi tests.
func serveDir(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("fugue-cmd-%x", sum[:4]))
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			return
		}
	})
	// The directory name derives from the test name, so a repeated call (or a
	// run that left the directory behind) finds files already there.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A host started on an empty directory runs every migration. Under the
	// race detector that CPU work, multiplied by the parallel hosts on a
	// loaded runner, outlasted the readiness deadline. Starting from a
	// migrated database keeps the real open-and-verify path and drops the
	// repeated schema build; the storage tests own migration coverage.
	if err := os.CopyFS(dir, os.DirFS(templateDir(os.Getpid()))); err != nil {
		t.Fatal(err)
	}
	return dir
}

// templateDir is where TestMain keeps the migrated database template of the
// test process pid; deriving it from the pid needs no package-level state.
func templateDir(pid int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("fugue-cmd-template-%d", pid))
}

// buildMigratedTemplate migrates a fresh database once for the whole test
// process and returns a cleanup for the directory holding it.
func buildMigratedTemplate() (cleanup func(int), err error) {
	dir := templateDir(os.Getpid())
	if err = os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("template dir: %w", err)
	}
	cleanup = func(int) {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return
		}
	}
	clk := clock.NewFake(time.Unix(0, 0))
	store, err := storage.OpenStore(context.Background(), config.Config{DataDir: dir}, clk, ids.NewSequence("template"))
	if err != nil {
		cleanup(0)
		return nil, fmt.Errorf("migrate template: %w", err)
	}
	if err = store.Close(); err != nil {
		cleanup(0)
		return nil, fmt.Errorf("close template: %w", err)
	}
	return cleanup, nil
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
	defer stopWinner()
	ready := make(chan struct{})
	winnerSigs := make(chan os.Signal, 1)
	results := startServe(t, winnerCtx, cfg, serveOpts{ready: ready, sigs: winnerSigs})
	awaitReady(t, winnerCtx, ready, results)

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

	// A stop signal, not a canceled context: a canceled context also cancels
	// the drain, which then cannot wait for the loser's probe connection.
	winnerSigs <- syscall.SIGTERM
	res := <-results
	if res.code != exitOK {
		t.Fatalf("winner exits %d after SIGTERM, want %d", res.code, exitOK)
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
	awaitReady(t, ctx, ready, results)

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
	if code := runMain(t.Context(), []string{"bogus"}, &out, io.Discard); code != exitUsage {
		t.Fatalf("unknown subcommand exits %d, want %d", code, exitUsage)
	}
	if !strings.Contains(out.String(), "usage") {
		t.Fatalf("usage not written to out: %q", out.String())
	}
}

// blockingDrain wraps the real transport with a drain that blocks until the
// test releases it, then lets the real teardown run so no goroutine outlives
// the test.
type blockingDrain struct {
	app.HostTransport
	blocked chan struct{}
	release chan struct{}
}

func (b *blockingDrain) Drain(ctx context.Context, force bool) error {
	close(b.blocked)
	<-b.release
	if derr := b.HostTransport.Drain(ctx, force); derr != nil {
		return errcode.Wrap(errcode.Internal, "drain the wrapped host transport", derr)
	}
	return nil
}

// TestSecondSignalDuringDrainExitsWithoutWaiting pins that a second stop
// signal ends the wait for a drain that has no deadline: the process exits at
// once with the second signal's code, and the operating system releases the
// lock when it dies.
func TestSecondSignalDuringDrainExitsWithoutWaiting(t *testing.T) {
	t.Parallel()
	dir := serveDir(t)
	cfg := config.Config{DataDir: dir}
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	blocked := make(chan struct{})
	release := make(chan struct{})
	sigs := make(chan os.Signal, 2)
	results := startServe(t, ctx, cfg, serveOpts{
		ready: ready,
		sigs:  sigs,
		wrapTransport: func(tr app.HostTransport) app.HostTransport {
			return &blockingDrain{HostTransport: tr, blocked: blocked, release: release}
		},
	})
	awaitReady(t, ctx, ready, results)

	// The first signal starts the drain; the wrapped transport blocks it.
	sigs <- syscall.SIGTERM
	<-blocked

	// The second signal must return at once with its own code, while the
	// drain is still blocked.
	sigs <- syscall.SIGINT
	res := <-results
	if res.code != exitInterrupted {
		t.Fatalf("serve exits %d on the second signal, want %d", res.code, exitInterrupted)
	}
	if res.err != nil {
		t.Fatalf("serve on the second signal reports %v", res.err)
	}

	// Release the drain so the real teardown runs and the host's base
	// context ends with the task context; no goroutine outlives the test.
	close(release)
	cancel()
}
