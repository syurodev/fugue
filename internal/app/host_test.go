package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/storage"
)

// scriptedTransport records Drain calls and can block the drain so a test
// controls when the transport step finishes.
type scriptedTransport struct {
	mu        sync.Mutex
	calls     int
	started   chan struct{}
	startOnce sync.Once
	block     chan struct{} // nil means unblocked
	done      chan struct{} // closed when Drain returns
	doneOnce  sync.Once
	lastErr   error
}

func newScriptedTransport() *scriptedTransport {
	return &scriptedTransport{started: make(chan struct{}), done: make(chan struct{})}
}

// Drain records the call and, when blocked, waits for the release or for the
// context to be canceled, returning the context error.
func (s *scriptedTransport) Drain(ctx context.Context, _ bool) error {
	defer s.doneOnce.Do(func() { close(s.done) })
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.startOnce.Do(func() { close(s.started) })
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			err := errcode.Wrap(errcode.Canceled, "the scripted drain was canceled", ctx.Err())
			s.mu.Lock()
			s.lastErr = err
			s.mu.Unlock()
			return err
		}
	}
	return nil
}

func (s *scriptedTransport) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

const (
	testHostVersion = "host-test"
	testV10         = "v10"
	testCommandID   = "cmd-1"
)

func testHostConfig() HostConfig {
	return HostConfig{
		HostVersion: testHostVersion,
		APIVersions: []string{"v1", "v2"},
		IdleGrace:   0,
	}
}

func testHostDeps(dir string, clk *clock.Fake, tr HostTransport) *HostDeps {
	return &HostDeps{
		DataDir:   dir,
		Config:    testHostConfig(),
		Clock:     clk,
		IDs:       ids.NewUUIDv7(clk, ids.NewDeterministic(testHostVersion)),
		Transport: tr,
	}
}

// newRuntime builds a bare runtime with no store for tests that exercise
// negotiation, leases and the grace without the host process. The returned
// channel closes when the spawned drain function has returned.
func newRuntime(t *testing.T, cfg HostConfig, clk *clock.Fake, drain DrainFunc) (*HostRuntime, <-chan struct{}) {
	t.Helper()
	if drain == nil {
		drain = func(context.Context, bool, string) error { return nil }
	}
	drained := make(chan struct{})
	rt := &HostRuntime{
		cfg:      cfg,
		started:  clk.Now(),
		clk:      clk,
		leases:   map[string]ClientKind{},
		drained:  drained,
		graceArm: make(chan graceArming),
	}
	ctx := t.Context()
	rt.goTask = func(name string, fn func(context.Context) error) *supervise.Task {
		return supervise.Go(ctx, name, fn)
	}
	rt.drain = drain
	rt.startGraceLoop()
	return rt, drained
}

func TestHelloNegotiatesHighestCommonVersion(t *testing.T) {
	t.Parallel()
	rt, _ := newRuntime(t, testHostConfig(), clock.NewFake(time.Unix(0, 0)), nil)
	rt.cfg.APIVersions = []string{"v1", "v2", "v3"}
	res, err := rt.Hello(t.Context(), HelloParams{
		Kind:                 KindCLI,
		Name:                 "fugue",
		ClientVersion:        "1.0",
		SupportedAPIVersions: []string{"v1", "v3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.APIVersion != "v3" || res.HostVersion != testHostVersion {
		t.Fatalf("Hello = %+v, want v3 from %s", res, testHostVersion)
	}
}

func TestHelloNumericOrderingV10BeatsV2(t *testing.T) {
	t.Parallel()
	rt, _ := newRuntime(t, testHostConfig(), clock.NewFake(time.Unix(0, 0)), nil)
	rt.cfg.APIVersions = []string{"v1", "v2", testV10}
	res, err := rt.Hello(t.Context(), HelloParams{
		Kind:                 KindTUI,
		SupportedAPIVersions: []string{"v2", testV10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.APIVersion != testV10 {
		t.Fatalf("Hello = %q, want %s (numeric, not lexical, ordering)", res.APIVersion, testV10)
	}
}

func TestHelloMalformedVersionIsInvalidArgument(t *testing.T) {
	t.Parallel()
	for _, malformed := range []string{"", "v", "banana", "v-1", "1", "v1.1"} {
		rt, _ := newRuntime(t, testHostConfig(), clock.NewFake(time.Unix(0, 0)), nil)
		_, err := rt.Hello(t.Context(), HelloParams{Kind: KindCLI, SupportedAPIVersions: []string{"v1", malformed}})
		if errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Fatalf("Hello with version %q = %v, want invalid_argument", malformed, err)
		}
	}
}

func TestHelloWithoutCommonVersionFailsTyped(t *testing.T) {
	t.Parallel()
	rt, _ := newRuntime(t, testHostConfig(), clock.NewFake(time.Unix(0, 0)), nil)
	rt.cfg.APIVersions = []string{"v1", "v2"}
	_, err := rt.Hello(t.Context(), HelloParams{
		Kind:                 KindCLI,
		SupportedAPIVersions: []string{"v3", "v4"},
	})
	if !errors.Is(err, ErrNoCommonAPIVersion) || errcode.CodeOf(err) != errcode.UnsupportedVersion {
		t.Fatalf("Hello = %v, want %v with code unsupported_version", err, ErrNoCommonAPIVersion)
	}
	// The public message names the host's versions so the client can tell
	// "host older" from "host newer" (cli §2.3).
	_, msg := errcode.Expose(err)
	if !strings.Contains(msg, "v1") || !strings.Contains(msg, "v2") {
		t.Fatalf("public message %q does not name the host's versions", msg)
	}
}

func TestLeaseZeroArmsGraceShutdown(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	cfg := testHostConfig()
	cfg.IdleGrace = 10 * time.Second
	var reasons []string
	var mu sync.Mutex
	rt, drained := newRuntime(t, cfg, clk, func(_ context.Context, _ bool, reason string) error {
		mu.Lock()
		reasons = append(reasons, reason)
		mu.Unlock()
		return nil
	})
	if err := rt.LeaseAcquired(t.Context(), "conn-1", KindTUI); err != nil {
		t.Fatal(err)
	}
	rt.LeaseReleased(t.Context(), "conn-1")
	if clk.Pending() != 1 {
		t.Fatalf("grace timer not armed: %d pending timers", clk.Pending())
	}
	clk.Advance(10 * time.Second)
	<-drained
	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 1 || reasons[0] != "idle_grace" {
		t.Fatalf("drain reasons = %v, want exactly [idle_grace]", reasons)
	}
}

func TestNewLeaseInsideGraceCancelsShutdown(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	cfg := testHostConfig()
	cfg.IdleGrace = 10 * time.Second
	rec := newScriptedTransport()
	rt, _ := newRuntime(t, cfg, clk, func(ctx context.Context, force bool, _ string) error {
		return rec.Drain(ctx, force)
	})
	if err := rt.LeaseAcquired(t.Context(), "conn-1", KindTUI); err != nil {
		t.Fatal(err)
	}
	rt.LeaseReleased(t.Context(), "conn-1")
	if err := rt.LeaseAcquired(t.Context(), "conn-2", KindCLI); err != nil {
		t.Fatal(err)
	}
	if clk.Pending() != 0 {
		t.Fatalf("grace timer still armed after a new lease: %d pending", clk.Pending())
	}
	clk.Advance(10 * time.Second)
	if got := rec.callCount(); got != 0 {
		t.Fatalf("drain started %d times inside the grace, want 0", got)
	}
}

func TestForegroundRuntimeNeverSelfShutsDown(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	rec := newScriptedTransport()
	rt, _ := newRuntime(t, testHostConfig(), clk, func(ctx context.Context, force bool, _ string) error {
		return rec.Drain(ctx, force)
	})
	if err := rt.LeaseAcquired(t.Context(), "conn-1", KindTUI); err != nil {
		t.Fatal(err)
	}
	rt.LeaseReleased(t.Context(), "conn-1")
	clk.Advance(24 * time.Hour)
	if got := rec.callCount(); got != 0 {
		t.Fatalf("foreground host drained itself: %d calls", got)
	}
}

func TestShutdownEmptyCommandIDIsInvalidArgument(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	rec := newScriptedTransport()
	rt, _ := newRuntime(t, testHostConfig(), clk, func(ctx context.Context, force bool, _ string) error {
		return rec.Drain(ctx, force)
	})
	if err := rt.Shutdown(t.Context(), ShutdownCommand{}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("Shutdown without command_id = %v, want invalid_argument", err)
	}
	if rec.callCount() != 0 || clk.Pending() != 0 {
		t.Fatal("empty command_id started a drain")
	}
}

func TestSecondShutdownWhileDrainingReturnsTheSameReply(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	rec := newScriptedTransport()
	rec.block = make(chan struct{})
	var blockOnce sync.Once
	release := func() { blockOnce.Do(func() { close(rec.block) }) }
	t.Cleanup(release)
	rt, _ := newRuntime(t, testHostConfig(), clk, func(ctx context.Context, force bool, _ string) error {
		return rec.Drain(ctx, force)
	})
	if err := rt.Shutdown(t.Context(), ShutdownCommand{ID: testCommandID}); err != nil {
		t.Fatal(err)
	}
	<-rec.started
	// The reply comes from the draining flag while the transport is blocked.
	if err := rt.Shutdown(t.Context(), ShutdownCommand{ID: "cmd-2"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.callCount(); got != 1 {
		t.Fatalf("drain called %d times, want 1", got)
	}
	release()
	<-rec.done
}

// testStoreConfig builds the config the storage package needs to open a store
// in dir.
func testStoreConfig(dir string) config.Config {
	return config.Config{DataDir: dir}
}

func TestLeaseReleasedDuringDrainDoesNotRearmGrace(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	cfg := testHostConfig()
	cfg.IdleGrace = 10 * time.Second
	rec := newScriptedTransport()
	rec.block = make(chan struct{})
	var blockOnce sync.Once
	release := func() { blockOnce.Do(func() { close(rec.block) }) }
	t.Cleanup(release)
	rt, drained := newRuntime(t, cfg, clk, func(ctx context.Context, force bool, _ string) error {
		return rec.Drain(ctx, force)
	})
	if err := rt.LeaseAcquired(t.Context(), "conn-1", KindTUI); err != nil {
		t.Fatal(err)
	}
	if err := rt.Shutdown(t.Context(), ShutdownCommand{ID: testCommandID}); err != nil {
		t.Fatal(err)
	}
	<-rec.started
	rt.LeaseReleased(t.Context(), "conn-1")
	clk.Advance(10 * time.Second)
	if got := rec.callCount(); got != 1 {
		t.Fatalf("drain called %d times, want 1 (a lease released while draining must not re-arm)", got)
	}
	release()
	<-drained
}

func TestHelloRejectedWhileDraining(t *testing.T) {
	t.Parallel()
	rt, drained, release := newDrainingRuntime(t)
	_, err := rt.Hello(t.Context(), HelloParams{Kind: KindCLI, SupportedAPIVersions: []string{"v1"}})
	if !errors.Is(err, ErrHostStopping) || errcode.CodeOf(err) != errcode.Unavailable {
		t.Fatalf("Hello while draining = %v, want %v", err, ErrHostStopping)
	}
	release()
	<-drained
}

func TestLeaseAcquiredRejectedWhileDraining(t *testing.T) {
	t.Parallel()
	rt, drained, release := newDrainingRuntime(t)
	if err := rt.LeaseAcquired(t.Context(), "conn-9", KindCLI); !errors.Is(err, ErrHostStopping) {
		t.Fatalf("LeaseAcquired while draining = %v, want %v", err, ErrHostStopping)
	}
	release()
	<-drained
}

// newDrainingRuntime starts a drain blocked until release is called, so tests
// can observe the runtime in the draining state.
func newDrainingRuntime(t *testing.T) (*HostRuntime, <-chan struct{}, func()) {
	t.Helper()
	rec := newScriptedTransport()
	rec.block = make(chan struct{})
	rt, drained := newRuntime(t, testHostConfig(), clock.NewFake(time.Unix(0, 0)), func(ctx context.Context, force bool, _ string) error {
		return rec.Drain(ctx, force)
	})
	if err := rt.Shutdown(t.Context(), ShutdownCommand{ID: testCommandID}); err != nil {
		t.Fatal(err)
	}
	<-rec.started
	var once sync.Once
	release := func() { once.Do(func() { close(rec.block) }) }
	t.Cleanup(release)
	return rt, drained, release
}

func TestStatusReportsVersionIncarnationAndCounts(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	hp, err := StartHost(t.Context(), testHostDeps(t.TempDir(), clk, newScriptedTransport()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close the host: %v", closeErr)
		}
	})
	rt := hp.Runtime()
	for connID, kind := range map[string]ClientKind{
		"conn-tui-1": KindTUI, "conn-tui-2": KindTUI, "conn-cli": KindCLI,
		"conn-app": KindApp, "conn-bg": KindAppBackground,
	} {
		if leaseErr := rt.LeaseAcquired(t.Context(), connID, kind); leaseErr != nil {
			t.Fatal(leaseErr)
		}
	}
	view, err := rt.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := ClientCounts{TUI: 2, CLI: 1, App: 1, AppBackground: 1, Total: 5}
	if diff := cmp.Diff(want, view.Clients); diff != "" {
		t.Fatalf("client counts mismatch (-want +got):\n%s", diff)
	}
	if view.HostVersion != testHostVersion {
		t.Fatalf("host version = %q, want %s", view.HostVersion, testHostVersion)
	}
	if diff := cmp.Diff([]string{"v1", "v2"}, view.SupportedAPIVersions); diff != "" {
		t.Fatalf("supported versions mismatch (-want +got):\n%s", diff)
	}
	if view.Incarnation.IncarnationID == "" || view.Incarnation.LockGeneration == 0 {
		t.Fatalf("status carries an incomplete incarnation: %+v", view.Incarnation)
	}
	if !view.StartedAt.Equal(clk.Now()) {
		t.Fatalf("started_at = %v, want the clock time %v", view.StartedAt, clk.Now())
	}
}

func TestStartHostDrainsThroughStoreCloseLast(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	tr := newScriptedTransport()
	tr.block = make(chan struct{})
	dir := t.TempDir()
	hp, err := StartHost(t.Context(), testHostDeps(dir, clk, tr))
	if err != nil {
		t.Fatal(err)
	}
	hp.Stop("test")
	<-tr.started
	// While the transport is still draining, the store and its lock are open.
	if _, probeErr := storage.OpenStore(t.Context(), testStoreConfig(dir), clk, ids.NewUUIDv7(clk, ids.NewDeterministic("probe"))); probeErr == nil {
		t.Fatal("a second store opened while the transport was still draining")
	}
	close(tr.block)
	<-hp.DrainDone()
	// The lock is released only by the store close at the end of the drain.
	again, openErr := storage.OpenStore(t.Context(), testStoreConfig(dir), clk, ids.NewUUIDv7(clk, ids.NewDeterministic("again")))
	if openErr != nil {
		t.Fatalf("the lock was not released after the drain: %v", openErr)
	}
	if closeErr := again.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestStartHostRejectsMalformedConfiguredVersions(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	deps := testHostDeps(t.TempDir(), clk, newScriptedTransport())
	deps.Config.APIVersions = []string{"v1", "banana"}
	if _, err := StartHost(t.Context(), deps); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("StartHost with a malformed configured version = %v, want invalid_argument", err)
	}
}

func TestDrainDeadlineComesFromTheClock(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	tr := newScriptedTransport()
	tr.block = make(chan struct{})
	t.Cleanup(func() { close(tr.block) })
	deps := testHostDeps(t.TempDir(), clk, tr)
	deps.Config.DrainTimeout = 5 * time.Second
	hp, err := StartHost(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	hp.Stop("test")
	<-tr.started
	clk.Advance(5 * time.Second)
	<-hp.DrainDone()
	tr.mu.Lock()
	saw := errors.Is(tr.lastErr, context.Canceled)
	tr.mu.Unlock()
	if !saw {
		t.Fatal("the drain deadline did not cancel the transport through the clock timer")
	}
	// Close reports the drain's outcome: the deadline cancellation.
	if err := hp.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close = %v, want the deadline cancellation", err)
	}
}

func TestHostProcessCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	hp, err := StartHost(t.Context(), testHostDeps(t.TempDir(), clk, newScriptedTransport()))
	if err != nil {
		t.Fatal(err)
	}
	if err := hp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := hp.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

// assertStaleFire checks that a fire from armed, a timer that is no longer
// the armed one of a draining runtime, starts nothing.
func assertStaleFire(t *testing.T, rt *HostRuntime, armed clock.Timer, drains chan string) {
	t.Helper()
	if rt.fireGrace(armed) {
		t.Fatal("a fire drained an already draining host")
	}
	select {
	case reason := <-drains:
		t.Fatalf("a second drain started with reason %q", reason)
	default:
	}
}

// TestStaleGraceFireDoesNotDrain pins the grace fire path: a fire only drains
// when it comes from the timer that is still armed, no lease is held and the
// runtime is not already draining.
func TestStaleGraceFireDoesNotDrain(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	cfg := testHostConfig()
	cfg.IdleGrace = 10 * time.Second
	drains := make(chan string, 4)
	rt, _ := newRuntime(t, cfg, clk, func(_ context.Context, _ bool, reason string) error {
		drains <- reason
		return nil
	})
	if err := rt.LeaseAcquired(t.Context(), "c1", KindCLI); err != nil {
		t.Fatalf("lease c1 = %v", err)
	}
	rt.LeaseReleased(t.Context(), "c1")

	// A fire from a foreign timer is stale.
	if rt.fireGrace(clk.NewTimer(10 * time.Second)) {
		t.Fatal("a foreign timer drained the host")
	}
	select {
	case reason := <-drains:
		t.Fatalf("a stale fire drained with reason %q", reason)
	default:
	}

	// An armed-but-disarmed timer is stale, and so is a fire while draining.
	if err := rt.LeaseAcquired(t.Context(), "c2", KindCLI); err != nil {
		t.Fatalf("lease c2 = %v", err)
	}
	rt.mu.Lock()
	armed := rt.graceTimer
	rt.mu.Unlock()
	rt.beginDrain(false, "shutdown")
	assertStaleFire(t, rt, armed, drains)

	// The live fire still drains exactly once, with the grace reason, on a
	// runtime of its own so the first runtime's slow drain cannot leak in.
	liveDrains := make(chan string, 4)
	rt2, _ := newRuntime(t, cfg, clk, func(_ context.Context, _ bool, reason string) error {
		liveDrains <- reason
		return nil
	})
	if err := rt2.LeaseAcquired(t.Context(), "c3", KindCLI); err != nil {
		t.Fatalf("lease c3 = %v", err)
	}
	rt2.LeaseReleased(t.Context(), "c3")
	rt2.mu.Lock()
	live := rt2.graceTimer
	rt2.mu.Unlock()
	if !rt2.fireGrace(live) {
		t.Fatal("the live grace fire did not drain")
	}
	if reason := <-liveDrains; reason != "idle_grace" {
		t.Fatalf("drain reason = %q, want idle_grace", reason)
	}
	select {
	case reason := <-liveDrains:
		t.Fatalf("an extra drain started with reason %q", reason)
	default:
	}
}

// TestGraceRearmDoesNotGrowTasks pins the bounded task set: many lease cycles
// re-arm the grace without spawning a new supervised task each time, so the
// host process holds only the one long-lived grace loop.
func TestGraceRearmDoesNotGrowTasks(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	dir := t.TempDir()
	deps := testHostDeps(dir, clk, newScriptedTransport())
	deps.Config.IdleGrace = 10 * time.Second
	hp, err := StartHost(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := hp.Close(); err != nil {
			t.Errorf("close the host: %v", err)
		}
	})
	rt := hp.Runtime()
	for i := range 50 {
		id := strconv.Itoa(i)
		if err := rt.LeaseAcquired(t.Context(), id, KindCLI); err != nil {
			t.Fatalf("lease %d = %v", i, err)
		}
		rt.LeaseReleased(t.Context(), id)
	}
	hp.tasksMu.Lock()
	tasks := append([]*supervise.Task(nil), hp.tasks...)
	hp.tasksMu.Unlock()
	if len(tasks) != 1 {
		names := make([]string, 0, len(tasks))
		for _, task := range tasks {
			names = append(names, task.Name())
		}
		t.Fatalf("supervised tasks after 50 lease cycles = %d (%v), want only the grace loop", len(tasks), names)
	}
	if name := tasks[0].Name(); name != "app.host-grace" {
		t.Fatalf("the surviving task is %q, want app.host-grace", name)
	}
}
