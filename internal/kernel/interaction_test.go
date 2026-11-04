package kernel

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const interactionArgsRef = "interaction-args"

func interactionState() *State {
	s := NewState()
	s.Accepted, s.Admitted = true, true
	s.RunID, s.AdmissionToken, s.OwnerIncarnation = "interaction-run", 1, 1
	return s
}

// pendingRequest adds a tool whose current approval request has the given kind.
// The attempt sits in AwaitingApproval unless the status is overridden.
func pendingRequest(s *State, id string, kind RequestKind, status ToolAttemptStatus) {
	q := new(ToolApprovalRequestedEvent)
	q.ToolCallID = id
	q.CheckGeneration = 1
	q.OwnerRequestRef = "req-" + id
	q.RequestKind = kind
	q.RequestRevision = 1
	q.ExactArgsRef = interactionArgsRef
	s.ToolInvocations[id] = &ToolInvocation{
		ToolCallID: id, CheckGeneration: 1, Approval: *q,
		Attempts: map[string]*ToolAttempt{id + "-a": {AttemptID: id + "-a", Status: status}},
	}
}

func TestQuestionRequestWaitsForUserInput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind RequestKind
		want []WaitReason
	}{
		{RequestKindConfirm, []WaitReason{WaitReasonApproval}},
		{RequestKindSelect, []WaitReason{WaitReasonApproval}},
		{RequestKindQuestion, []WaitReason{WaitReasonUserInput}},
		{"", []WaitReason{WaitReasonApproval}},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			t.Parallel()
			s := interactionState()
			pendingRequest(s, "t1", tc.kind, ToolAttemptStatusAwaitingApproval)
			if diff := cmp.Diff(tc.want, DeriveWaitReasons(s)); diff != "" {
				t.Fatalf("DeriveWaitReasons mismatch (-want +got):\n%s", diff)
			}
			if got := DeriveLifecycle(s); got != LifecycleWaiting {
				t.Fatalf("lifecycle = %s, want waiting", got)
			}
		})
	}
}

func TestMixedRequestsDeriveBothReasons(t *testing.T) {
	t.Parallel()
	s := interactionState()
	pendingRequest(s, "t1", RequestKindQuestion, ToolAttemptStatusAwaitingApproval)
	pendingRequest(s, "t2", RequestKindSelect, ToolAttemptStatusAwaitingApproval)
	want := []WaitReason{WaitReasonApproval, WaitReasonUserInput}
	if diff := cmp.Diff(want, DeriveWaitReasons(s)); diff != "" {
		t.Fatalf("DeriveWaitReasons mismatch (-want +got):\n%s", diff)
	}
}

func TestInteractionBlocksMatchRequestKind(t *testing.T) {
	t.Parallel()
	block := func(kind WaitReason, ref string) *Block {
		return &Block{BlockerID: "b-" + ref, Kind: kind, Reference: ref, ResumeCondition: "answered"}
	}
	for _, tc := range []struct {
		name   string
		kind   RequestKind
		block  *Block
		wantOK bool
	}{
		{"approval block for confirm", RequestKindConfirm, block(WaitReasonApproval, "req-t1"), true},
		{"approval block for select", RequestKindSelect, block(WaitReasonApproval, "req-t1"), true},
		{"user_input block for question", RequestKindQuestion, block(WaitReasonUserInput, "req-t1"), true},
		{"approval block for question", RequestKindQuestion, block(WaitReasonApproval, "req-t1"), false},
		{"user_input block for confirm", RequestKindConfirm, block(WaitReasonUserInput, "req-t1"), false},
		{"user_input block for select", RequestKindSelect, block(WaitReasonUserInput, "req-t1"), false},
		{"user_input block without request", RequestKindQuestion, block(WaitReasonUserInput, "other"), false},
		{"approval block without request", RequestKindConfirm, block(WaitReasonApproval, "other"), false},
		{"user_input block without reference", RequestKindQuestion, block(WaitReasonUserInput, ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := interactionState()
			pendingRequest(s, "t1", tc.kind, ToolAttemptStatusAwaitingApproval)
			s.Blocks[tc.block.BlockerID] = tc.block
			if got := !slices.Contains(CheckInvariants(s), SM02); got != tc.wantOK {
				t.Fatalf("SM-02 satisfied = %v, want %v", got, tc.wantOK)
			}
		})
	}
}

func TestInteractionBlocksRejectSharedRequest(t *testing.T) {
	t.Parallel()
	s := interactionState()
	pendingRequest(s, "t1", RequestKindQuestion, ToolAttemptStatusAwaitingApproval)
	s.Blocks["b1"] = &Block{BlockerID: "b1", Kind: WaitReasonUserInput, Reference: "req-t1", ResumeCondition: "c"}
	s.Blocks["b2"] = &Block{BlockerID: "b2", Kind: WaitReasonUserInput, Reference: "req-t1", ResumeCondition: "c"}
	if ids := CheckInvariants(s); !slices.Contains(ids, SM02) {
		t.Fatalf("two blocks on one request passed SM-02: %v", ids)
	}
}

func TestPendingRequestRequiresItsOwnWaitReason(t *testing.T) {
	t.Parallel()
	for _, kind := range []RequestKind{RequestKindConfirm, RequestKindQuestion} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			s := interactionState()
			pendingRequest(s, "t1", kind, ToolAttemptStatusAwaitingApproval)
			if ids := CheckInvariants(s); slices.Contains(ids, SM03) {
				t.Fatalf("waiting request violates SM-03: %v", ids)
			}
			s.ToolInvocations["t1"].Attempts["t1-a"].Status = ToolAttemptStatusChecking
			if ids := CheckInvariants(s); !slices.Contains(ids, SM03) {
				t.Fatalf("request without wait reason passed SM-03: %v", ids)
			}
		})
	}
}

func TestPendingQuestionIsNotSatisfiedByAnApprovalWait(t *testing.T) {
	t.Parallel()
	s := interactionState()
	pendingRequest(s, "t1", RequestKindConfirm, ToolAttemptStatusAwaitingApproval)
	pendingRequest(s, "t2", RequestKindQuestion, ToolAttemptStatusChecking)
	if ids := CheckInvariants(s); !slices.Contains(ids, SM03) {
		t.Fatalf("question waiting only on approval passed SM-03: %v", ids)
	}
}

func TestPendingRequestNeedsAKnownKind(t *testing.T) {
	t.Parallel()
	for _, kind := range []RequestKind{"", "dialog"} {
		s := interactionState()
		pendingRequest(s, "t1", kind, ToolAttemptStatusAwaitingApproval)
		if ids := CheckInvariants(s); !slices.Contains(ids, SM02) {
			t.Fatalf("request kind %q passed SM-02: %v", kind, ids)
		}
	}
}

func TestBlocksCarryTheirBudgetKind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		kind   WaitReason
		budget BudgetKind
		wantOK bool
	}{
		{string(BudgetKindSteps), WaitReasonBudget, BudgetKindSteps, true},
		{string(BudgetKindTokens), WaitReasonBudget, BudgetKindTokens, true},
		{string(BudgetKindTime), WaitReasonBudget, BudgetKindTime, true},
		{string(BudgetKindToolCalls), WaitReasonBudget, BudgetKindToolCalls, true},
		{"budget without kind", WaitReasonBudget, "", false},
		{"budget with unknown kind", WaitReasonBudget, "money", false},
		{"quota_refresh", WaitReasonQuotaRefresh, "", true},
		{"quota_refresh with budget kind", WaitReasonQuotaRefresh, BudgetKindSteps, false},
		{"vault_locked with unknown kind", WaitReasonVaultLocked, "money", false},
		{"retired dependency", "dependency", "", false},
		{"unknown reason", "weather", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := interactionState()
			s = Evolve(s, &RunBlockAddedEvent{Envelope: Envelope{}, BlockerID: "b", Kind: tc.kind, BudgetKind: tc.budget, Reference: "r", ResumeCondition: "c"})
			if got := s.Blocks["b"].BudgetKind; got != tc.budget {
				t.Fatalf("block budget kind = %q, want %q", got, tc.budget)
			}
			if got := !slices.Contains(CheckInvariants(s), SM09); got != tc.wantOK {
				t.Fatalf("SM-09 satisfied = %v, want %v", got, tc.wantOK)
			}
		})
	}
}

func TestNilBlockIsInvalid(t *testing.T) {
	t.Parallel()
	s := interactionState()
	s.Blocks["b"] = nil
	if ids := CheckInvariants(s); !slices.Contains(ids, SM09) {
		t.Fatalf("nil block passed: %v", ids)
	}
}

func TestRetiredWaitReasonsDeriveNothing(t *testing.T) {
	t.Parallel()
	s := interactionState()
	s.Blocks["dep"] = &Block{BlockerID: "dep", Kind: "dependency", Reference: "r", ResumeCondition: "c"}
	if got := DeriveWaitReasons(s); len(got) != 0 {
		t.Fatalf("retired reasons derived %v", got)
	}
}
