package app

// VR-E14 / git-guard GG-E01..GG-E03: every vendor turn is snapshotted at its
// start and end in the shadow repository and records one turn_diff item.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/snapshot"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// userGitBinary is the git the tests run on the user's own repository.
const userGitBinary = "git"

type turnDiffHarness struct {
	t       *testing.T
	f       *transcriptFixture
	session *actorTestSession
	manager *VendorSessions
	turns   int
}

func newTurnDiffHarness(t *testing.T, workDir string, port TurnSnapshotter) *turnDiffHarness {
	t.Helper()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"), TurnSnapshots: port,
	})
	info := &vendorstore.Session{ID: testActorSessionID, Vendor: testVendorName, AccountID: testAccountID, Role: testActorRole}
	if err := manager.Start(t.Context(), info, &vendor.StartSpec{SessionID: testActorSessionID, Cwd: workDir}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// t.Context() is already cancelled when cleanups run.
		if err := manager.CloseAll(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return &turnDiffHarness{t: t, f: f, session: session, manager: manager}
}

func realTurnSnapshots(t *testing.T) *engineTurnSnapshots {
	t.Helper()
	return realTurnSnapshotsWith(t, snapshot.DefaultLimits())
}

func realTurnSnapshotsWith(t *testing.T, limits snapshot.Limits) *engineTurnSnapshots {
	t.Helper()
	git := snapshot.Locate(t.Context())
	if !git.OK() {
		t.Fatalf("git is required for the turn snapshot tests: %s", git.Reason)
	}
	return newEngineTurnSnapshots(t.TempDir(), git, limits, testutil.OSClock(), ids.NewSequence("snap-"))
}

// runTurn sends one turn, runs step while the turn is open (the fake vendor's
// shell step: no file_change item is reported) and ends the turn.
func (h *turnDiffHarness) runTurn(step func()) {
	h.t.Helper()
	h.turns++
	h.session.turnID = fmt.Sprintf("turn-%d", h.turns)
	mustSendActorInput(h.t, h.manager, "go")
	if step != nil {
		step()
	}
	h.session.observations <- claude.Observation{TurnEnd: &claude.TurnEnd{TurnID: h.session.turnID, Outcome: claude.OutcomeCompleted}}
	awaitActor(h.t, func() bool {
		snap, err := h.f.store.Snapshot(h.t.Context(), testActorSessionID)
		return err == nil && snap.State == vendorstore.StateReady && len(h.turnDiffs()) == h.turns
	})
}

func (h *turnDiffHarness) turnDiffs() []TranscriptEntry {
	h.t.Helper()
	entries, err := h.f.service.TranscriptPage(h.t.Context(), TranscriptPageParams{Session: testActorSessionID, Limit: 500})
	if err != nil {
		h.t.Fatal(err)
	}
	var out []TranscriptEntry
	for i := range entries {
		if entries[i].Kind == transcript.TurnDiff {
			out = append(out, entries[i])
		}
	}
	return out
}

func (h *turnDiffHarness) content(entry *TranscriptEntry) transcript.TurnDiffContent {
	h.t.Helper()
	detail, err := h.f.service.TranscriptItem(h.t.Context(), testActorSessionID, entry.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	var content transcript.TurnDiffContent
	if len(detail.Content) > 0 {
		if err := json.Unmarshal(detail.Content, &content); err != nil {
			h.t.Fatal(err)
		}
	}
	return content
}

func writeTestFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestVRE14GGE01ShellChangeAppearsInTurnDiff: a file the vendor edits through
// a shell step, with no file_change reported, is in the turn's diff, and the
// path lives only in the sealed payload, never in the metadata.
func TestVRE14GGE01ShellChangeAppearsInTurnDiff(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n")
	writeTestFile(t, filepath.Join(dir, "keep.txt"), "same\n")
	h := newTurnDiffHarness(t, dir, realTurnSnapshots(t))
	h.runTurn(func() {
		writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n// formatted\n")
		writeTestFile(t, filepath.Join(dir, "new.txt"), "hello\n")
		if err := os.Remove(filepath.Join(dir, "keep.txt")); err != nil {
			t.Fatal(err)
		}
	})
	entries := h.turnDiffs()
	if len(entries) != 1 {
		t.Fatalf("turn_diff items = %d, want 1", len(entries))
	}
	want := vendorstore.ItemMetadata{
		Status: transcript.TurnDiffComplete, FilesAdded: 1, FilesModified: 1, FilesDeleted: 1,
		LinesAdded: 2, LinesRemoved: 1,
	}
	if diff := cmp.Diff(want, entries[0].Metadata); diff != "" {
		t.Fatalf("turn_diff metadata mismatch (-want +got):\n%s", diff)
	}
	got := map[string]string{}
	content := h.content(&entries[0])
	for _, file := range content.Files {
		got[file.Path] = file.Change
	}
	if diff := cmp.Diff(map[string]string{"main.go": "modify", "new.txt": "add", "keep.txt": "delete"}, got); diff != "" {
		t.Fatalf("turn_diff files mismatch (-want +got):\n%s", diff)
	}
	encoded, err := json.Marshal(entries[0].Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "main.go") {
		t.Fatal("a path reached the item metadata")
	}
}

// hashGitDir digests every file under dir, so any change to refs, index,
// objects or config shows.
func hashGitDir(t *testing.T, dir string) string {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	sum := sha256.New()
	fsys := root.FS()
	walkErr := fs.WalkDir(fsys, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		sum.Write([]byte(path + "\x00"))
		if !entry.IsDir() {
			data, readErr := fs.ReadFile(fsys, path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			sum.Write(data)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func runUserGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), userGitBinary)
	cmd.Args = append([]string{userGitBinary, "-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestVRE14GGE02UserGitUntouchedAndNoGitDirectoryWorks: many turns leave the
// user's .git byte-identical, and a directory without .git still gets diffs.
func TestVRE14GGE02UserGitUntouchedAndNoGitDirectoryWorks(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeTestFile(t, filepath.Join(repo, "a.txt"), "one\n")
	runUserGit(t, repo, "init", "-q")
	runUserGit(t, repo, "add", "a.txt")
	runUserGit(t, repo, "commit", "-q", "-m", "init")
	before := hashGitDir(t, filepath.Join(repo, ".git"))
	h := newTurnDiffHarness(t, repo, realTurnSnapshots(t))
	for i := range 5 {
		h.runTurn(func() { writeTestFile(t, filepath.Join(repo, "a.txt"), fmt.Sprintf("edit %d\n", i)) })
	}
	if after := hashGitDir(t, filepath.Join(repo, ".git")); after != before {
		t.Fatal("the user's .git changed after five turns")
	}
	for _, entry := range h.turnDiffs() {
		if entry.Metadata.Status != transcript.TurnDiffComplete || entry.Metadata.FilesModified != 1 {
			t.Fatalf("turn_diff metadata = %+v, want one modified file", entry.Metadata)
		}
	}

	plain := t.TempDir()
	if _, err := os.Stat(filepath.Join(plain, ".git")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("test directory unexpectedly has .git: %v", err)
	}
	hp := newTurnDiffHarness(t, plain, realTurnSnapshots(t))
	hp.runTurn(func() { writeTestFile(t, filepath.Join(plain, "b.txt"), "x\n") })
	entries := hp.turnDiffs()
	if len(entries) != 1 || entries[0].Metadata.Status != transcript.TurnDiffComplete || entries[0].Metadata.FilesAdded != 1 {
		t.Fatalf("no-.git turn_diff = %+v, want one added file", entries)
	}
}

// noopSnapshotLifecycle is the warm-up and release half of a fake snapshotter.
type noopSnapshotLifecycle struct{}

func (noopSnapshotLifecycle) Warm(context.Context, string) error                   { return nil }
func (noopSnapshotLifecycle) Release(context.Context, TurnSnapshot) error          { return nil }
func (noopSnapshotLifecycle) ReleaseSession(context.Context, string, string) error { return nil }

// stalledSnapshots blocks Begin until its context ends, like a git that hangs.
type stalledSnapshots struct {
	noopSnapshotLifecycle
	entered   chan struct{}
	cancelled atomic.Bool
	endErr    error
}

func (s *stalledSnapshots) Begin(ctx context.Context, _, _ string) (TurnSnapshot, error) {
	close(s.entered)
	<-ctx.Done()
	s.cancelled.Store(true)
	return TurnSnapshot{}, fmt.Errorf("stalled begin: %w", ctx.Err())
}

func (s *stalledSnapshots) End(context.Context, TurnSnapshot) (TurnChanges, error) {
	return TurnChanges{}, s.endErr
}

// failingEnd opens normally and fails at the end of the turn.
type failingEnd struct{ noopSnapshotLifecycle }

func (failingEnd) Begin(_ context.Context, _, workDir string) (TurnSnapshot, error) {
	return TurnSnapshot{ID: "snap", WorkDir: workDir}, nil
}

var errInjectedSnapshot = errors.New("injected snapshot failure")

func (failingEnd) End(context.Context, TurnSnapshot) (TurnChanges, error) {
	return TurnChanges{}, errInjectedSnapshot
}

func (h *turnDiffHarness) requireUnavailableAndReady() {
	h.t.Helper()
	entries := h.turnDiffs()
	if len(entries) != 1 || entries[0].Metadata.Status != transcript.TurnDiffUnavailable {
		h.t.Fatalf("turn_diff items = %+v, want one unavailable", entries)
	}
	if c := h.content(&entries[0]); len(c.Files) != 0 {
		h.t.Fatalf("unavailable turn_diff carries files: %+v", c)
	}
}

// TestVRE14GGE03SnapshotTimeoutIsUnavailableAndTurnCompletes: a snapshot that
// outlives its 10 s cap on the fake clock is cancelled, the item says
// unavailable and the turn still runs to its end.
func TestVRE14GGE03SnapshotTimeoutIsUnavailableAndTurnCompletes(t *testing.T) {
	t.Parallel()
	port := &stalledSnapshots{entered: make(chan struct{}), endErr: errInjectedSnapshot}
	h := newTurnDiffHarness(t, t.TempDir(), port)
	supervise.Go(t.Context(), "stalled-snapshot-clock", func(context.Context) error {
		<-port.entered
		// The timer may be created after the first advance, so advance until
		// the cap has fired.
		testutil.Spin(t, "the snapshot cap cancelling the stalled snapshot", func() bool {
			h.f.clock.Advance(TurnSnapshotCap)
			return port.cancelled.Load()
		})
		return nil
	})
	h.runTurn(nil)
	h.requireUnavailableAndReady()
}

// TestVRE14GGE03SnapshotFailureIsUnavailableAndTurnCompletes covers a failing
// end snapshot after a good start.
func TestVRE14GGE03SnapshotFailureIsUnavailableAndTurnCompletes(t *testing.T) {
	t.Parallel()
	h := newTurnDiffHarness(t, t.TempDir(), failingEnd{})
	h.runTurn(nil)
	h.requireUnavailableAndReady()
}

func heldCheckpoints(t *testing.T, port *engineTurnSnapshots) int {
	t.Helper()
	rows, err := port.store.ListCheckpoints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// TestVRE14TurnDiffsOutliveTheSessionTurnQuota: a session with more turns than
// the engine's per-session turn quota still gets complete turn_diffs, because
// each turn's records are released once its diff is computed. The quota is 2
// here so six turns cross it quickly; the default is 200.
func TestVRE14TurnDiffsOutliveTheSessionTurnQuota(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), "0\n")
	port := realTurnSnapshotsWith(t, snapshot.Limits{MaxTurnsPerSession: 2})
	h := newTurnDiffHarness(t, dir, port)
	for i := range 6 {
		h.runTurn(func() { writeTestFile(t, filepath.Join(dir, "a.txt"), fmt.Sprintf("%d\n", i+1)) })
	}
	entries := h.turnDiffs()
	if len(entries) != 6 {
		t.Fatalf("turn_diff items = %d, want 6", len(entries))
	}
	for i := range entries {
		if entries[i].Metadata.Status != transcript.TurnDiffComplete || entries[i].Metadata.FilesModified != 1 {
			t.Fatalf("turn_diff metadata = %+v, want one modified file", entries[i].Metadata)
		}
	}
}

// TestVRE14SnapshotRecordsAreReleasedAfterTurnsAndSession: the journal is empty
// once a turn's diff is computed, and a session's leftovers are released when
// it closes.
func TestVRE14SnapshotRecordsAreReleasedAfterTurnsAndSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), "0\n")
	port := realTurnSnapshots(t)
	h := newTurnDiffHarness(t, dir, port)
	h.runTurn(func() { writeTestFile(t, filepath.Join(dir, "a.txt"), "1\n") })
	h.runTurn(nil)
	if n := heldCheckpoints(t, port); n != 0 {
		t.Fatalf("records held after two turns = %d, want 0", n)
	}
	// A start snapshot nobody closed, as a turn lost to a crash would leave.
	if _, err := port.Begin(t.Context(), testActorSessionID, dir); err != nil {
		t.Fatal(err)
	}
	if n := heldCheckpoints(t, port); n != 1 {
		t.Fatalf("records held with a dangling start = %d, want 1", n)
	}
	if err := h.manager.Close(t.Context(), testActorSessionID, vendor.CloseUserRequest); err != nil {
		t.Fatal(err)
	}
	if n := heldCheckpoints(t, port); n != 0 {
		t.Fatalf("records held after the session closed = %d, want 0", n)
	}
}

// warmSnapshots records whether Begin ran after the warm-up finished.
type warmSnapshots struct {
	noopSnapshotLifecycle
	gate        chan struct{}
	warmEntered chan struct{}
	warmed      atomic.Bool
	beginWarmed atomic.Bool
	beginCalls  atomic.Int32
	endedWithID atomic.Value
}

func (w *warmSnapshots) Warm(ctx context.Context, _ string) error {
	close(w.warmEntered)
	select {
	case <-w.gate:
		w.warmed.Store(true)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("warm cancelled: %w", ctx.Err())
	}
}

func (w *warmSnapshots) Begin(_ context.Context, _, workDir string) (TurnSnapshot, error) {
	w.beginCalls.Add(1)
	w.beginWarmed.Store(w.warmed.Load())
	return TurnSnapshot{ID: "start-1", WorkDir: workDir}, nil
}

func (w *warmSnapshots) End(_ context.Context, snap TurnSnapshot) (TurnChanges, error) {
	w.endedWithID.Store(snap.ID)
	return TurnChanges{}, nil
}

// TestVRE14TurnDuringWarmUpWaitsAndGetsACorrectStartSnapshot: the background
// warm-up starts with the session, a turn that begins during it waits for it
// before its start snapshot, and that snapshot is the one the turn ends with.
func TestVRE14TurnDuringWarmUpWaitsAndGetsACorrectStartSnapshot(t *testing.T) {
	t.Parallel()
	port := &warmSnapshots{gate: make(chan struct{}), warmEntered: make(chan struct{})}
	h := newTurnDiffHarness(t, t.TempDir(), port)
	<-port.warmEntered
	sent := make(chan error, 1)
	supervise.Go(t.Context(), "send-during-warm-up", func(ctx context.Context) error {
		sent <- h.manager.Send(ctx, testActorSessionID, vendor.Input{Text: "go"})
		return nil
	})
	testutil.Spin(t, "the turn reaching its start snapshot wait", func() bool { return h.f.clock.Pending() > 0 })
	if port.beginCalls.Load() != 0 {
		t.Fatal("the start snapshot ran before the warm-up ended")
	}
	close(port.gate)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if !port.beginWarmed.Load() {
		t.Fatal("Begin did not see a warmed shadow index")
	}
	h.session.observations <- claude.Observation{TurnEnd: &claude.TurnEnd{TurnID: h.session.turnID, Outcome: claude.OutcomeCompleted}}
	h.turns++
	awaitActor(t, func() bool { return len(h.turnDiffs()) == 1 })
	if got := port.endedWithID.Load(); got != "start-1" {
		t.Fatalf("End got snapshot %v, want start-1", got)
	}
	if h.turnDiffs()[0].Metadata.Status != transcript.TurnDiffComplete {
		t.Fatalf("turn_diff = %+v, want complete", h.turnDiffs()[0].Metadata)
	}
}
