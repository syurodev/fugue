package vendorstore

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/vault"
)

func seedMailboxTeam(t *testing.T, s *Store) TeamRecord {
	t.Helper()
	ctx := t.Context()
	rec, _ := createDecideTeam(t, s, "team-mb")
	seq, err := s.DecideTeam(ctx, rec.TeamID, &TeamDecisionInput{
		ExpectedSeq:     rec.LastSeq,
		DecisionEventID: "ev-mb-decide",
		StartedPayload:  []byte(`{"decision":"approve"}`),
		Mailbox: []MailboxEnqueueInput{{
			RecipientTaskID: decideTestLeadTask,
			Kind:            MailboxKindDecision,
			Payload:         []byte(`{"decision":"approve"}`),
		}},
	})
	if err != nil {
		t.Fatalf("DecideTeam: %v", err)
	}
	if _, appendErr := s.AppendTeamEvent(ctx, rec.TeamID, seq, &AppendTeamEventInput{
		EventID:   "ev-mb-note",
		TaskID:    decideTestLeadTask,
		EventType: EventTaskAssigned,
		SessionID: "sess-mb",
		Mailbox: []MailboxEnqueueInput{{
			RecipientTaskID: decideTestLeadTask,
			Kind:            MailboxKindDigest,
			Payload:         []byte(`{"task_id":"t1","state":"done"}`),
		}},
	}); appendErr != nil {
		t.Fatalf("AppendTeamEvent: %v", appendErr)
	}
	return rec
}

func TestTeamMailboxEnqueueClaimReleaseDeliver(t *testing.T) {
	t.Parallel()
	s, v, _ := protectedStore(t)
	ctx := t.Context()
	rec := seedMailboxTeam(t, s)

	// PendingMailbox is metadata-only and readable while the vault is locked (INV-09).
	v.Lock()
	pending, err := s.PendingMailbox(ctx)
	if err != nil || len(pending) != 1 || !pending[0].HasImmediate {
		t.Fatalf("PendingMailbox locked = %+v, %v", pending, err)
	}
	if unlockErr := v.Unlock(ctx, redact.NewSecret("master-pw")); unlockErr != nil {
		t.Fatalf("Unlock: %v", unlockErr)
	}

	claimed, err := s.ClaimMailbox(ctx, rec.TeamID, decideTestLeadTask)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("ClaimMailbox = %d, %v; want 2", len(claimed), err)
	}
	assertMailboxPayload(t, s, v, rec.TeamID, claimed)
	assertMailboxReleaseAndDeliver(t, s, rec.TeamID, claimed[0].BatchID)
}

func assertMailboxPayload(t *testing.T, s *Store, v *vault.Service, teamID string, claimed []MailboxMessage) {
	t.Helper()
	ctx := t.Context()
	batchID := claimed[0].BatchID
	if batchID == "" || claimed[1].BatchID != batchID {
		t.Fatalf("batch IDs = (%q, %q), want same non-empty batch ID", batchID, claimed[1].BatchID)
	}
	delivering, err := s.DeliveringMailboxBatches(ctx)
	if err != nil || len(delivering) != 1 || delivering[0].BatchID != batchID {
		t.Fatalf("DeliveringMailboxBatches = %+v, %v", delivering, err)
	}
	payload, err := s.OpenMailboxPayload(ctx, teamID, claimed[0].MessageID)
	if err != nil || string(payload) != `{"decision":"approve"}` {
		t.Fatalf("OpenMailboxPayload = %q, %v", payload, err)
	}
	v.Lock()
	if _, lockErr := s.OpenMailboxPayload(ctx, teamID, claimed[0].MessageID); lockErr == nil {
		t.Fatal("OpenMailboxPayload while locked should fail")
	}
	if unlockErr := v.Unlock(ctx, redact.NewSecret("master-pw")); unlockErr != nil {
		t.Fatalf("Unlock 2: %v", unlockErr)
	}
}

func assertMailboxReleaseAndDeliver(t *testing.T, s *Store, teamID, batchID string) {
	t.Helper()
	ctx := t.Context()
	if emptyClaim, claimErr := s.ClaimMailbox(ctx, teamID, "nonexistent"); claimErr != nil || len(emptyClaim) != 0 {
		t.Fatalf("empty ClaimMailbox = %+v, %v", emptyClaim, claimErr)
	}
	if releaseErr := s.ReleaseMailboxBatch(ctx, batchID); releaseErr != nil {
		t.Fatalf("ReleaseMailboxBatch: %v", releaseErr)
	}
	reclaimed, err := s.ClaimMailbox(ctx, teamID, decideTestLeadTask)
	if err != nil || len(reclaimed) != 2 || reclaimed[0].BatchID != batchID {
		t.Fatalf("re-ClaimMailbox = %+v, %v; want same batchID %q", reclaimed, err, batchID)
	}
	if deliverErr := s.MarkMailboxBatchDelivered(ctx, batchID); deliverErr != nil {
		t.Fatalf("MarkMailboxBatchDelivered: %v", deliverErr)
	}
	if empty, pendErr := s.PendingMailbox(ctx); pendErr != nil || len(empty) != 0 {
		t.Fatalf("PendingMailbox after deliver = %+v, %v", empty, pendErr)
	}
	if empty, delErr := s.DeliveringMailboxBatches(ctx); delErr != nil || len(empty) != 0 {
		t.Fatalf("DeliveringMailboxBatches after deliver = %+v, %v", empty, delErr)
	}
}

func TestTeamMailboxRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	ctx := t.Context()
	rec, _ := createDecideTeam(t, s, "team-mb-bad")

	for _, tc := range []struct {
		name string
		msg  MailboxEnqueueInput
	}{
		{name: "unknown kind", msg: MailboxEnqueueInput{RecipientTaskID: decideTestLeadTask, Kind: "bogus", Payload: []byte("x")}},
		{name: "missing recipient", msg: MailboxEnqueueInput{Kind: MailboxKindAnswer, Payload: []byte("x")}},
		{name: "empty payload", msg: MailboxEnqueueInput{RecipientTaskID: decideTestLeadTask, Kind: MailboxKindAnswer}},
	} {
		_, err := s.DecideTeam(ctx, rec.TeamID, &TeamDecisionInput{
			ExpectedSeq:     rec.LastSeq,
			DecisionEventID: "ev-" + tc.name,
			StartedPayload:  []byte(`{"decision":"approve"}`),
			Mailbox:         []MailboxEnqueueInput{tc.msg},
		})
		if !errors.Is(err, ErrTeamInvalidInput) {
			t.Errorf("%s: err = %v, want ErrTeamInvalidInput", tc.name, err)
		}
	}
}
