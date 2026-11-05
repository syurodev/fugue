package coordinator

import (
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func TestAdmissionFollowsFIFOForRunsAcceptedThisIncarnation(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("fifo-test"))

	store := newFakeStore()
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "",
		OwnerIncarnation: 1,
		AdmissionToken:   0,
		Revision:         1,
		QueueRevision:    1,
	}

	exec := newFakeExecutor()
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   2,
		Store:            store,
		Executor:         exec,
		Clock:            fakeClk,
		IDs:              idGen,
	}

	ctx := t.Context()
	sess, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	defer sess.Stop()

	// 1. Accept r1 (results queued: AcceptRun, then auto AdmitRun)
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-accept-r1", Kind: "AcceptRun", Target: "r1"},
		RunRevision:       1,
		AdmissionRevision: 1,
	}, nil)
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-admit-r1", Kind: "AdmitRun", Target: "r1"},
		RunRevision:       2,
		AdmissionRevision: 2,
	}, nil)

	modelPlanR1 := &kernel.ModelRequestPlannedEvent{
		InvocationID:   "m1",
		FirstAttemptID: "a1",
		Purpose:        "turn",
		ModelRef:       "gpt-4",
		ContextRef:     "c1",
	}

	_, err = sess.Submit(ctx, Request{
		Command: &kernel.AcceptRunCommand{
			CommandID: "cmd-accept-r1",
			RunID:     "r1",
			Origin:    "user",
			SessionID: "s1",
		},
		Facts: kernel.DecisionFacts{
			ModelPlan: modelPlanR1,
		},
		Kind:      "AcceptRun",
		Principal: "user",
		Target:    "r1",
	})
	if err != nil {
		t.Fatalf("accept r1 failed: %v", err)
	}

	// 2. Accept r2 while r1 is active (results queued: AcceptRun only, no auto AdmitRun yet)
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-accept-r2", Kind: "AcceptRun", Target: "r2"},
		RunRevision:       1,
		AdmissionRevision: 2,
	}, nil)

	modelPlanR2 := &kernel.ModelRequestPlannedEvent{
		InvocationID:   "m2",
		FirstAttemptID: "a2",
		Purpose:        "turn",
		ModelRef:       "gpt-4",
		ContextRef:     "c2",
	}

	_, err = sess.Submit(ctx, Request{
		Command: &kernel.AcceptRunCommand{
			CommandID: "cmd-accept-r2",
			RunID:     "r2",
			Origin:    "user",
			SessionID: "s1",
		},
		Facts: kernel.DecisionFacts{
			ModelPlan: modelPlanR2,
		},
		Kind:      "AcceptRun",
		Principal: "user",
		Target:    "r2",
	})
	if err != nil {
		t.Fatalf("accept r2 failed: %v", err)
	}

	// 3. Finish r1 -> should trigger auto-admit of r2
	// For ObserveModelResponseCommand, decide_model produces ModelResponseRecorded + StepCompleted + RunFinished
	// when no tool calls are planned.
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-finish-r1", Kind: "ObserveModelResponse", Target: "r1"},
		RunRevision:       3,
		AdmissionRevision: 3,
	}, nil)
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-admit-r2", Kind: "AdmitRun", Target: "r2"},
		RunRevision:       2,
		AdmissionRevision: 4,
	}, nil)

	// In r1 state, make attempt a1 requesting so ObserveModelResponse is valid.
	sess.mu.Lock()
	r1State := sess.runs["r1"]
	if r1State != nil {
		if inv := r1State.ModelInvocations["m1"]; inv != nil {
			if att := inv.Attempts["a1"]; att != nil {
				att.Status = kernel.ModelAttemptStatusRequesting
				att.Dispatch.ExecutorFence = 10
			}
		}
	}
	sess.mu.Unlock()

	_, err = sess.Submit(ctx, Request{
		Command: &kernel.ObserveModelResponseCommand{
			CommandID:    "cmd-finish-r1",
			RunID:        "r1",
			Origin:       "system",
			Token:        1,
			AttemptID:    "a1",
			InvocationID: "m1",
			ResponseRef:  "resp-r1",
			UsageStatus:  "known",
		},
		Facts: kernel.DecisionFacts{
			ExecutorFence: 10,
			Usage: &kernel.BudgetObservedEvent{
				Envelope:   kernel.Envelope{},
				AttemptID:  "a1",
				TokensUsed: 10,
				Basis:      "provider",
			},
		},
		Kind:      "ObserveModelResponse",
		Principal: "system",
		Target:    "r1",
	})
	if err != nil {
		t.Fatalf("finish r1 failed: %v", err)
	}

	// Verify transitions
	store.mu.Lock()
	transitions := store.transitions
	store.mu.Unlock()

	// We expect:
	// 0: AcceptRun r1
	// 1: AdmitRun r1 (token 1)
	// 2: AcceptRun r2
	// 3: ObserveModelResponse r1 (finishing r1)
	// 4: AdmitRun r2 (token 2)
	if len(transitions) < 5 {
		t.Fatalf("expected at least 5 transitions, got %d", len(transitions))
	}

	admitR1Tx := transitions[1]
	if admitR1Tx.RunID != "r1" {
		t.Fatalf("expected r1 for first admit, got %s", admitR1Tx.RunID)
	}
	if admitR1Tx.Fence.AdmissionToken != 1 {
		t.Fatalf("expected token 1 for r1, got %d", admitR1Tx.Fence.AdmissionToken)
	}

	admitR2Tx := transitions[4]
	if admitR2Tx.RunID != "r2" {
		t.Fatalf("expected r2 for second admit, got %s", admitR2Tx.RunID)
	}
	if admitR2Tx.Fence.AdmissionToken != 2 {
		t.Fatalf("expected token 2 for r2, got %d", admitR2Tx.Fence.AdmissionToken)
	}
}

func TestQueuedRunsFromEarlierIncarnationAreNotAutoAdmitted(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("earlier-test"))

	store := newFakeStore()
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "",
		OwnerIncarnation: 2,
		AdmissionToken:   0,
		Revision:         1,
		QueueRevision:    1,
	}
	// Store holds a queued run from an earlier incarnation
	store.runs["r-old"] = &kernel.State{
		RunID:    "r-old",
		Accepted: true,
		Admitted: false,
	}

	exec := newFakeExecutor()
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 2,
		WorkerCapacity:   1,
		Store:            store,
		Executor:         exec,
		Clock:            fakeClk,
		IDs:              idGen,
	}

	ctx := t.Context()
	sess, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	defer sess.Stop()

	// Verify no auto-admit was recorded
	store.mu.Lock()
	txCount := len(store.transitions)
	store.mu.Unlock()

	if txCount != 0 {
		t.Fatalf("expected zero transitions for queued run from earlier incarnation, got %d", txCount)
	}
}
