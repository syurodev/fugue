package hostconn

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/pkg/client"
)

const binPointerName = "fugue-f10.2a-bin.path"

// TestMain builds the fugue binary from the core module's cmd/fugue — once
// for the whole package, before any test runs — and checks the package for
// leaked goroutines. The evidence test needs a real host.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fugue-f10.2a-bin")
	if err != nil {
		fmt.Fprintf(os.Stderr, "fugue-e04 harness: %v\n", err)
		os.Exit(1)
	}
	// go install with GOBIN keeps every command argument literal; the binary
	// lands at <dir>/fugue.
	cmd := exec.CommandContext(context.Background(), "go", "install", "./cmd/fugue")
	cmd.Dir = filepath.Join("..", "..", "..")
	cmd.Env = append(os.Environ(), "GOBIN="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build fugue: %v: %s\n", err, out)
		removeBuildDir(dir)
		os.Exit(1)
	}
	binPath := filepath.Join(dir, "fugue")
	pointerFile := filepath.Join(os.TempDir(), binPointerName)
	if writeErr := os.WriteFile(pointerFile, []byte(binPath), 0o600); writeErr != nil {
		fmt.Fprintf(os.Stderr, "write bin path pointer: %v\n", writeErr)
		removeBuildDir(dir)
		os.Exit(1)
	}
	code := m.Run()
	removeBuildDir(dir)
	if code == 0 {
		if err := goleak.Find(); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			os.Exit(1)
		}
	}
	os.Exit(code)
}

// removeBuildDir drops the harness's build directory and pointer file; nothing
// can act on the error this late.
func removeBuildDir(dir string) {
	pointerFile := filepath.Join(os.TempDir(), binPointerName)
	if err := os.Remove(pointerFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "remove the bin pointer: %v\n", err)
	}
	if rmErr := os.RemoveAll(dir); rmErr != nil {
		fmt.Fprintf(os.Stderr, "remove the build dir: %v\n", rmErr)
	}
}

// buildFugue returns the binary TestMain built; a failed build is fatal for
// the calling test.
func buildFugue(t *testing.T) string {
	t.Helper()
	pointerFile := filepath.Join(os.TempDir(), binPointerName)
	data, err := os.ReadFile(filepath.Clean(pointerFile))
	if err != nil {
		t.Fatalf("the host binary was not built in TestMain: %v", err)
	}
	return string(data)
}

// testHost is one running fugue serve process with its data directory.
type testHost struct {
	cmd     *exec.Cmd
	dataDir string
}

// startServe runs `fugue serve` on a fresh data directory with short runtime
// paths, so the socket path fits sun_path on macOS. It returns at once: the
// manager's own retry loop is what waits for readiness, which is exactly the
// behavior the app has.
func startServe(t *testing.T, ctx context.Context, bin string) *testHost {
	t.Helper()
	base := shortDir(t)
	dataDir := filepath.Join(base, "data")
	runtime := filepath.Join(base, "rt")
	for _, dir := range []string{dataDir, runtime} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Env = append(os.Environ(),
		"SYNX_FUGUE_DATA_DIR="+dataDir,
		"XDG_RUNTIME_DIR="+runtime,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			if kerr := cmd.Process.Kill(); kerr != nil {
				return
			}
		}
		// Wait reaps the process and ends exec's watch goroutine, even after
		// a kill.
		if werr := cmd.Wait(); werr != nil {
			return
		}
	})
	return &testHost{cmd: cmd, dataDir: dataDir}
}

// stopServe drains the host with SIGTERM and waits for the exit, bounded by
// ctx so a hung drain fails the test's assertions rather than hanging the run.
func (h *testHost) stopServe(t *testing.T, ctx context.Context) {
	t.Helper()
	if h.cmd.ProcessState != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// cmd.Wait reaps the process and ends exec's watch goroutine.
		if werr := h.cmd.Wait(); werr != nil {
			return
		}
	}()
	if err := h.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("signal the host: %v", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		if kerr := h.cmd.Process.Kill(); kerr != nil {
			t.Logf("kill host: %v", kerr)
		}
		<-done
		t.Errorf("the host did not exit before deadline: %v", ctx.Err())
	}
}

// socketOf reads the socket path from the host's discovery file.
func socketOf(t *testing.T, h *testHost) string {
	t.Helper()
	hf, err := client.ReadHostFile(h.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return hf.SocketPath
}

// dropConn tears the current stream set down, which is what a network drop
// does to the leased connection: the run loop sees the streams end, drops the
// client, and reconnects with a fresh Hello. It exits through the same
// cancellation a transport failure exits through.
func (m *Manager) dropConn() {
	m.mu.Lock()
	cancel := m.streamsCancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// waitForState drives the manager's fake-clock retry loop until it reports
// the wanted state: every armed backoff is a failed attempt, so the loop
// fires the clock again; a state change means the manager moved on.
func waitForState(t *testing.T, ctx context.Context, m *Manager, clock *fakeClock, upd *collector, want State) {
	t.Helper()
	for {
		if s, _ := m.State(); s == want {
			return
		}
		clock.fireAll()
		select {
		case <-clock.armed:
			// The attempt failed and armed the next backoff; fire again.
		case <-upd.changed:
			// The state moved; re-check it on top of the loop.
		case <-ctx.Done():
			t.Fatalf("the manager never reached %q: %v", want, ctx.Err())
		}
	}
}

// isEvent matches one delivered event of the topic.
func isEvent(topic string, f func(*client.Event) bool) func(Update) bool {
	return func(u Update) bool { return u.Topic == topic && u.Event != nil && f(u.Event) }
}

// isSnapshot matches one delivered snapshot of the topic.
func isSnapshot(topic string) func(Update) bool {
	return func(u Update) bool { return u.Topic == topic && u.Snapshot != nil }
}

// TestAppReconnectsAndCatchesUpFromSequence is DA-E04 (desktop-app §10): the
// app shows the connection state, reconnects on its own, catches up from its
// cursor without a duplicate or a gap, and reloads the snapshot when the host
// returns with a new incarnation. It runs against a real `fugue serve`.
func TestAppReconnectsAndCatchesUpFromSequence(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	bin := buildFugue(t)
	host := startServe(t, ctx, bin)
	clock := newFakeClock()
	m, upd := newTestManager(t, host.dataDir, clock, "host")
	m.Start(ctx)

	snap := catchUpAfterDrop(t, ctx, m, clock, upd, host)
	reconnectAfterRestart(t, ctx, m, clock, upd, host, bin, snap.Incarnation)
}

// catchUpAfterDrop covers the dropped connection: the manager reconnects to
// the same host, catches up from its cursor with no duplicate and no gap, and
// reloads no snapshot. It returns the first snapshot.
func catchUpAfterDrop(t *testing.T, ctx context.Context, m *Manager, clock *fakeClock, upd *collector, host *testHost) *client.Snapshot {
	t.Helper()
	// The host is not up yet on the first attempts; the retry loop waits for
	// it, which is the "host not running" state the status bar shows.
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateDisconnected && u.Reason == reasonHostNotRunning })
	waitForState(t, ctx, m, clock, upd, StateConnected)
	snapUpd, _ := upd.find(t, ctx, 0, isSnapshot("host"))
	if snapUpd.Snapshot.Incarnation == "" {
		t.Fatalf("first snapshot = %+v, want an incarnation", snapUpd.Snapshot)
	}

	// Drop the connection with the same host: the state goes disconnected,
	// then connected again.
	m.dropConn()
	upd.find(t, ctx, 0, func(u Update) bool { return u.State == StateDisconnected && u.Reason == "connection lost" })
	mark := upd.offset()

	// While the manager is offline, a second local client holds the lease:
	// its hello and its release publish on the host topic, and the manager
	// must catch up to them from its cursor.
	second, err := client.Dial(ctx, socketOf(t, host), client.Params{
		Kind: client.KindApp, Name: "fugue-e04-second", Version: "0", APIVersions: []string{"v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.HostStatus(ctx); err != nil {
		t.Fatal(err)
	}
	second.Close()

	// Reconnect on the injected clock and collect the catch-up.
	waitForState(t, ctx, m, clock, upd, StateConnected)
	// Four events follow the drop, in order: the dropped lease's release
	// (1->0), the second client's hello (0->1, published while the manager
	// was offline), its release (1->0), and the manager's own hello (0->1).
	lastWant := snapUpd.Snapshot.Sequence + 4
	upd.find(t, ctx, mark, isEvent("host", func(ev *client.Event) bool { return ev.Sequence == lastWant }))
	// The reconnect must not have reloaded a snapshot: the cursor was valid.
	for _, u := range upd.history()[mark:] {
		if u.Snapshot != nil {
			t.Fatalf("the reconnect reloaded a snapshot %+v: the cursor was not used for catch-up", u.Snapshot)
		}
	}
	assertContiguous(t, upd.history(), snapUpd.Snapshot.Sequence, "before the restart")
	return snapUpd.Snapshot
}

// reconnectAfterRestart covers the host restart: the drained host is started
// again on the same data directory, and the manager reconnects into a new
// incarnation with a fresh snapshot and live events after it.
func reconnectAfterRestart(t *testing.T, ctx context.Context, m *Manager, clock *fakeClock, upd *collector, host *testHost, bin, firstIncarnation string) {
	t.Helper()
	host.stopServe(t, ctx)
	waitForState(t, ctx, m, clock, upd, StateDisconnected)
	host2 := startServe(t, ctx, bin)
	waitForState(t, ctx, m, clock, upd, StateConnected)
	snap2Upd, snap2Next := upd.find(t, ctx, upd.offset()-1, isSnapshot("host"))
	if snap2Upd.Snapshot.Incarnation == firstIncarnation {
		t.Fatalf("snapshot after restart = %+v, want a new incarnation (was %s)",
			snap2Upd.Snapshot, firstIncarnation)
	}
	// The state showed disconnected between the drain and the new host.
	sawDisconnected, sawConnected := false, false
	for _, u := range upd.history() {
		if u.State == StateDisconnected {
			sawDisconnected = true
		}
		if u.State == StateConnected && sawDisconnected {
			sawConnected = true
			break
		}
	}
	if !sawDisconnected || !sawConnected {
		t.Fatalf("the restart never showed disconnected then connected (saw %v then %v)",
			sawDisconnected, sawConnected)
	}
	// A third client now connects and leaves, so the restarted stream shows
	// live events after the fresh snapshot — its hello and its release — and
	// the sequences stay contiguous within the new incarnation.
	third, err := client.Dial(ctx, socketOf(t, host2), client.Params{
		Kind: client.KindApp, Name: "fugue-e04-third", Version: "0", APIVersions: []string{"v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	third.Close()
	upd.find(t, ctx, snap2Next, isEvent("host", func(ev *client.Event) bool {
		return ev.Sequence == snap2Upd.Snapshot.Sequence+2
	}))
	// From the fresh snapshot on, the slice holds only this incarnation's
	// events, and they advance by exactly one.
	assertContiguous(t, upd.history()[snap2Next-1:], snap2Upd.Snapshot.Sequence, "after the restart")
}

// assertContiguous pins "no duplicate and no gap": every event in updates
// after the snapshot with firstSequence advances the topic's sequence by
// exactly one. The topic is the host topic; snapshots appear only where a
// reload was due.
func assertContiguous(t *testing.T, updates []Update, firstSequence uint64, phase string) {
	t.Helper()
	want := firstSequence + 1
	seen := map[uint64]bool{}
	for _, u := range updates {
		if u.Snapshot != nil {
			continue
		}
		if u.Event == nil {
			continue
		}
		if u.Event.Sequence != want {
			t.Fatalf("%s: event sequence %d at position %d, want %d (snapshot %d): duplicate or gap",
				phase, u.Event.Sequence, len(seen), want, firstSequence)
		}
		if seen[u.Event.Sequence] {
			t.Fatalf("%s: event sequence %d delivered twice", phase, u.Event.Sequence)
		}
		seen[u.Event.Sequence] = true
		want++
	}
	if len(seen) == 0 {
		t.Fatalf("%s: no events were delivered after the snapshot", phase)
	}
}
