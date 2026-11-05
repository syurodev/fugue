package kernel

import (
	"fmt"
	"testing"
	"time"
)

const (
	drvChunkRef = "chunk-ref"
	drvKnown    = "known"
	drvProvider = "provider"
	drvAllowed  = "allowed"
)

// runDriver walks one run through real Decide calls, checking every committed
// batch with CheckBatch, so continuation and budget tests exercise the same
// path a coordinator would.
type runDriver struct {
	t       *testing.T
	s       *State
	history []Event
	n       int
}

func driverFacts() DecisionFacts {
	return DecisionFacts{
		OwnerIncarnation: 3, ExecutorFence: 11, WorkerReservationRef: "worker-reservation",
		DispatchAuthorizationRef: "current-authorization", BudgetReservationRef: "tool-budget-ref",
		ContextRefs: []string{decContext},
	}
}

func newRunDriver(t *testing.T, budgets RunBudgets) *runDriver {
	t.Helper()
	r := &runDriver{t: t, s: NewState(), history: nil, n: 0}
	accept := &AcceptRunCommand{
		CommandID: "", RunID: "", Origin: "", InputRef: "input-ref", SessionID: "session", QueueItemRef: "queue-ref",
		ProtocolBinding: "config-ref", Budgets: budgets, PriorityClass: "", PeerOutboxRef: "", GoalID: "", GoalRound: 0,
	}
	r.must(accept, nil)
	admit := &AdmitRunCommand{
		CommandID: "", RunID: "", Origin: "", Token: 7, OwnerIncarnation: 3, QueueDecisionRef: "queue-decision",
		ContextEpoch: 1, StartRunID: testTargetRun, InitialContextRevision: 1,
	}
	r.must(admit, func(f *DecisionFacts) { f.ModelPlan = modelPlan("m1", "a1") })
	return r
}

// try decides one command and, when it is accepted, commits and invariant-checks the batch.
func (r *runDriver) try(command Command, mutate func(*DecisionFacts)) ([]Event, error) {
	r.t.Helper()
	r.n++
	c := decisionCase{command: command, facts: driverFacts(), names: nil}
	if mutate != nil {
		mutate(&c.facts)
	}
	bindDecision(&c, r.s, fmt.Sprintf("drive-%d", r.n))
	c.facts.Envelope.Timestamp = time.Date(2026, 11, 5, 0, 0, r.n, 0, time.UTC)
	events, err := Decide(r.s, c.command, &c.facts)
	if err != nil {
		return nil, err
	}
	if ids := CheckBatch(r.s, events, &c.facts); len(ids) != 0 {
		r.t.Fatalf("%s breaks %v", c.command.CommandName(), ids)
	}
	for _, e := range events {
		r.s = Evolve(r.s, e)
	}
	r.history = append(r.history, events...)
	return events, nil
}

func (r *runDriver) must(command Command, mutate func(*DecisionFacts)) []Event {
	r.t.Helper()
	events, err := r.try(command, mutate)
	if err != nil {
		r.t.Fatalf("%s: %v", command.CommandName(), err)
	}
	return events
}

func driverToolPlan(step, position int) *ToolCallPlannedEvent {
	return &ToolCallPlannedEvent{
		Envelope: Envelope{}, ToolCallID: fmt.Sprintf("t%d-%d", step, position), SourcePosition: position,
		DescriptorVersion: "descriptor-v1", EffectClass: EffectClassPureRead,
		FirstAttemptID: fmt.Sprintf("ta%d-%d", step, position), InputRef: fmt.Sprintf("args-%d-%d", step, position),
		Terminates: false, RepeatsPrevious: false,
	}
}

// model dispatches, streams and completes the model call of a step; the returned
// events are those of the response batch.
func (r *runDriver) model(step int, finish FinishReason, tools []*ToolCallPlannedEvent, mutate func(*DecisionFacts)) []Event {
	r.t.Helper()
	r.startModel(step)
	events, err := r.respond(step, finish, tools, mutate)
	if err != nil {
		r.t.Fatalf("response: %v", err)
	}
	return events
}

func (r *runDriver) startModel(step int) {
	r.t.Helper()
	inv, att := fmt.Sprintf("m%d", step), fmt.Sprintf("a%d", step)
	r.must(&PrepareDispatchCommand{CommandID: "", RunID: "", Origin: "", Token: 7, InvocationID: inv, AttemptID: att},
		func(f *DecisionFacts) { f.ModelDispatch = modelDispatch(inv, att) })
	r.must(&ObserveModelStreamCommand{
		CommandID: "", RunID: "", Origin: "", Token: 7, AttemptID: att, InvocationID: inv, FirstChunkRef: drvChunkRef,
	}, nil)
}

func (r *runDriver) respond(step int, finish FinishReason, tools []*ToolCallPlannedEvent, mutate func(*DecisionFacts)) ([]Event, error) {
	r.t.Helper()
	inv, att := fmt.Sprintf("m%d", step), fmt.Sprintf("a%d", step)
	return r.try(&ObserveModelResponseCommand{
		CommandID: "", RunID: "", Origin: "", Token: 7, AttemptID: att, InvocationID: inv,
		ResponseRef: fmt.Sprintf("response-%d", step), UsageStatus: drvKnown, FinishReason: finish,
	}, func(f *DecisionFacts) {
		f.Usage = &BudgetObservedEvent{Envelope: Envelope{}, AttemptID: att, WindowID: "", TokensUsed: 10, DurationUsed: 0, Basis: drvProvider}
		f.ToolPlans = tools
		if mutate != nil {
			mutate(f)
		}
	})
}

// tool runs one planned tool call to its result. The next model plan is always
// supplied; it is only used when the result completes the step and the run continues.
func (r *runDriver) tool(step, position int, mutate func(*DecisionFacts)) []Event {
	r.t.Helper()
	id, att := fmt.Sprintf("t%d-%d", step, position), fmt.Sprintf("ta%d-%d", step, position)
	prepare := &PrepareDispatchCommand{CommandID: "", RunID: "", Origin: "", Token: 7, InvocationID: id, AttemptID: att}
	r.must(prepare, nil)
	r.must(&ObserveToolCheckCommand{
		CommandID: "", RunID: "", Origin: "", Token: 7, ToolCallID: id, AttemptID: att, CheckGeneration: 1,
		ValidatedArgsRef: "validated-args-ref", GateObservation: drvAllowed, ApprovalReceiptRef: "",
	}, nil)
	r.must(prepare, nil)
	return r.must(&ObserveToolResultCommand{
		CommandID: "", RunID: "", Origin: "", Token: 7, ToolCallID: id, AttemptID: att,
		Outcome: string(ToolResultOutcomeSucceeded), ResultRef: fmt.Sprintf("result-%d-%d", step, position),
		Code: "ok", EffectEvidence: "completion-ref", SourcePosition: position,
	}, func(f *DecisionFacts) {
		f.ModelPlan = modelPlan(fmt.Sprintf("m%d", step+1), fmt.Sprintf("a%d", step+1))
		if mutate != nil {
			mutate(f)
		}
	})
}

func (r *runDriver) advance(step int, mutate func(*DecisionFacts)) ([]Event, error) {
	r.t.Helper()
	return r.try(&AdvanceRunCommand{CommandID: "", RunID: "", Origin: "", Token: 7, StepIndex: step}, func(f *DecisionFacts) {
		f.ModelPlan = modelPlan(fmt.Sprintf("m%d", step), fmt.Sprintf("a%d", step))
		if mutate != nil {
			mutate(f)
		}
	})
}

func eventNames(events []Event) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e.EventName())
	}
	return names
}

func findEvent[T Event](events []Event) (T, bool) {
	for _, e := range events {
		if v, ok := e.(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}
