package execstore

import (
	"slices"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	budgetRun     = "run-budget"
	budgetSession = "sess-budget"
	budgetBlocker = "budget:steps"
)

func budgetTime() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC) }

// appendStepBudgetWait commits the batch a one-step run produces when its only
// step completes: the step closes with continuation=budget and a budget(steps)
// block follows. The batch is metadata-only, as the coordinator's would be for
// the events that carry no payload.
func appendStepBudgetWait(t *testing.T, s *Store) {
	t.Helper()
	const cmd = "cmd-wait"
	now := budgetTime()
	accepted := makeRunAccepted(makeEnvelope(budgetRun, 1, cmd, 1, now), budgetSession, "")
	accepted.Budgets = kernel.RunBudgets{Steps: 1, Tokens: 2_000_000, ToolCalls: 500, ActiveTime: 2 * time.Hour}
	events := []kernel.Event{
		accepted,
		makeRunAdmitted(makeEnvelope(budgetRun, 2, cmd, 1, now), 1),
		makeStepStarted(makeEnvelope(budgetRun, 3, cmd, 1, now), 1),
		&kernel.StepCompletedEvent{
			Envelope: *makeEnvelope(budgetRun, 4, cmd, 1, now), StepIndex: 1, ResponseRef: "response-1", OrderedResultRefs: nil,
			Continuation: kernel.StepContinuationContinue, Reason: kernel.ContinuationReasonBudget, BudgetKind: kernel.BudgetKindSteps,
		},
		&kernel.RunBlockAddedEvent{
			Envelope: *makeEnvelope(budgetRun, 5, cmd, 1, now), BlockerID: budgetBlocker, Kind: kernel.WaitReasonBudget,
			BudgetKind: kernel.BudgetKindSteps, Reference: "", ResumeCondition: "budget_extended",
		},
	}
	outcome, err := s.Append(t.Context(), &AppendRequest{
		SessionID: budgetSession, RunID: budgetRun, CommandID: cmd, Kind: testKindAcceptRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		AdmissionFence: &AdmissionFence{Token: 1, OwnerIncarnation: 1}, ExpectedAdmissionRevision: 1,
		Events: events, Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 5, RejectionCode: "", Message: ""},
	})
	requireCommitted(t, outcome.Committed, err)
}

// reopenWithLockedVault closes the store and opens the same data directory the
// way a restarted host does: a new vault service that starts locked.
func reopenWithLockedVault(t *testing.T, s *Store, dataDir string) *Store {
	t.Helper()
	ctx := t.Context()
	closeStore(t, s)
	reopenedDB, err := storage.Open(ctx, storage.DBPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, reopenedDB)
	locked, err := vault.NewService(ctx, vault.NewSQLKeyringStore(reopenedDB), ids.NewSequence("vault-inc-restart-"))
	if err != nil {
		t.Fatal(err)
	}
	if locked.Snapshot().State != vault.StateLocked {
		t.Fatalf("restarted vault state = %v", locked.Snapshot().State)
	}
	clk := clock.NewFake(budgetTime().Add(time.Hour))
	host := storage.HostIncarnation{LockGeneration: 2, IncarnationID: "host-inc-restart", AcquiredAt: clk.Now()}
	reopened, err := New(ctx, reopenedDB, locked, host, clk, ids.NewSequence("restart-id-"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeStore(t, reopened) })
	return reopened
}

func TestBudgetWaitSurvivesStoreReopen(t *testing.T) {
	t.Parallel()
	s, _, dataDir, _ := newVaultAndStore(t)
	appendStepBudgetWait(t, s)
	// The budget wait is metadata: it must be readable while the vault is locked.
	reopened := reopenWithLockedVault(t, s, dataDir)

	assertStepBudgetWait(t, reopened)
}

func assertStepBudgetWait(t *testing.T, s *Store) {
	t.Helper()
	state, err := s.RebuildRun(t.Context(), budgetRun)
	if err != nil {
		t.Fatalf("RebuildRun: %v", err)
	}
	block := state.Blocks[budgetBlocker]
	if block == nil || block.Kind != kernel.WaitReasonBudget || block.BudgetKind != kernel.BudgetKindSteps || len(state.Blocks) != 1 {
		t.Fatalf("rebuilt blocks = %+v", state.Blocks)
	}
	if state.StepsUsed != 1 || state.BudgetLimit(kernel.BudgetKindSteps) != 1 || state.Finished {
		t.Fatalf("rebuilt counters: %d steps, limit %d, finished %v", state.StepsUsed, state.BudgetLimit(kernel.BudgetKindSteps), state.Finished)
	}
	assertWaitingRecord(t, s)
}

func assertWaitingRecord(t *testing.T, s *Store) {
	t.Helper()
	record, err := s.ReadRun(t.Context(), budgetRun)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if record.Lifecycle != kernel.LifecycleWaiting || !slices.Equal(record.WaitReasons, []kernel.WaitReason{kernel.WaitReasonBudget}) ||
		record.Outcome != "" {
		t.Fatalf("run record: lifecycle %s, waits %v, outcome %q", record.Lifecycle, record.WaitReasons, record.Outcome)
	}
}

func TestExtendBudgetIsMetadataOnly(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, v, _, _ := newVaultAndStore(t)
	appendStepBudgetWait(t, s)
	v.Lock()

	const cmd = "cmd-extend"
	now := budgetTime().Add(time.Minute)
	input := []byte(`{"ExtensionAmount":50,"Actor":"` + testOriginUser + `","Kind":"steps"}`)
	outcome, err := s.Append(ctx, &AppendRequest{
		SessionID: budgetSession, RunID: budgetRun, CommandID: cmd, Kind: cmdExtendBudget, Principal: testOriginUser, Target: budgetRun,
		CodecVersion: codec.CodecV1, MetadataInput: input, CanonicalInput: input,
		AdmissionFence: &AdmissionFence{Token: 1, OwnerIncarnation: 1}, ExpectedRunRevision: 1, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{
			&kernel.BudgetExtendedEvent{Envelope: *makeEnvelope(budgetRun, 6, cmd, 1, now), Kind: kernel.BudgetKindSteps, ExtensionAmount: 50, Actor: testOriginUser},
			&kernel.RunBlockClearedEvent{Envelope: *makeEnvelope(budgetRun, 7, cmd, 1, now), BlockerID: budgetBlocker, ResolvingEvidence: "budget_extended"},
		},
		Result: codec.ReceiptResult{FirstSequence: 6, LastSequence: 7, RejectionCode: "", Message: ""},
	})
	requireCommitted(t, outcome.Committed, err)

	state, err := s.RebuildRun(ctx, budgetRun)
	if err != nil {
		t.Fatalf("RebuildRun: %v", err)
	}
	if state.BudgetLimit(kernel.BudgetKindSteps) != 51 || len(state.Blocks) != 0 || state.StepsUsed != 1 {
		t.Fatalf("limit %d, blocks %d, steps used %d", state.BudgetLimit(kernel.BudgetKindSteps), len(state.Blocks), state.StepsUsed)
	}
	record, err := s.ReadRun(ctx, budgetRun)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if record.Lifecycle != kernel.LifecycleRunning || len(record.WaitReasons) != 0 {
		t.Fatalf("run record after extension: lifecycle %s, waits %v", record.Lifecycle, record.WaitReasons)
	}
}
