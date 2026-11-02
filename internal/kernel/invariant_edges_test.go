package kernel

import (
	"slices"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const invDifferent = "different"

func TestApprovalAndWindowEvidence(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 6)
	c := cases[6]
	bindDecision(&c, s, "approval")
	q := new(ToolApprovalRequestedEvent)
	q.Envelope = c.facts.Envelope
	q.ToolCallID = "t1"
	q.CheckGeneration = 1
	q.OwnerRequestRef = "owner-request"
	q.RequestRevision = 1
	q.ExactArgsRef = "approval-args"
	if ids := CheckBatch(s, []Event{q}, &c.facts); len(ids) > 0 {
		t.Fatal(ids)
	}
	s = Evolve(s, q)
	if ids := CheckInvariants(s); len(ids) > 0 {
		t.Fatal(ids)
	}
	before := cloneState(s)
	resolved := new(ToolApprovalResolvedEvent)
	resolved.ToolCallID = "t1"
	resolved.CheckGeneration = 2
	after := Evolve(s, resolved)
	if after.ToolInvocations["t1"].Approval != s.ToolInvocations["t1"].Approval {
		t.Fatal("stale resolution cleared approval")
	}
	if diff := cmp.Diff(&before, s); diff != "" {
		t.Fatal(diff)
	}
	resolved.CheckGeneration = 1
	after = Evolve(s, resolved)
	if after.ToolInvocations["t1"].Approval.ToolCallID != "" {
		t.Fatal("resolution did not clear approval")
	}
}

func TestWindowAccountingRequiredBeforeFinish(t *testing.T) {
	t.Parallel()
	s, _ := traceState(t, 12)
	opened := new(TimeBudgetWindowOpenedEvent)
	opened.WindowID = "invariant-window"
	opened.Generation = 1
	s = Evolve(s, opened)
	if !slices.Contains(CheckInvariants(s), SM05) {
		t.Fatal("open window allowed at finish")
	}
	closed := new(TimeBudgetWindowClosedEvent)
	closed.WindowID = "invariant-window"
	closed.TailCoverage = "measured"
	closed.ReservationDisposition = "released"
	before := cloneState(s)
	after := Evolve(s, closed)
	if ids := CheckInvariants(after); len(ids) > 0 {
		t.Fatal(ids)
	}
	if diff := cmp.Diff(&before, s); diff != "" {
		t.Fatal(diff)
	}
	after.TimeWindows["invariant-window"].Close.ReservationDisposition = "changed"
	if s.TimeWindows["invariant-window"].Close.ReservationDisposition != "" {
		t.Fatal("window clone aliased")
	}
}

type batchGuard struct {
	name   string
	stage  int
	id     InvariantID
	mutate func(*State, *decisionCase, []Event) []Event
}

func metadataBatchGuards() []batchGuard {
	return []batchGuard{
		{"no-facts", 2, SM09, func(_ *State, c *decisionCase, e []Event) []Event { c.facts = DecisionFacts{}; return e }},
		{"no-worker", 2, SM10, func(_ *State, c *decisionCase, e []Event) []Event { c.facts.WorkerReservationRef = ""; return e }},
		{"no-grant", 2, SM10, func(_ *State, c *decisionCase, e []Event) []Event { c.facts.DispatchAuthorizationRef = ""; return e }},
		{"wrong-owner", 4, SM09, func(_ *State, c *decisionCase, e []Event) []Event { c.facts.OwnerIncarnation++; return e }},
		{"wrong-model-fence", 4, SM09, func(_ *State, c *decisionCase, e []Event) []Event { c.facts.ExecutorFence++; return e }},
		{"wrong-tool-fence", 8, SM09, func(_ *State, c *decisionCase, e []Event) []Event { c.facts.ExecutorFence++; return e }},
		{"wrong-token", 4, SM09, func(_ *State, _ *decisionCase, e []Event) []Event {
			fixtureValue[*ModelResponseRecordedEvent](e[0]).Envelope.Token++
			return e
		}},
		{"wrong-sequence", 2, SM09, func(_ *State, _ *decisionCase, e []Event) []Event {
			fixtureValue[*ModelDispatchStartedEvent](e[0]).Envelope.Sequence++
			return e
		}},
		{"wrong-run", 2, SM09, func(_ *State, _ *decisionCase, e []Event) []Event {
			fixtureValue[*ModelDispatchStartedEvent](e[0]).Envelope.RunID = invDifferent
			return e
		}},
		{"missing-attempt", 4, SM09, func(_ *State, _ *decisionCase, e []Event) []Event {
			fixtureValue[*ModelResponseRecordedEvent](e[0]).AttemptID = "missing"
			return e
		}},
		{"missing-batch-tool", 8, SM09, func(_ *State, _ *decisionCase, e []Event) []Event {
			fixtureValue[*ToolResultRecordedEvent](e[0]).ToolCallID = "missing"
			return e
		}},
		{"wrong-pairs", 8, SM07, func(_ *State, _ *decisionCase, e []Event) []Event {
			fixtureValue[*StepCompletedEvent](e[1]).OrderedResultRefs = nil
			return e
		}},
		{"wrong-result-ref", 8, SM07, func(_ *State, _ *decisionCase, e []Event) []Event {
			fixtureValue[*StepCompletedEvent](e[1]).OrderedResultRefs[0] = invDifferent
			return e
		}},
		{"wrong-response-ref", 8, SM07, func(_ *State, _ *decisionCase, e []Event) []Event {
			fixtureValue[*StepCompletedEvent](e[1]).ResponseRef = invDifferent
			return e
		}},
		{"lost-owner", 4, SM09, func(s *State, c *decisionCase, e []Event) []Event {
			s.OwnerIncarnation++
			c.facts.OwnerIncarnation = s.OwnerIncarnation
			return e
		}},
	}
}

func TestBatchEvidenceGuards(t *testing.T) {
	t.Parallel()
	for _, tc := range metadataBatchGuards() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, cases := traceState(t, tc.stage)
			c := cases[tc.stage]
			bindDecision(&c, s, "edge")
			events, err := Decide(s, c.command, &c.facts)
			if err != nil {
				t.Fatal(err)
			}
			events = tc.mutate(s, &c, events)
			if ids := CheckBatch(s, events, &c.facts); !slices.Contains(ids, tc.id) {
				t.Fatalf("missing %s: %v", tc.id, ids)
			}
		})
	}
}

func TestCheckerRejectsDuplicateCommittedEvents(t *testing.T) {
	t.Parallel()
	for _, stage := range []int{2, 3, 4, 7, 8, 11} {
		t.Run(strconv.Itoa(stage), func(t *testing.T) {
			t.Parallel()
			s, cases := traceState(t, stage)
			c := cases[stage]
			bindDecision(&c, s, "first")
			events, err := Decide(s, c.command, &c.facts)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range events {
				s = Evolve(s, e)
			}
			if ids := CheckBatch(s, events, &c.facts); !slices.Contains(ids, SM09) {
				t.Fatal(ids)
			}
		})
	}
}

func TestQueuedRebuildHasNoEffects(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 1)
	c := cases[0]
	bindDecision(&c, NewState(), "queued")
	events, err := Decide(nil, c.command, &c.facts)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(s, replayInvariantHistory(events)); diff != "" {
		t.Fatal(diff)
	}
	if ids := CheckInvariants(s); len(ids) > 0 {
		t.Fatal(ids)
	}
	if DeriveLifecycle(s) != LifecycleQueued {
		t.Fatal("queued run changed lifecycle")
	}
}

func TestApprovalResolutionBindsExactArguments(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 6)
	c := cases[6]
	bindDecision(&c, s, "pending")
	q := new(ToolApprovalRequestedEvent)
	q.Envelope = c.facts.Envelope
	q.ToolCallID = "t1"
	q.CheckGeneration = 1
	q.OwnerRequestRef = "binding-request"
	q.RequestRevision = 1
	q.ExactArgsRef = "binding-args"
	s = Evolve(s, q)
	bindDecision(&c, s, "approved")
	r := new(ToolApprovalResolvedEvent)
	r.Envelope = c.facts.Envelope
	r.ToolCallID = "t1"
	r.CheckGeneration = 1
	r.Resolution = "approved"
	r.OwnerResolutionEvidence = "owner-proof"
	ready := new(ToolReadyRecordedEvent)
	ready.Envelope = r.Envelope
	ready.Envelope.Sequence++
	ready.ToolCallID = "t1"
	ready.CheckGeneration = 1
	ready.ValidatedArgsRef = q.ExactArgsRef
	ready.GateObservation = "allowed"
	ready.OneUseApprovalReceiptRef = "one-use"
	if ids := CheckBatch(s, []Event{r, ready}, &c.facts); len(ids) > 0 {
		t.Fatal(ids)
	}
	ready.ValidatedArgsRef = "changed-args"
	if ids := CheckBatch(s, []Event{r, ready}, &c.facts); !slices.Contains(ids, SM02) {
		t.Fatal(ids)
	}
}

func TestHistoryChecksEveryBatch(t *testing.T) {
	t.Parallel()
	s := NewState()
	cases := traceCases()
	history := make([]InvariantBatch, 0, len(cases))
	for i := range cases {
		c := &cases[i]
		bindDecision(c, s, "history-"+strconv.Itoa(i))
		events, err := Decide(s, c.command, &c.facts)
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, InvariantBatch{Events: events, Facts: c.facts})
		for _, e := range events {
			s = Evolve(s, e)
		}
	}
	if ids := CheckHistory(history); len(ids) > 0 {
		t.Fatal(ids)
	}
	history[4].Facts.ExecutorFence++
	if ids := CheckHistory(history); !slices.Contains(ids, SM09) {
		t.Fatal(ids)
	}
}

func TestSnapshotCheckerHandlesNilEntries(t *testing.T) {
	t.Parallel()
	s := NewState()
	s.ModelInvocations["nil"] = nil
	s.ToolInvocations["nil"] = nil
	if ids := CheckBatch(s, []Event{nil}, nil); !slices.Contains(ids, SM09) {
		t.Fatal(ids)
	}
	s.TimeWindows["nil"] = nil
	s.Finished = true
	if ids := CheckInvariants(s); !slices.Contains(ids, SM05) {
		t.Fatal(ids)
	}
	next := Evolve(s, nil)
	if next.TimeWindows["nil"] != nil {
		t.Fatal("nil window changed")
	}
}
