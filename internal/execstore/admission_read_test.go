package execstore

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

const testContextStartRun = "start-run"

func TestReadAdmissionReturnsZeroRecordForNewSession(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestStore(t.Context(), t, nil)
	got, err := s.ReadAdmission(t.Context(), "new-session")
	if err != nil || got != (AdmissionRecord{}) {
		t.Fatal("missing admission must return the zero record")
	}
	var unavailable *Store
	if _, err = unavailable.ReadAdmission(t.Context(), "new-session"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatal("nil store must be unavailable")
	}
	ctx := t.Context()
	if err = s.DB().Close(); err != nil {
		t.Fatal("close database")
	}
	if _, err = s.ReadAdmission(ctx, "new-session"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatal("closed store must be unavailable")
	}
}

func TestReadAdmissionReflectsAdmitAndFinish(t *testing.T) {
	t.Parallel()
	s := setupFenceRun(t.Context(), t, budgetTime())
	got, err := s.ReadAdmission(t.Context(), testFenceSess)
	want := AdmissionRecord{SessionID: testFenceSess, ActiveRunID: testFenceRun1, OwnerIncarnation: 1, AdmissionToken: 5, Revision: 2}
	if err != nil || got != want {
		t.Fatal("admission must reflect the committed owner, token and revision")
	}
	event := makeRunFinished(makeEnvelope(testFenceRun1, 3, "finish", 5, budgetTime()), kernel.RunOutcomeCompleted)
	appendPersisted(t.Context(), t, s, &AppendRequest{
		SessionID: testFenceSess, RunID: testFenceRun1, CommandID: "finish", Kind: cmdAdvanceRun, Principal: testOriginUser, Target: testFenceRun1,
		CodecVersion: codec.CodecV1, MetadataInput: []byte(`{}`), AdmissionFence: &AdmissionFence{Token: 5, OwnerIncarnation: 1},
		ExpectedRunRevision: 2, ExpectedAdmissionRevision: 2, Events: []kernel.Event{event}, Result: codec.ReceiptResult{FirstSequence: 3, LastSequence: 3},
	})
	got, err = s.ReadAdmission(t.Context(), testFenceSess)
	want.ActiveRunID, want.Revision = "", 3
	if err != nil || got != want {
		t.Fatal("finish must clear active run and retain fencing history")
	}
}

func TestReadAdmissionReturnsContextStartRun(t *testing.T) {
	t.Parallel()
	for _, queuedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial-row", true: "existing-row"}[queuedFirst], func(t *testing.T) {
			t.Parallel()
			s, _, _ := newTestStore(t.Context(), t, nil)
			accepted := makeRunAccepted(makeEnvelope(testContextStartRun, 1, "accept", 0, budgetTime()), "context-session", "")
			admitted := makeRunAdmitted(makeEnvelope(testContextStartRun, 2, "admit", 1, budgetTime()), 1)
			admitted.ContextEpoch, admitted.InitialContextRevision, admitted.StartRunID = 1, 1, testContextStartRun
			req := &AppendRequest{SessionID: "context-session", RunID: testContextStartRun, CommandID: "admit", Kind: cmdAdmitRun, Principal: testOriginUser, Target: testContextStartRun, CodecVersion: codec.CodecV1, MetadataInput: []byte(`{}`), AdmissionFence: &AdmissionFence{Token: 1, OwnerIncarnation: 1}, ExpectedAdmissionRevision: 1, Events: []kernel.Event{accepted, admitted}, Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 2}}
			if queuedFirst {
				appendPersisted(t.Context(), t, s, &AppendRequest{SessionID: "context-session", RunID: testContextStartRun, CommandID: "accept", Kind: cmdAcceptRun, Principal: testOriginUser, Target: testContextStartRun, CodecVersion: codec.CodecV1, MetadataInput: []byte(`{}`), QueueFence: &QueueFence{}, ExpectedAdmissionRevision: 1, Events: []kernel.Event{accepted}, Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 1}})
				req.Events, req.ExpectedRunRevision, req.Result.FirstSequence = []kernel.Event{admitted}, 1, 2
			}
			appendPersisted(t.Context(), t, s, req)
			got, err := s.ReadAdmission(t.Context(), "context-session")
			if err != nil || got.ContextEpoch != 1 || got.ContextRevision != 1 || got.ContextStartRunID != testContextStartRun {
				t.Fatal("first admission must persist context continuity")
			}
		})
	}
}
