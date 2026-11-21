package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/pkg/client"
)

const (
	testInc1    = "inc-1"
	testInc2    = "inc-2"
	testSess1   = "sess-1"
	testSess2   = "sess-2"
	testReqA    = "req-a"
	testReqB    = "req-b"
	testReqC    = "req-c"
	testSummary = "Run command in workspace"
)

var errScriptedInbox = errors.New("scripted inbox error")

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type fakeInboxClient struct {
	mu      sync.Mutex
	inc     string
	list    client.PendingInteractionList
	listErr error
	ansErr  error
	answers []client.AnswerInteractionInput
}

func (f *fakeInboxClient) HostStatus(context.Context) (client.HostStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return client.HostStatus{Incarnation: f.inc}, nil
}

func (f *fakeInboxClient) ListPendingInteractions(context.Context) (client.PendingInteractionList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.list, f.listErr
}

func (f *fakeInboxClient) AnswerInteraction(_ context.Context, in *client.AnswerInteractionInput) (client.InteractionAnswerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, *in)
	if f.ansErr != nil {
		return client.InteractionAnswerResult{}, f.ansErr
	}
	return client.InteractionAnswerResult{RequestID: in.RequestID, Outcome: in.Outcome}, nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func awaitInboxResult(t *testing.T, w *Worker) Result {
	t.Helper()
	select {
	case res := <-w.Results():
		return res
	case <-t.Context().Done():
		t.Fatalf("context ended waiting for inbox result: %v", t.Context().Err())
		return Result{}
	}
}

func sampleConfirm(reqID, inc, sessID string) client.PendingInteraction {
	return client.PendingInteraction{
		RequestID:       reqID,
		Incarnation:     inc,
		SessionID:       sessID,
		Tool:            "shell",
		Kind:            client.InteractionKindConfirm,
		Risk:            "exec",
		EnvTag:          "dev",
		Summary:         testSummary,
		Detail:          `{"cmd":"echo ok"}`,
		OfferedOutcomes: []string{client.InteractionOutcomeAllowedOnce, client.InteractionOutcomeDenied},
	}
}

func TestInboxModelPinsRequestIDAndIncarnation(t *testing.T) {
	t.Parallel()
	m := NewModel()

	reqA := sampleConfirm(testReqA, testInc1, testSess1)
	m.ApplyListPending(testInc1, client.PendingInteractionList{
		Sequence: 5,
		Requests: []client.PendingInteraction{reqA},
	})
	pinA1 := Pin{RequestID: testReqA, Incarnation: testInc1}
	if !m.CanAnswer(pinA1) || m.PendingCount() != 1 || m.PendingCountForSession(testSess1) != 1 {
		t.Fatalf("after initial ListPending: count=%d sess1=%d can=%v", m.PendingCount(), m.PendingCountForSession(testSess1), m.CanAnswer(pinA1))
	}

	assertOldIncarnationIgnoredAndDropped(t, m, pinA1)
	assertWorkerSendsExactPinnedPair(t, Pin{RequestID: testReqA, Incarnation: testInc2})
}

func assertOldIncarnationIgnoredAndDropped(t *testing.T, m *Model, pinA1 Pin) {
	t.Helper()
	oldReq := sampleConfirm(testReqB, "inc-0", testSess2)
	if err := m.ApplyTopicEvent(6, client.EventInteractionRequested, mustJSON(t, oldReq)); err != nil {
		t.Fatal(err)
	}
	if m.CanAnswer(Pin{RequestID: testReqB, Incarnation: "inc-0"}) {
		t.Fatal("accepted event from older incarnation")
	}

	reqA2 := sampleConfirm(testReqA, testInc2, testSess1)
	if err := m.ApplyTopicSnapshot(testInc2, 1, mustJSON(t, []client.PendingInteraction{reqA2})); err != nil {
		t.Fatal(err)
	}
	if m.CanAnswer(pinA1) {
		t.Fatal("old incarnation pin must never remain answerable after incarnation change")
	}
	if !m.CanAnswer(Pin{RequestID: testReqA, Incarnation: testInc2}) {
		t.Fatal("new incarnation pin should be answerable")
	}
}

func assertWorkerSendsExactPinnedPair(t *testing.T, pinA2 Pin) {
	t.Helper()
	fc := &fakeInboxClient{inc: testInc2}
	var notified atomic.Int32
	w := NewWorker(func() Client { return fc }, func() { notified.Add(1) })
	w.Start(t.Context())
	defer w.Stop()

	w.Do(&Request{Kind: OpAnswer, Pin: Pin{RequestID: testReqA}})
	if res := awaitInboxResult(t, w); !errors.Is(res.Err, errMissingPin) {
		t.Fatalf("missing incarnation pin err = %v, want errMissingPin", res.Err)
	}

	w.Do(&Request{
		Kind: OpAnswer,
		Pin:  pinA2,
		Answer: client.AnswerInteractionInput{
			RequestID:   "wrong-req",
			Incarnation: "wrong-inc",
			Outcome:     client.InteractionOutcomeDenied,
		},
	})
	res := awaitInboxResult(t, w)
	if res.Err != nil || res.Pin != pinA2 {
		t.Fatalf("OpAnswer result = %+v", res)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.answers) != 1 || fc.answers[0].RequestID != testReqA || fc.answers[0].Incarnation != testInc2 || fc.answers[0].CommandID == "" {
		t.Fatalf("recorded answer = %+v, want pinned (%s, %s) with minted command_id", fc.answers, testReqA, testInc2)
	}
	if notified.Load() < 2 {
		t.Fatalf("onResult called %d times, want >= 2", notified.Load())
	}
}

func TestInboxReconcilesEventsAndDisappearsOnExternalResolve(t *testing.T) {
	t.Parallel()
	m := NewModel()
	reqA := sampleConfirm(testReqA, testInc1, testSess1)
	reqB := sampleConfirm(testReqB, testInc1, testSess1)
	reqC := sampleConfirm(testReqC, testInc1, testSess2)

	if err := m.ApplyTopicSnapshot(testInc1, 10, mustJSON(t, []client.PendingInteraction{reqA})); err != nil {
		t.Fatal(err)
	}
	for i, req := range []client.PendingInteraction{reqB, reqC} {
		if err := m.ApplyTopicEvent(uint64(11+i), client.EventInteractionRequested, mustJSON(t, req)); err != nil {
			t.Fatal(err)
		}
	}
	assertInboxCounts(t, m, 3, 2, 1)

	// Late ListPending from seq 9 must not roll back seq 12 events.
	m.ApplyListPending(testInc1, client.PendingInteractionList{Sequence: 9, Requests: []client.PendingInteraction{reqA}})
	if m.PendingCount() != 3 || m.Sequence() != 12 {
		t.Fatalf("stale ListPending rolled back state: count=%d seq=%d", m.PendingCount(), m.Sequence())
	}

	assertExternalResolveDropsTicket(t, m, 13, testReqB, client.InteractionOutcomeExpired, testSess1, 1)
	// Q-161: external cancellation (turn end / vendor abort) drops the ticket.
	assertExternalResolveDropsTicket(t, m, 14, testReqC, client.InteractionOutcomeCancelled, testSess2, 0)

	m.ApplyListPending(testInc2, client.PendingInteractionList{Sequence: 0})
	if m.PendingCount() != 0 || m.Incarnation() != testInc2 {
		t.Fatalf("after empty new incarnation: count=%d inc=%q", m.PendingCount(), m.Incarnation())
	}
}

func assertExternalResolveDropsTicket(t *testing.T, m *Model, seq uint64, reqID, outcome, sessID string, wantSessCount int) {
	t.Helper()
	payload := mustJSON(t, client.InteractionResolvedEvent{RequestID: reqID, Outcome: outcome})
	if err := m.ApplyTopicEvent(seq, client.EventInteractionResolved, payload); err != nil {
		t.Fatal(err)
	}
	if m.CanAnswer(Pin{RequestID: reqID, Incarnation: testInc1}) || m.PendingCountForSession(sessID) != wantSessCount {
		t.Fatalf("resolved(%s) ticket remained in inbox: count=%d sess=%d", outcome, m.PendingCount(), m.PendingCountForSession(sessID))
	}
}

func assertInboxCounts(t *testing.T, m *Model, total, s1, s2 int) {
	t.Helper()
	if m.PendingCount() != total || m.PendingCountForSession(testSess1) != s1 || m.PendingCountForSession(testSess2) != s2 {
		t.Fatalf("counts = total:%d s1:%d s2:%d, want %d/%d/%d", m.PendingCount(), m.PendingCountForSession(testSess1), m.PendingCountForSession(testSess2), total, s1, s2)
	}
}

func TestInboxWorkerDisconnectedAndListPendingEmptyIncarnation(t *testing.T) {
	t.Parallel()
	wNil := NewWorker(func() Client { return nil }, nil)
	wNil.Start(t.Context())
	defer wNil.Stop()

	wNil.Do(&Request{Kind: OpListPending})
	if res := awaitInboxResult(t, wNil); !errors.Is(res.Err, client.ErrDisconnected) {
		t.Fatalf("nil client err = %v, want ErrDisconnected", res.Err)
	}

	fc := &fakeInboxClient{inc: testInc2, list: client.PendingInteractionList{Sequence: 4}}
	w := NewWorker(func() Client { return fc }, nil)
	w.Start(t.Context())
	defer w.Stop()

	w.Do(&Request{Kind: OpListPending})
	res := awaitInboxResult(t, w)
	if res.Err != nil || res.Incarnation != testInc2 || res.List.Sequence != 4 {
		t.Fatalf("OpListPending empty list result = %+v", res)
	}

	fc.mu.Lock()
	fc.listErr = errScriptedInbox
	fc.mu.Unlock()
	w.Do(&Request{Kind: OpListPending})
	if res = awaitInboxResult(t, w); !errors.Is(res.Err, errScriptedInbox) {
		t.Fatalf("scripted error = %v, want %v", res.Err, errScriptedInbox)
	}
}
