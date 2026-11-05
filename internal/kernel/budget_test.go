package kernel

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func budgetEnvelope(seq uint64) Envelope {
	return Envelope{
		RunID: testTargetRun, Sequence: seq, CommandID: "cmd", CausationID: "cause",
		Origin: decOrigin, Token: 7, Timestamp: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
	}
}

func budgetObserved(seq uint64, attempt, window string, tokens int, duration time.Duration) *BudgetObservedEvent {
	return &BudgetObservedEvent{
		Envelope: budgetEnvelope(seq), AttemptID: attempt, WindowID: window,
		TokensUsed: tokens, DurationUsed: duration, Basis: "observed",
	}
}

func TestDefaultRunBudgetsMatchDesign(t *testing.T) {
	t.Parallel()
	want := RunBudgets{Steps: 100, Tokens: 2_000_000, ToolCalls: 500, ActiveTime: 2 * time.Hour}
	if diff := cmp.Diff(want, DefaultRunBudgets()); diff != "" {
		t.Fatal(diff)
	}
	extensions := map[BudgetKind]int64{
		BudgetKindSteps: 50, BudgetKindTokens: 500_000, BudgetKindTime: 1800, BudgetKindToolCalls: 200,
	}
	for kind, amount := range extensions {
		if got := DefaultExtension(kind); got != amount {
			t.Fatalf("DefaultExtension(%s) = %d, want %d", kind, got, amount)
		}
	}
	if got := DefaultExtension("unknown"); got != 0 {
		t.Fatalf("DefaultExtension(unknown) = %d, want 0", got)
	}
}

func TestEvolveCountsStepsTokensAndToolCalls(t *testing.T) {
	t.Parallel()
	s := NewState()
	events := []Event{
		&StepStartedEvent{Envelope: budgetEnvelope(1), StepIndex: 1, ContextRefs: nil},
		budgetObserved(2, "a1", "", 1200, 0),
		&ToolDispatchStartedEvent{Envelope: budgetEnvelope(3), ToolCallID: "t1", AttemptID: "ta1", CheckGeneration: 1, ExecutorFence: 1, Token: 7, ReceiptRef: "r", BudgetReservation: "b"},
		&ToolDispatchStartedEvent{Envelope: budgetEnvelope(4), ToolCallID: "t2", AttemptID: "ta2", CheckGeneration: 1, ExecutorFence: 1, Token: 7, ReceiptRef: "r", BudgetReservation: "b"},
		&StepStartedEvent{Envelope: budgetEnvelope(5), StepIndex: 2, ContextRefs: nil},
		budgetObserved(6, "a2", "", 800, 0),
		budgetObserved(7, "a2", "w", -5, time.Second),
		&ToolDispatchStartedEvent{Envelope: budgetEnvelope(8), ToolCallID: "t3", AttemptID: "ta3", CheckGeneration: 1, ExecutorFence: 1, Token: 7, ReceiptRef: "r", BudgetReservation: "b"},
	}
	for _, e := range events {
		s = Evolve(s, e)
	}
	if s.StepsUsed != 2 || s.TokensUsed != 2000 || s.ToolCallsDispatched != 3 {
		t.Fatalf("counters = %d steps, %d tokens, %d tool calls", s.StepsUsed, s.TokensUsed, s.ToolCallsDispatched)
	}
	for kind, want := range map[BudgetKind]int64{BudgetKindSteps: 2, BudgetKindTokens: 2000, BudgetKindToolCalls: 3, BudgetKindTime: 0} {
		if got := s.BudgetUsed(kind); got != want {
			t.Fatalf("BudgetUsed(%s) = %d, want %d", kind, got, want)
		}
	}
	if got := s.BudgetUsed("unknown"); got != 0 {
		t.Fatalf("BudgetUsed(unknown) = %d", got)
	}
}

func TestBudgetLimitAddsExtensions(t *testing.T) {
	t.Parallel()
	s := NewState()
	s = Evolve(s, &RunAcceptedEvent{
		Envelope: budgetEnvelope(1), InputRef: "", SessionID: "", QueueItemRef: "", ProtocolBinding: "",
		Budgets: DefaultRunBudgets(), PriorityClass: "", PeerOutboxRef: "", GoalID: "", GoalRound: 0,
	})
	for i := range 2 {
		s = Evolve(s, &BudgetExtendedEvent{
			Envelope: budgetEnvelope(uint64(i) + 2), Kind: BudgetKindSteps, ExtensionAmount: 50, Actor: testUserActor,
		})
	}
	s = Evolve(s, &BudgetExtendedEvent{Envelope: budgetEnvelope(4), Kind: BudgetKindTime, ExtensionAmount: 1800, Actor: testUserActor})
	s = Evolve(s, &BudgetExtendedEvent{Envelope: budgetEnvelope(5), Kind: BudgetKindTokens, ExtensionAmount: -1, Actor: testUserActor})
	want := map[BudgetKind]int64{
		BudgetKindSteps: 200, BudgetKindTokens: 2_000_000, BudgetKindToolCalls: 500, BudgetKindTime: 7200 + 1800,
	}
	for kind, limit := range want {
		if got := s.BudgetLimit(kind); got != limit {
			t.Fatalf("BudgetLimit(%s) = %d, want %d", kind, got, limit)
		}
	}
	if got := s.BudgetLimit("unknown"); got != 0 {
		t.Fatalf("BudgetLimit(unknown) = %d", got)
	}
}

func TestAcceptRunRejectsNonPositiveBudgets(t *testing.T) {
	t.Parallel()
	mutations := map[string]func(*RunBudgets){
		"steps-zero":      func(b *RunBudgets) { b.Steps = 0 },
		"steps-negative":  func(b *RunBudgets) { b.Steps = -1 },
		"tokens-zero":     func(b *RunBudgets) { b.Tokens = 0 },
		"tool-calls-zero": func(b *RunBudgets) { b.ToolCalls = 0 },
		"time-zero":       func(b *RunBudgets) { b.ActiveTime = 0 },
		"time-negative":   func(b *RunBudgets) { b.ActiveTime = -time.Second },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := traceCases()[0]
			bindDecision(&c, NewState(), "accept")
			budgets := DefaultRunBudgets()
			mutate(&budgets)
			fixtureValue[*AcceptRunCommand](c.command).Budgets = budgets
			events, err := Decide(NewState(), c.command, &c.facts)
			var rejection Rejection
			if !errors.As(err, &rejection) || rejection.Code != RejectionCodeCommandRejected || len(events) != 0 {
				t.Fatalf("expected command_rejected, got %v and %d events", err, len(events))
			}
		})
	}
	c := traceCases()[0]
	bindDecision(&c, NewState(), "accept")
	events, err := Decide(NewState(), c.command, &c.facts)
	if err != nil || len(events) != 1 {
		t.Fatalf("default budgets rejected: %v", err)
	}
	accepted, ok := events[0].(*RunAcceptedEvent)
	if !ok || accepted.Budgets != DefaultRunBudgets() {
		t.Fatalf("budgets were not pinned: %s", fmt.Sprint(events[0]))
	}
}
