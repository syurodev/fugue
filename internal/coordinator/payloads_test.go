package coordinator

import (
	"bytes"
	"errors"
	"testing"
)

func TestCoordinatorPassesPayloadsThrough(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "payloads", 1)
	defer sess.Stop()
	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	req.Payloads = []Payload{{ID: "input-r1", Kind: "user_input", Plaintext: []byte("sample input")}}
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatal("submit payload input")
	}
	transitions := store.transitionList()
	got := transitions[0].Payloads
	if len(got) != 1 || got[0].ID != "input-r1" || got[0].Kind != "user_input" || !bytes.Equal(got[0].Plaintext, []byte("sample input")) {
		t.Fatal("coordinator must carry request payloads unchanged")
	}
}

func TestCoordinatorPassesObservationPayloadsThrough(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	setupDispatchedRun(store, 1)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "observation-payloads", 1)
	defer sess.Stop()
	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "dispatch")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatal("dispatch")
	}
	obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "response", 1, 1, 5)
	obs.Payloads = []Payload{{ID: "resp-a1", Kind: "model_response", Plaintext: []byte("sample response")}}
	if _, err := sess.SubmitObservation(ctx, &obs); err != nil {
		t.Fatal("observe")
	}
	transitions := store.transitionList()
	got := transitions[len(transitions)-1].Payloads
	if len(got) != 1 || got[0].ID != "resp-a1" || got[0].Kind != "model_response" || !bytes.Equal(got[0].Plaintext, []byte("sample response")) {
		t.Fatal("coordinator must carry observation payloads unchanged")
	}
}

func TestConfirmedCommitWithUnavailableRevisionsReloadsBeforeNextAppend(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	setupAdmittedRun(store)
	store.queueAppend(&AppendResult{RevisionsUnavailable: true}, nil)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "stale-revisions", 1)
	defer sess.Stop()
	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "dispatch")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatal("confirmed dispatch must succeed")
	}
	store.mu.Lock()
	store.admissionErr = errAdmLoadBoom
	store.mu.Unlock()
	next := acceptRunRequest(testRunID2, testCmdAcceptR2, "m2", "a2")
	_, err := sess.Submit(ctx, &next)
	if !errors.Is(err, errAdmLoadBoom) || store.appendCount() != 1 {
		t.Fatal("stale revision must block next append until admission reload succeeds")
	}
	store.mu.Lock()
	store.admissionErr = nil
	store.mu.Unlock()
	if _, err = sess.Submit(ctx, &next); err != nil {
		t.Fatal("retry after reload")
	}
	transitions := store.transitionList()
	if transitions[1].ExpectedAdmissionRevision != 2 {
		t.Fatal("next append must use the revision read from storage")
	}
}

func TestUnavailableRevisionsRemainStaleAfterAdmissionCallback(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.queueAppend(&AppendResult{RunRevision: 1, AdmissionRevision: 1}, nil)
	store.queueAppend(&AppendResult{RevisionsUnavailable: true}, nil)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "admission-revisions", 1)
	defer sess.Stop()
	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatal("accept and admission must succeed")
	}
	store.mu.Lock()
	store.admissionErr = errAdmLoadBoom
	store.mu.Unlock()
	next := acceptRunRequest(testRunID2, testCmdAcceptR2, "m2", "a2")
	if _, err := sess.Submit(ctx, &next); !errors.Is(err, errAdmLoadBoom) || store.appendCount() != 2 {
		t.Fatal("admission callback must not make unavailable revisions appear fresh")
	}
}
