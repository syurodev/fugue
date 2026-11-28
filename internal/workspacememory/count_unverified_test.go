package workspacememory

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestCountUnverifiedAndSnapshotAndEvent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})

	// Initially empty.
	if n, err := env.store.CountUnverified(ctx, testWSID); err != nil || n != 0 {
		t.Fatalf("CountUnverified empty = (%d, %v), want (0, nil)", n, err)
	}
	byWS, err := env.store.CountUnverifiedByWorkspace(ctx)
	if err != nil || len(byWS) != 0 {
		t.Fatalf("CountUnverifiedByWorkspace empty = (%v, %v), want empty", byWS, err)
	}

	// Agent write => unverified valid entry => count becomes 1.
	e1, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       "agent-fact-1",
		Summary:     "First unverified agent fact.",
		Body:        "Agent observed something useful in the repository.",
		Author:      AuthorAgent,
	})
	if err != nil {
		t.Fatalf("Add agent e1: %v", err)
	}
	env.clk.Advance(time.Second)

	// Second agent write in same workspace => count becomes 2.
	e2, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindNote,
		Label:       "agent-note-2",
		Summary:     "Second unverified agent note.",
		Body:        "Another unverified note written by the agent.",
		Author:      AuthorAgent,
	})
	if err != nil {
		t.Fatalf("Add agent e2: %v", err)
	}
	env.clk.Advance(time.Second)

	// User write => verified => does not increment unverified count.
	if _, addErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindDecision,
		Label:       "user-decision",
		Summary:     "Verified user decision.",
		Body:        "User decisions are verified on write.",
		Author:      AuthorUser,
	}); addErr != nil {
		t.Fatalf("Add user: %v", addErr)
	}
	env.clk.Advance(time.Second)

	// Agent write in another workspace => 1 there.
	if _, addErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: "ws-2",
		Kind:        KindFact,
		Label:       "ws2-fact",
		Summary:     "Workspace 2 unverified fact.",
		Body:        "Agent note for the second workspace.",
		Author:      AuthorAgent,
	}); addErr != nil {
		t.Fatalf("Add ws-2: %v", addErr)
	}
	env.clk.Advance(time.Second)

	// Verify e1 => ws-1 drops to 1.
	if _, vErr := env.store.Verify(ctx, &VerifyInput{
		WorkspaceID:      testWSID,
		ID:               e1.ID,
		ExpectedRevision: e1.Revision,
	}); vErr != nil {
		t.Fatalf("Verify e1: %v", vErr)
	}
	env.clk.Advance(time.Second)

	// Invalidate e2 => ws-1 drops to 0 (invalid entries are excluded).
	if _, invErr := env.store.Invalidate(ctx, &InvalidateInput{
		WorkspaceID:      testWSID,
		ID:               e2.ID,
		ExpectedRevision: e2.Revision,
		Reason:           "no longer needed",
		Author:           AuthorUser,
	}); invErr != nil {
		t.Fatalf("Invalidate e2: %v", invErr)
	}

	if n, nErr := env.store.CountUnverified(ctx, testWSID); nErr != nil || n != 0 {
		t.Fatalf("CountUnverified(%s) = (%d, %v), want (0, nil)", testWSID, n, nErr)
	}
	if n, nErr := env.store.CountUnverified(ctx, "ws-2"); nErr != nil || n != 1 {
		t.Fatalf("CountUnverified(ws-2) = (%d, %v), want (1, nil)", n, nErr)
	}

	byWS, err = env.store.CountUnverifiedByWorkspace(ctx)
	if err != nil {
		t.Fatalf("CountUnverifiedByWorkspace: %v", err)
	}
	if diff := cmp.Diff(map[string]int{"ws-2": 1}, byWS); diff != "" {
		t.Fatalf("CountUnverifiedByWorkspace diff (-want +got):\n%s", diff)
	}

	evs := env.events.snapshot()
	var counts []int
	for _, ev := range evs {
		counts = append(counts, ev.UnverifiedCount)
	}
	// e1 add (1), e2 add (2), user add (2), ws-2 add (1), e1 verify (1), e2 invalidate (0).
	wantCounts := []int{1, 2, 2, 1, 1, 0}
	if diff := cmp.Diff(wantCounts, counts); diff != "" {
		t.Fatalf("WrittenEvent.UnverifiedCount sequence diff (-want +got):\n%s", diff)
	}
}
