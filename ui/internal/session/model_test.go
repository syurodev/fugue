package session

import (
	"fmt"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/pkg/client"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestSessionListAppliesSnapshotThenLiveChanges pins the sidebar over the
// vendor-sessions topic: the snapshot replaces the list, a state change
// updates its session in place, and a created session joins the list with
// what the event carries.
func TestSessionListAppliesSnapshotThenLiveChanges(t *testing.T) {
	t.Parallel()
	s := NewSidebar()
	snap := []byte(`{"sessions":[
		{"session_id":"s1","vendor":"claude","state":"running","created_at":"t1","updated_at":"t1"},
		{"session_id":"s2","vendor":"claude","state":"waiting_input","waiting_reason":"approval","created_at":"t2","updated_at":"t2"}]}`)
	if err := s.ApplySnapshot(snap); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 2 || list[0].ID != "s1" || list[1].State != "waiting_input" {
		t.Fatalf("list after snapshot = %+v", list)
	}

	if err := s.ApplyEvent(EventSessionStateChanged, []byte(`{"session_id":"s1","state":"waiting_input","waiting_reason":"steer"}`)); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Lookup("s1")
	if !ok || got.State != "waiting_input" || got.WaitingReason != "steer" {
		t.Fatalf("s1 after state change = %+v, ok %v", got, ok)
	}

	if err := s.ApplyEvent(EventSessionCreated, []byte(`{"session_id":"s3","state":"starting"}`)); err != nil {
		t.Fatal(err)
	}
	list = s.List()
	if len(list) != 3 || list[2].ID != "s3" || list[2].State != "starting" {
		t.Fatalf("list after create = %+v", list)
	}

	// A state change for a session the list has not seen stays ignored; the
	// next snapshot or list read names it.
	if err := s.ApplyEvent(EventSessionStateChanged, []byte(`{"session_id":"s9","state":"closed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup("s9"); ok {
		t.Fatal("an unknown session joined the list from a state change")
	}
}

// transcriptFixture builds one durable item payload the way the host writes
// it.
func transcriptFixture(t *testing.T, itemID string, seq, rev uint64, text string) client.TranscriptItem {
	t.Helper()
	return client.TranscriptItem{
		ItemID: itemID, TurnID: "turn-1", Kind: "assistant_message",
		Sequence: seq, Revision: rev,
		View: client.TranscriptView{Text: text},
	}
}

// TestTranscriptModelAppliesUpsertsByRevisionAndIgnoresStale pins the upsert
// rule: a newer revision replaces the held item, and a revision that is not
// newer — from a replayed catch-up or a duplicated event — changes nothing.
func TestTranscriptModelAppliesUpsertsByRevisionAndIgnoresStale(t *testing.T) {
	t.Parallel()
	m := NewTranscript("s1")
	m.ApplyNewestPage([]client.TranscriptItem{
		transcriptFixture(t, "i1", 3, 1, "one"),
		transcriptFixture(t, "i2", 4, 1, "two"),
	})
	if len(m.Items()) != 2 || m.Items()[0].ItemID != "i1" {
		t.Fatalf("page applied out of order: %+v", m.Items())
	}

	// A newer revision replaces the held state.
	upsert := transcriptFixture(t, "i1", 3, 2, "one, revised")
	if err := m.ApplyEvent(EventTranscriptItem, 9, mustItemPayload(t, upsert)); err != nil {
		t.Fatal(err)
	}
	if got := m.Items()[0]; got.Revision != 2 || got.View().Text != "one, revised" {
		t.Fatalf("i1 after upsert = rev %d %q, want rev 2", got.Revision, got.View().Text)
	}

	// The same revision again, and a stale one, change nothing.
	for _, rev := range []uint64{2, 1} {
		stale := transcriptFixture(t, "i1", 3, rev, "stale")
		if err := m.ApplyEvent(EventTranscriptItem, 9, mustItemPayload(t, stale)); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.Items()[0]; got.Revision != 2 || got.View().Text != "one, revised" {
		t.Fatalf("i1 after stale upserts = rev %d %q, want the rev-2 text", got.Revision, got.View().Text)
	}

	// A page never rolls a held item back: only newer revisions replace.
	m.ApplyNewestPage([]client.TranscriptItem{transcriptFixture(t, "i1", 3, 1, "old")})
	if got := m.Items()[0]; got.Revision != 2 {
		t.Fatalf("page rolled i1 back to rev %d", got.Revision)
	}
}

// TestTranscriptDeltaAppliesToTheOpenItem pins the delta rule: the latest
// complete display state lands on the open item, a stale base is ignored, and
// a durable revision ends the streaming view.
func TestTranscriptDeltaAppliesToTheOpenItem(t *testing.T) {
	t.Parallel()
	m := NewTranscript("s1")
	m.ApplyNewestPage([]client.TranscriptItem{transcriptFixture(t, "i1", 3, 2, "")})

	if err := m.ApplyDelta(mustDeltaPayload(t, "i1", 2, "streaming…")); err != nil {
		t.Fatal(err)
	}
	if got := m.Items()[0].View().Text; got != "streaming…" {
		t.Fatalf("open item view = %q, want the streaming view", got)
	}
	// A newer revision of the item ends the streaming view.
	item := transcriptFixture(t, "i1", 3, 3, "final")
	if err := m.ApplyEvent(EventTranscriptItem, 10, mustItemPayload(t, item)); err != nil {
		t.Fatal(err)
	}
	if got := m.Items()[0].View().Text; got != "final" {
		t.Fatalf("open item view after the durable event = %q, want the durable text", got)
	}
	if m.Items()[0].Streaming != nil {
		t.Fatal("the streaming view survived the durable revision")
	}
	// A stale delta base does not reopen the streaming view.
	if err := m.ApplyDelta(mustDeltaPayload(t, "i1", 2, "stale")); err != nil {
		t.Fatal(err)
	}
	if got := m.Items()[0].View().Text; got != "final" {
		t.Fatalf("stale delta overwrote the durable view with %q", got)
	}
	// A delta for an item the model has not seen is dropped, not created.
	if err := m.ApplyDelta(mustDeltaPayload(t, "i9", 1, "ghost")); err != nil {
		t.Fatal(err)
	}
	if len(m.Items()) != 1 {
		t.Fatalf("a delta created an item: %d items", len(m.Items()))
	}
}

// TestTranscriptReconnectCatchesUpFromTheCursor is DA-E04 at the model level:
// after a dropped connection the durable events replayed from the cursor land
// once, in order, with no duplicate and no gap.
func TestTranscriptReconnectCatchesUpFromTheCursor(t *testing.T) {
	t.Parallel()
	m := NewTranscript("s1")
	m.ApplyNewestPage([]client.TranscriptItem{
		transcriptFixture(t, "i1", 1, 1, "one"),
		transcriptFixture(t, "i2", 2, 1, "two"),
	})
	if m.LastSequence() != 0 {
		t.Fatalf("last sequence = %d before any topic event, want 0", m.LastSequence())
	}
	// The stream drops; the manager resubscribes from the cursor and the
	// missed events arrive: item 3, then item 2's update, contiguously.
	events := []struct {
		seq  uint64
		item client.TranscriptItem
	}{
		{3, transcriptFixture(t, "i3", 3, 1, "three")},
		{4, transcriptFixture(t, "i2", 2, 2, "two, revised")},
		{5, transcriptFixture(t, "i4", 4, 1, "four")},
	}
	want := uint64(0)
	for _, e := range events {
		if err := m.ApplyEvent(EventTranscriptItem, e.seq, mustItemPayload(t, e.item)); err != nil {
			t.Fatal(err)
		}
		want = e.seq
		if m.LastSequence() != want {
			t.Fatalf("last sequence = %d after event %d, want %d: gap", m.LastSequence(), e.seq, want)
		}
	}
	// The host also replays the newest of the catch-up once more.
	if err := m.ApplyEvent(EventTranscriptItem, 5, mustItemPayload(t, events[2].item)); err != nil {
		t.Fatal(err)
	}
	if m.LastSequence() != 5 || len(m.Items()) != 4 {
		t.Fatalf("replayed catch-up duplicated: last %d, %d items", m.LastSequence(), len(m.Items()))
	}
	if got := m.Items()[1]; got.ItemID != "i2" || got.Revision != 2 {
		t.Fatalf("i2 after catch-up = rev %d, want 2", got.Revision)
	}
}

// TestResyncRequiredReloadsFromTheSnapshot pins the resync rule: a snapshot
// the host sends after resync_required clears everything held and the caller
// re-reads the history.
func TestResyncRequiredReloadsFromTheSnapshot(t *testing.T) {
	t.Parallel()
	m := NewTranscript("s1")
	m.ApplyNewestPage([]client.TranscriptItem{transcriptFixture(t, "i1", 1, 1, "one")})
	if m.NeedsNewestPage() {
		t.Fatal("the model wants a page it already has")
	}

	// resync_required: the manager clears its cursor, the host answers with a
	// fresh snapshot, and the model must reload from it.
	snap := mustSnapshotPayload(t, 20, "running", transcriptFixture(t, "i2", 21, 1, "open item"))
	if err := m.ApplySnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if len(m.Items()) != 1 || m.Items()[0].ItemID != "i2" {
		t.Fatalf("items after resync = %+v, want only the snapshot's open item", m.Items())
	}
	if !m.NeedsNewestPage() {
		t.Fatal("the model did not re-request the history after the resync")
	}
	if m.LastSequence() != 20 || m.SessionState() != "running" {
		t.Fatalf("snapshot state = seq %d %q, want 20 running", m.LastSequence(), m.SessionState())
	}
	m.ApplyNewestPage(fullPage(t, 21))
	if got := len(m.Items()); got != 200 {
		t.Fatalf("items after reload = %d, want the 200-item page", got)
	}
	if before, ok := m.OlderCursor(); !ok || before != 2 {
		t.Fatalf("older cursor = %d %v, want 2 true", before, ok)
	}
}

// fullPage builds a full 200-item page whose item at want is the fixture
// item, so a short page never completes the history.
func fullPage(t *testing.T, want int) []client.TranscriptItem {
	t.Helper()
	page := make([]client.TranscriptItem, PageLimit)
	for i := range page {
		seq := i + 2
		if seq == want {
			page[i] = transcriptFixture(t, "i2", uint64(seq), 1, "open item")
			continue
		}
		page[i] = transcriptFixture(t, fmt.Sprintf("p%d", seq), uint64(seq), 1, "older")
	}
	return page
}
