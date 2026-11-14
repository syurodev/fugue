package vendorstore

// F9.4b read-only additions: the session list, the atomic session overview
// behind the durable topic snapshot, and the metadata-only item read.

import (
	"errors"
	"testing"
)

const (
	overviewSession  = "s"
	overviewOpenItem = "open"
)

func mustCreateOverviewSession(t *testing.T, s *Store, id, state string) {
	t.Helper()
	requireNoError(t, s.CreateSession(t.Context(), &Session{
		ID: id, Vendor: "v", AccountID: "a", Role: "r", State: state,
	}))
}

func mustAppendOverview(t *testing.T, s *Store, expected int64, event *Event) int64 {
	t.Helper()
	seq, err := s.Append(t.Context(), overviewSession, expected, event)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestListSessionsReturnsEverySession(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	mustCreateOverviewSession(t, s, "s1", StateStarting)
	mustCreateOverviewSession(t, s, "s2", StateReady)

	sessions, err := s.ListSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("ListSessions = %d sessions, want 2", len(sessions))
	}
	// The fake clock stamps both sessions the same, so the tie breaks on ID.
	if sessions[0].ID != "s1" || sessions[1].ID != "s2" {
		t.Fatalf("order = %s, %s; want s1, s2", sessions[0].ID, sessions[1].ID)
	}
	if sessions[1].State != StateReady {
		t.Fatalf("session state = %s, want %s", sessions[1].State, StateReady)
	}
}

func TestListSessionsIsEmptyWithoutSessions(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	sessions, err := s.ListSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("ListSessions = %v, want empty", sessions)
	}
}

func TestOverviewReadsSessionTurnsAndOpenItems(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	mustCreateOverviewSession(t, s, "s", StateRunning)
	// Sequence 2 opens a turn, 3 records an open tool call, 4 a finished item.
	mustAppendOverview(t, s, 1, &Event{Type: VendorTurnStarted, TurnID: "t", State: StateRunning})
	mustAppendOverview(t, s, 2, &Event{
		Type: TranscriptItemRecorded, TurnID: "t",
		Item: &Item{ID: overviewOpenItem, TurnID: "t", Kind: KindToolCall, Metadata: ItemMetadata{Tool: "shell", Status: ToolPending}},
	})
	mustAppendOverview(t, s, 3, &Event{
		Type: TranscriptItemRecorded, TurnID: "t",
		Item: &Item{ID: "done", TurnID: "t", Kind: KindAssistantMessage, Metadata: ItemMetadata{Model: "m"}},
	})

	session, turns, items, err := s.Overview(t.Context(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "s" || session.LastSequence != 4 || session.State != StateRunning {
		t.Fatalf("session = %+v, want s at sequence 4 running", session)
	}
	if len(turns) != 1 || turns[0].ID != "t" {
		t.Fatalf("open turns = %v, want [t]", turns)
	}
	if len(items) != 1 || items[0].ID != overviewOpenItem {
		t.Fatalf("open items = %+v, want [open]", items)
	}

	// Ending the turn and closing the item empties the overview's open sets.
	mustAppendOverview(t, s, 4, &Event{Type: VendorTurnEnded, TurnID: "t", Outcome: OutcomeCompleted})
	updated := &Event{
		Type: TranscriptItemUpdated, TurnID: "t",
		Item: &Item{ID: overviewOpenItem, TurnID: "t", Kind: KindToolCall, Revision: 1, Metadata: ItemMetadata{Tool: "shell", Status: ToolSucceeded}},
	}
	mustAppendOverview(t, s, 5, updated)
	_, turns, items, err = s.Overview(t.Context(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 || len(items) != 0 {
		t.Fatalf("after completion turns = %v items = %+v, want both empty", turns, items)
	}
}

func TestOverviewOfUnknownSessionIsNotFound(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	if _, _, _, err := s.Overview(t.Context(), "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("overview of a missing session = %v, want ErrSessionNotFound", err)
	}
}

func TestStoredItemMetaReturnsProjectionWithoutPayload(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	mustCreateOverviewSession(t, s, "s", StateRunning)
	mustAppendOverview(t, s, 1, &Event{Type: VendorTurnStarted, TurnID: "t", State: StateRunning})
	mustAppendOverview(t, s, 2, &Event{
		Type: TranscriptItemRecorded, TurnID: "t",
		Item: &Item{ID: "i", TurnID: "t", Kind: KindNotice, Metadata: ItemMetadata{Level: "info", Code: "c"}},
	})

	item, err := s.StoredItemMeta(t.Context(), "s", "i")
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "i" || item.Kind != KindNotice || item.Sequence != 3 {
		t.Fatalf("stored item = %+v, want i at sequence 3", item)
	}
	if _, err := s.StoredItemMeta(t.Context(), "s", "missing"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("missing item = %v, want ErrItemNotFound", err)
	}
}
