package execstore

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

func appendBlockRun(t *testing.T, s *Store, runID string, kind kernel.WaitReason, budget kernel.BudgetKind) {
	t.Helper()
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	const cmd = "cmd-block"
	block := &kernel.RunBlockAddedEvent{
		Envelope: *makeEnvelope(runID, 3, cmd, 1, now), BlockerID: "b", Kind: kind, BudgetKind: budget,
		Reference: "ref", ResumeCondition: "later",
	}
	_, err := s.Append(t.Context(), &AppendRequest{
		SessionID: "sess-block", RunID: runID, CommandID: cmd, Kind: testKindAcceptRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		AdmissionFence: &AdmissionFence{Token: 1, OwnerIncarnation: 1}, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{
			makeRunAccepted(makeEnvelope(runID, 1, cmd, 1, now), "sess-block", ""),
			makeRunAdmitted(makeEnvelope(runID, 2, cmd, 1, now), 1),
			block,
		},
		Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 3, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
}

func TestRebuildRunValidatesBlockKinds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		kind    kernel.WaitReason
		budget  kernel.BudgetKind
		corrupt bool
	}{
		{"budget with kind", kernel.WaitReasonBudget, kernel.BudgetKindTokens, false},
		{"quota refresh", kernel.WaitReasonQuotaRefresh, "", false},
		{"budget without kind", kernel.WaitReasonBudget, "", true},
		{"retired dependency", "dependency", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _, _ := newTestStore(t.Context(), t, nil)
			appendBlockRun(t, s, "run-block", tc.kind, tc.budget)
			state, err := s.RebuildRun(t.Context(), "run-block")
			if tc.corrupt != errors.Is(err, ErrCorruptHistory) || (!tc.corrupt && err != nil) {
				t.Fatalf("RebuildRun error = %v, corrupt expected %v", err, tc.corrupt)
			}
			if tc.corrupt {
				return
			}
			if got := kernel.DeriveWaitReasons(state); !slices.Equal(got, []kernel.WaitReason{tc.kind}) {
				t.Fatalf("wait reasons = %v, want [%s]", got, tc.kind)
			}
			if got := state.Blocks["b"].BudgetKind; got != tc.budget {
				t.Fatalf("budget kind = %q, want %q", got, tc.budget)
			}
		})
	}
}
