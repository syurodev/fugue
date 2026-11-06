package coordinator

import (
	"testing"

	"github.com/syurodev/fugue/internal/codec"
)

func TestFirstAppendOnFreshSessionExpectsAdmissionRevisionOne(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = Admission{SessionID: testSessionID, OwnerIncarnation: 1}
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "fresh-expectation", 1)
	defer sess.Stop()

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept: %v", err)
	}

	first := store.transitionList()[0]
	if first.ExpectedAdmissionRevision != 1 {
		t.Fatalf("first append on a session with no admission row expects revision %d, want 1", first.ExpectedAdmissionRevision)
	}
}

func TestAdmitMetadataIsCanonicalTokenAndContextEpoch(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = idleAdmission()
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "admit-metadata", 1)
	defer sess.Stop()

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept: %v", err)
	}

	admit := admitTransition(store)
	if admit == nil {
		t.Fatal("the run was not admitted")
	}
	if got, want := string(admit.MetadataInput), `{"token":1,"ContextEpoch":3}`; got != want {
		t.Fatalf("admit metadata %s, want %s", got, want)
	}
	if err := codec.ValidateCanonicalJSON(admit.MetadataInput); err != nil {
		t.Fatalf("admit metadata is not canonical JSON: %v", err)
	}
}
