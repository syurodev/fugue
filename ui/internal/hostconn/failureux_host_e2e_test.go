package hostconn

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/session"
)

// A-E05 (activation §3, §5): the failure UX rows of a host that cannot start,
// over a real `fugue serve`. A data directory another process holds refuses a
// second host typed (`failed_precondition`, the in-use exit code), and the
// app's connection manager turns that refusal into the stable reason the
// window renders as its execution-store banner. A host the app cannot speak
// to, and a schema this Fugue cannot serve, reach the manager the same way.

// heldDataDir prepares a data directory whose lock another party holds, with
// short runtime paths so the socket fits sun_path.
func heldDataDir(t *testing.T) (dataDir, runtime string) {
	t.Helper()
	base := shortDir(t)
	dataDir = filepath.Join(base, "data")
	runtime = filepath.Join(base, "rt")
	if err := os.Mkdir(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	holdDataDirLock(t, dataDir)
	return dataDir, runtime
}

// TestFailureUXSecondHostOnAHeldDataDirFailsTypedOverRealHost is the held
// data directory row at the process level: a real `fugue serve` on a data
// directory another process holds exits with the in-use code and logs the
// typed failed_precondition, and leaves no discovery file behind.
func TestFailureUXSecondHostOnAHeldDataDirFailsTypedOverRealHost(t *testing.T) {
	t.Parallel()
	bin := buildFugue(t)
	ctx := testCtx(t)
	dataDir, runtime := heldDataDir(t)

	out, err := runServeToExit(ctx, bin, dataDir, runtime)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != client.ExitDataDirInUse {
		t.Fatalf("second host exit = %v, want exit code %d", err, client.ExitDataDirInUse)
	}
	if !strings.Contains(out, "failed_precondition") || !strings.Contains(out, "already locked") {
		t.Fatalf("second host log = %q, want the typed failed_precondition lock failure", out)
	}
	if _, readErr := client.ReadHostFile(dataDir); readErr == nil {
		t.Fatal("the refused host left a discovery file")
	}
}

// runServeToExit runs `fugue serve` in the foreground until it exits and
// returns its exit error and its stderr.
func runServeToExit(ctx context.Context, bin, dataDir, runtime string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Env = append(os.Environ(), "SYNX_FUGUE_DATA_DIR="+dataDir, "XDG_RUNTIME_DIR="+runtime)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

// TestFailureUXManagerReportsAHeldDataDirOverRealHost is the same row from
// the app's side: the manager launches the real host, which is refused, and
// the manager freezes disconnected with the data-directory-in-use reason the
// window's banner is chosen by, never the generic lost-connection text.
func TestFailureUXManagerReportsAHeldDataDirOverRealHost(t *testing.T) {
	t.Parallel()
	appPath, _ := lifecycleFixture(t)
	ctx := testCtx(t)
	dataDir, _ := heldDataDir(t)
	m, col := startRefusalManager(ctx, dataDir, appPath)

	col.waitFor(t, ctx, func(u Update) bool {
		return u.State == StateDisconnected && u.Reason == ReasonDataDirInUse
	})
	if st, reason := m.State(); st != StateDisconnected || reason != ReasonDataDirInUse {
		t.Fatalf("manager state = %q (%q), want disconnected for the held data directory", st, reason)
	}
	m.Stop()
	if successes := waitLifecycleChildren(t, ctx, []*Manager{m}); successes != 0 {
		t.Fatalf("successful hosts = %d, want the launched host refused", successes)
	}
	col.stop()
	if m.DataDir() != dataDir {
		t.Fatalf("manager data dir = %q, want %q for the banner to name", m.DataDir(), dataDir)
	}
}

// TestFailureUXManagerReportsAnUnsupportedSchemaHost is the unsupported
// schema row from the app's side: a launched host that exits with the
// unsupported-store code ends the manager disconnected with its own reason,
// not the data-directory one. The host's refusal itself is pinned over a real
// newer-schema store in cmd/fugue.
func TestFailureUXManagerReportsAnUnsupportedSchemaHost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	appPath := filepath.Join(dir, "fugue-app")
	testExecutable(t, appPath, []byte("#!/bin/sh\n/bin/cat >&3\n"))
	testExecutable(t, filepath.Join(dir, "fugue"), []byte("#!/bin/sh\nexit "+strconv.Itoa(client.ExitStoreUnsupported)+"\n"))
	ctx := testCtx(t)
	m, col := startRefusalManager(ctx, filepath.Join(shortDir(t), "data"), appPath)

	col.waitFor(t, ctx, func(u Update) bool {
		return u.State == StateDisconnected && u.Reason == ReasonStoreUnsupported
	})
	m.Stop()
	if successes := waitLifecycleChildren(t, ctx, []*Manager{m}); successes != 0 {
		t.Fatalf("successful hosts = %d, want the launched host refused", successes)
	}
	col.stop()
}

// TestFailureUXManagerReportsAHostWithNoCommonProtocolOverRealHost is the
// unsupported protocol row: an app that speaks only a Host API version the
// real host does not serve is refused at Hello with unsupported_version, and
// the manager freezes incompatible with the reason naming the host's
// versions, which the window shows next to the app's own.
func TestFailureUXManagerReportsAHostWithNoCommonProtocolOverRealHost(t *testing.T) {
	t.Parallel()
	bin := buildFugue(t)
	ctx := testCtx(t)
	host := startServe(t, ctx, bin)
	waitHostReady(t, ctx, host)
	col := newCollector(128)
	col.start()
	params := appParams()
	params.APIVersions = []string{"v99"}
	m := NewManager(&Config{DataDir: host.dataDir, Params: params, Topics: []string{testTopicHost}, Updates: col.ch})
	m.Start(ctx)
	defer col.stop()
	defer m.Stop()

	u := col.waitFor(t, ctx, func(u Update) bool { return u.State == StateIncompatible })
	if !strings.Contains(u.Reason, "v1") {
		t.Fatalf("incompatible reason = %q, want the host's supported version v1", u.Reason)
	}
	if got := m.AppParams().APIVersions; len(got) != 1 || got[0] != "v99" {
		t.Fatalf("app params = %v, want the app's own v99 for the banner", got)
	}
}

// startRefusalManager runs an auto-starting manager whose updates a collector
// keeps.
func startRefusalManager(ctx context.Context, dataDir, appPath string) (*Manager, *collector) {
	col := newCollector(128)
	col.start()
	m := NewManager(&Config{DataDir: dataDir, Params: lifecycleParams(), AutoStart: true, ExecutablePath: appPath, Topics: []string{testTopicHost}, Updates: col.ch})
	m.Start(ctx)
	return m, col
}

// TestFailureUXUnprovedStopSurfacesOnTheTranscriptOverRealHost is the Stage A
// equivalent of the hung-worker row: closing a session whose process tree the
// host cannot prove empty (the serve runs without a systemd user scope, so the
// stop proof is `unknown`) ends the session `closed` and reports the unproved
// stop on the transcript topic, which the app's read model turns into the
// restart-the-host guidance.
func TestFailureUXUnprovedStopSurfacesOnTheTranscriptOverRealHost(t *testing.T) {
	t.Parallel()
	bin := buildFugue(t)
	ctx := testCtx(t)
	host := startServeWithFakeVendor(t, ctx, bin, fakeVendorDir(t, fakeVendorVersion, true))
	c := dialApp(t, ctx, host)
	wsID := unlockAndSeedWorkspace(t, ctx, c)
	started, err := c.StartSession(ctx, "e05-unproved-start", client.VendorClaude, wsID, "", client.SessionProfile{})
	if err != nil {
		t.Fatalf("start the session: %v", err)
	}
	waitSessionState(t, ctx, c, started.ID, stateE2EReady)

	stream, err := c.Subscribe(ctx, "transcript:"+started.ID, nil)
	if err != nil {
		t.Fatalf("subscribe to the transcript: %v", err)
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			t.Logf("close the transcript stream: %v", closeErr)
		}
	}()
	tr := session.NewTranscript(started.ID)
	applyNextTranscriptMessage(t, stream, tr)
	if tr.StopUnproved() {
		t.Fatal("a live session reads as an unproved stop")
	}

	if done, closeErr := c.CloseSession(ctx, "e05-unproved-close", started.ID); closeErr != nil || !done {
		t.Fatalf("close the session: done %v, err %v", done, closeErr)
	}
	waitSessionState(t, ctx, c, started.ID, stateE2EClosed)
	for !tr.StopUnproved() {
		applyNextTranscriptMessage(t, stream, tr)
	}
}

// applyNextTranscriptMessage folds the stream's next message into the model:
// the stream returns an error when the test's deadline ends it.
func applyNextTranscriptMessage(t *testing.T, stream *client.Stream, tr *session.Transcript) {
	t.Helper()
	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive a transcript message: %v", err)
	}
	switch {
	case msg.Snapshot != nil:
		if applyErr := tr.ApplySnapshot(msg.Snapshot.State); applyErr != nil {
			t.Fatal(applyErr)
		}
	case msg.Event != nil:
		if applyErr := tr.ApplyEvent(msg.Event.Type, msg.Event.Sequence, msg.Event.Payload); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
}
