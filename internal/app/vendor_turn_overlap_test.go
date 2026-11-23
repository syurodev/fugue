package app

// VR-E14 / git-guard GG-E04: a turn_diff is overlapped when a turn of another
// session on the same working directory was open while it was.

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	sessA = "sess-a"
	sessB = "sess-b"
)

// multiAdapter hands each session its own fake vendor session.
type multiAdapter struct {
	sessions map[string]*actorTestSession
}

func (multiAdapter) Probe(context.Context, vendor.AccountHome) (vendor.Probe, error) {
	return vendor.Probe{}, nil
}

func (m multiAdapter) Start(_ context.Context, spec *vendor.StartSpec) (vendor.Session, error) {
	return m.sessions[spec.SessionID], nil
}

type overlapHarness struct {
	t        *testing.T
	f        *transcriptFixture
	manager  *VendorSessions
	sessions map[string]*actorTestSession
}

func newOverlapHarness(t *testing.T, dirs map[string]string) *overlapHarness {
	t.Helper()
	f := newTranscriptFixture(t, nil)
	sessions := make(map[string]*actorTestSession, len(dirs))
	for id := range dirs {
		sessions[id] = newActorTestSession()
	}
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: multiAdapter{sessions: sessions}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("overlap-item-"), TurnSnapshots: realTurnSnapshots(t),
	})
	for id, dir := range dirs {
		info := &vendorstore.Session{ID: id, Vendor: testVendorName, AccountID: testAccountID, Role: testActorRole}
		if err := manager.Start(t.Context(), info, &vendor.StartSpec{SessionID: id, Cwd: dir}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if err := manager.CloseAll(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return &overlapHarness{t: t, f: f, manager: manager, sessions: sessions}
}

func (h *overlapHarness) begin(session, turn string) {
	h.t.Helper()
	h.sessions[session].turnID = turn
	if err := h.manager.Send(h.t.Context(), session, vendor.Input{Text: "go"}); err != nil {
		h.t.Fatal(err)
	}
}

// finish ends the turn and returns the overlapped flag of its turn_diff.
func (h *overlapHarness) finish(session, turn string) bool {
	h.t.Helper()
	h.sessions[session].observations <- claude.Observation{TurnEnd: &claude.TurnEnd{TurnID: turn, Outcome: claude.OutcomeCompleted}}
	var found bool
	var overlapped bool
	awaitActor(h.t, func() bool {
		entries, err := h.f.service.TranscriptPage(h.t.Context(), TranscriptPageParams{Session: session, Limit: 500})
		if err != nil {
			return false
		}
		for i := range entries {
			if entries[i].Kind == transcript.TurnDiff && entries[i].TurnID == turn {
				found, overlapped = true, entries[i].Metadata.Overlapped
			}
		}
		return found
	})
	return overlapped
}

func TestVRE14GGE04OverlappingTurnsOnOneDirectoryAreBothOverlapped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newOverlapHarness(t, map[string]string{sessA: dir, sessB: dir})
	h.begin(sessA, "turn-a")
	h.begin(sessB, "turn-b")
	if !h.finish(sessA, "turn-a") {
		t.Error("the first turn to end is not overlapped")
	}
	if !h.finish(sessB, "turn-b") {
		t.Error("the second turn to end is not overlapped")
	}
	if n := h.manager.overlaps.openCount(); n != 0 {
		t.Errorf("open registry entries after both turns = %d, want 0", n)
	}
}

func TestVRE14GGE04TurnNestedInsideAnotherMarksBoth(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newOverlapHarness(t, map[string]string{sessA: dir, sessB: dir})
	h.begin(sessA, "turn-a")
	h.begin(sessB, "turn-b")
	if !h.finish(sessB, "turn-b") {
		t.Error("the inner turn is not overlapped")
	}
	if !h.finish(sessA, "turn-a") {
		t.Error("the outer turn is not overlapped")
	}
}

func TestVRE14GGE04SequentialTurnsAreNotOverlapped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newOverlapHarness(t, map[string]string{sessA: dir, sessB: dir})
	h.begin(sessA, "turn-a")
	if h.finish(sessA, "turn-a") {
		t.Error("a turn with no other turn open is overlapped")
	}
	h.begin(sessB, "turn-b")
	if h.finish(sessB, "turn-b") {
		t.Error("a turn that started after another had ended is overlapped")
	}
	h.begin(sessA, "turn-a2")
	if h.finish(sessA, "turn-a2") {
		t.Error("a second turn of the same session is overlapped")
	}
}

func TestVRE14GGE04DifferentDirectoriesAreNotOverlapped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	h := newOverlapHarness(t, map[string]string{sessA: filepath.Join(root, "a"), sessB: filepath.Join(root, "b")})
	h.begin(sessA, "turn-a")
	h.begin(sessB, "turn-b")
	if h.finish(sessA, "turn-a") || h.finish(sessB, "turn-b") {
		t.Error("turns on different directories are overlapped")
	}
}

func TestVRE14GGE04SessionClosedMidTurnReleasesItsRegistryEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newOverlapHarness(t, map[string]string{sessA: dir, sessB: dir})
	h.begin(sessA, "turn-a")
	if err := h.manager.Close(t.Context(), sessA, vendor.CloseUserRequest); err != nil {
		t.Fatal(err)
	}
	if n := h.manager.overlaps.openCount(); n != 0 {
		t.Fatalf("open registry entries after the session closed = %d, want 0", n)
	}
	h.begin(sessB, "turn-b")
	if h.finish(sessB, "turn-b") {
		t.Error("a turn is overlapped by a session that already closed")
	}
}

// TestVRE14GGE04ConcurrentTurnsKeepTheRegistryRaceFree hammers begin and end
// from many goroutines under -race; nothing is left open afterwards.
func TestVRE14GGE04ConcurrentTurnsKeepTheRegistryRaceFree(t *testing.T) {
	t.Parallel()
	o := newTurnOverlaps()
	dir := t.TempDir()
	const sessions = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range sessions {
		wg.Go(func() {
			<-start
			id := fmt.Sprintf("s%d", i)
			for range 50 {
				o.begin(dir, id, "t")
				o.end(dir, id, "t")
			}
		})
	}
	close(start)
	wg.Wait()
	if n := o.openCount(); n != 0 {
		t.Fatalf("open entries after the run = %d, want 0", n)
	}
}
