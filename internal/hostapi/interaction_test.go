package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	interactionv1 "github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1/interactionv1connect"
)

func newInteractionClient(t *testing.T, sockPath string) (interactionv1connect.InteractionServiceClient, hostv1connect.HostServiceClient, *http.Client) {
	t.Helper()
	hostClient, httpClient := newTestClient(t, sockPath)
	interClient := interactionv1connect.NewInteractionServiceClient(httpClient, "http://fugue")
	return interClient, hostClient, httpClient
}

func setupInteractionServer(t *testing.T) (*Server, *fakeBackend, string) {
	t.Helper()
	backend := newFakeBackend()
	backend.status = fakeStatus()
	dir := shortDir(t)
	srv := startTestServer(t, dir, backend)
	return srv, backend, srv.cfg.Socket.Path
}

const (
	testSessionID   = "sess-1"
	testKindConfirm = "confirm"
	testOpQ2        = "op-q2"
	testColorRed    = "red"
)

func addTestConfirm(backend *fakeBackend, reqID, summary string) string {
	backend.pendingMu.Lock()
	defer backend.pendingMu.Unlock()
	backend.pendingSeq++
	backend.pending[reqID] = app.PendingInteractionView{
		RequestID:       reqID,
		Incarnation:     backend.inc,
		SessionID:       testSessionID,
		Tool:            "file_writer",
		Kind:            testKindConfirm,
		Risk:            "write",
		EnvTag:          "dev",
		Summary:         summary,
		OfferedOutcomes: []string{app.OutcomeAllowedOnce, app.OutcomeAllowedSession, app.OutcomeDenied},
		GrantScopeLabel: "workspace write",
	}
	if backend.hub != nil {
		if payload, err := json.Marshal(backend.pending[reqID]); err == nil {
			if _, perr := backend.hub.Publish("interaction", app.Event{
				Seq:     backend.pendingSeq,
				Type:    "requested",
				Payload: payload,
			}); perr != nil {
				return ""
			}
		}
	}
	return reqID
}

func addTestRelayed(backend *fakeBackend, reqID, summary string) string {
	backend.pendingMu.Lock()
	defer backend.pendingMu.Unlock()
	backend.pendingSeq++
	backend.pending[reqID] = app.PendingInteractionView{
		RequestID:       reqID,
		Incarnation:     backend.inc,
		SessionID:       testSessionID,
		Tool:            "bash",
		Kind:            testKindConfirm,
		Summary:         summary,
		OfferedOutcomes: []string{app.OutcomeAnswered, app.OutcomeDenied},
		Options: []app.InteractionOption{
			{ID: "allow", Label: "Allow"},
			{ID: "deny", Label: "Deny"},
		},
	}
	return reqID
}

func assertConnectCode(t *testing.T, err error, want connect.Code, wantCode errcode.Code) {
	t.Helper()
	if connect.CodeOf(err) != want {
		t.Fatalf("error code = %v, want %v (err: %v)", connect.CodeOf(err), want, err)
	}
	if wantCode == "" {
		return
	}
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("expected connect error, got %T: %v", err, err)
	}
	assertDetailCode(t, cerr, wantCode)
}

func assertDetailCode(t *testing.T, cerr *connect.Error, wantCode errcode.Code) {
	t.Helper()
	if len(cerr.Details()) == 0 {
		t.Fatalf("expected error detail with code %q, got none", wantCode)
	}
	val, derr := cerr.Details()[0].Value()
	if derr != nil {
		t.Fatal(derr)
	}
	detail, ok := val.(*hostv1.ErrorDetail)
	if !ok || detail.Code != string(wantCode) {
		t.Fatalf("detail code = %q, want %q", detail.Code, wantCode)
	}
}

func verifyClient1SeesRequest(t *testing.T, backend *fakeBackend, sockPath string) string {
	t.Helper()
	interClient1, hostClient1, http1 := newInteractionClient(t, sockPath)
	if _, err := hostClient1.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}

	reqID := addTestConfirm(backend, "op-1", "delete workspace")

	list1, err := interClient1.ListPending(t.Context(), connect.NewRequest(&interactionv1.ListPendingRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if list1.Msg.Sequence != 1 || len(list1.Msg.Requests) != 1 || list1.Msg.Requests[0].RequestId != reqID {
		t.Fatalf("ListPending mismatch: %+v", list1.Msg)
	}

	http1.CloseIdleConnections()
	return reqID
}

func verifyClient2ListPending(t *testing.T, client interactionv1connect.InteractionServiceClient, reqID, inc string) {
	t.Helper()
	list, err := client.ListPending(t.Context(), connect.NewRequest(&interactionv1.ListPendingRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if list.Msg.Sequence != 1 || len(list.Msg.Requests) != 1 {
		t.Fatalf("reconnected ListPending mismatch: %+v", list.Msg)
	}
	reqView := list.Msg.Requests[0]
	if reqView.RequestId != reqID || reqView.Incarnation != inc || reqView.Tool != "file_writer" || reqView.Risk != "write" {
		t.Fatalf("reconnected pending request view mismatch: %+v", reqView)
	}
}

func verifyClient2Stream(t *testing.T, hostClient hostv1connect.HostServiceClient, interClient interactionv1connect.InteractionServiceClient, reqID, inc string) {
	t.Helper()
	stream, err := hostClient.Subscribe(streamCtx(t), connect.NewRequest(&hostv1.SubscribeRequest{
		Topic: app.TopicInteraction,
	}))
	if err != nil {
		t.Fatal(err)
	}

	snap := recv(t, stream).GetSnapshot()
	if snap == nil || snap.Sequence != 1 {
		t.Fatalf("Subscribe snapshot mismatch: %+v", snap)
	}

	_, err = interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-answer-1",
		Incarnation: inc,
		RequestId:   reqID,
		Outcome:     app.OutcomeAllowedOnce,
	}))
	if err != nil {
		t.Fatal(err)
	}

	ev := recv(t, stream).GetEvent()
	if ev == nil || ev.Sequence != 2 || ev.Type != app.EventInteractionResolved {
		t.Fatalf("stream event = %+v, want (2, resolved)", ev)
	}
}

func TestIE07RequestSurvivesClientReconnectInSnapshot(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)
	reqID := verifyClient1SeesRequest(t, backend, sockPath)
	inc := backend.inc

	interClient2, hostClient2, http2 := newInteractionClient(t, sockPath)
	t.Cleanup(http2.CloseIdleConnections)
	if _, err := hostClient2.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_TUI, "v1")); err != nil {
		t.Fatal(err)
	}

	verifyClient2ListPending(t, interClient2, reqID, inc)
	verifyClient2Stream(t, hostClient2, interClient2, reqID, inc)
}

func newHelloClient(t *testing.T, sockPath string, kind hostv1.ClientKind) interactionv1connect.InteractionServiceClient {
	t.Helper()
	client, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, err := hostClient.Hello(t.Context(), helloRequest(kind, "v1")); err != nil {
		t.Fatal(err)
	}
	return client
}

func assertOneWon(t *testing.T, errA, errB error) {
	t.Helper()
	if errA == nil && errB == nil {
		t.Fatal("both concurrent answers succeeded, want exactly one")
	}
	if errA != nil && errB != nil {
		t.Fatalf("both answers failed: A=%v, B=%v", errA, errB)
	}
	losingErr := errA
	if losingErr == nil {
		losingErr = errB
	}
	assertConnectCode(t, losingErr, connect.CodeFailedPrecondition, errcode.InteractionAlreadyAnswered)
}

func TestIE07TwoClientsAnsweringAtOnceExactlyOneIsAccepted(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)
	inc := backend.inc

	reqID := addTestConfirm(backend, "op-race", "concurrent answer")

	clientA := newHelloClient(t, sockPath, hostv1.ClientKind_CLIENT_KIND_CLI)
	clientB := newHelloClient(t, sockPath, hostv1.ClientKind_CLIENT_KIND_TUI)

	var (
		wg      sync.WaitGroup
		errA    error
		errB    error
		startCh = make(chan struct{})
	)
	wg.Add(2)

	supervise.Go(t.Context(), "test.answer-a", func(ctx context.Context) error {
		defer wg.Done()
		<-startCh
		_, errA = clientA.Answer(ctx, connect.NewRequest(&interactionv1.AnswerRequest{
			CommandId:   "cmd-client-a",
			Incarnation: inc,
			RequestId:   reqID,
			Outcome:     app.OutcomeAllowedOnce,
		}))
		return nil
	})

	supervise.Go(t.Context(), "test.answer-b", func(ctx context.Context) error {
		defer wg.Done()
		<-startCh
		_, errB = clientB.Answer(ctx, connect.NewRequest(&interactionv1.AnswerRequest{
			CommandId:   "cmd-client-b",
			Incarnation: inc,
			RequestId:   reqID,
			Outcome:     app.OutcomeDenied,
		}))
		return nil
	})

	close(startCh)
	wg.Wait()
	assertOneWon(t, errA, errB)
}

func TestIE07DuplicateCommandIDReturnsTheSameResult(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)
	inc := backend.inc

	reqID := addTestConfirm(backend, "op-dedup", "duplicate command_id test")

	interClient, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, herr := hostClient.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); herr != nil {
		t.Fatal(herr)
	}

	req := connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-idempotent-42",
		Incarnation: inc,
		RequestId:   reqID,
		Outcome:     app.OutcomeAllowedOnce,
	})

	res1, err := interClient.Answer(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}

	res2, err := interClient.Answer(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}

	if res1.Msg.RequestId != res2.Msg.RequestId || res1.Msg.Outcome != res2.Msg.Outcome {
		t.Fatalf("repeated answer mismatch: res1=%+v, res2=%+v", res1.Msg, res2.Msg)
	}
	if res1.Msg.Outcome != app.OutcomeAllowedOnce {
		t.Fatalf("outcome = %s, want allowed_once", res1.Msg.Outcome)
	}
}

func TestInteractionSessionPrincipalIsDenied(t *testing.T) {
	t.Parallel()
	srv, backend, sockPath := setupInteractionServer(t)

	reqID := addTestConfirm(backend, "op-sec", "session principal test")

	interClient, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, herr := hostClient.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); herr != nil {
		t.Fatal(herr)
	}

	srv.mu.Lock()
	for _, st := range srv.conns {
		st.mu.Lock()
		st.kind = app.ClientKind("session")
		st.mu.Unlock()
	}
	srv.mu.Unlock()

	_, err := interClient.ListPending(t.Context(), connect.NewRequest(&interactionv1.ListPendingRequest{}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)

	_, err = interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-session",
		Incarnation: backend.inc,
		RequestId:   reqID,
		Outcome:     app.OutcomeAllowedOnce,
	}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)

	stream, serr := hostClient.Subscribe(streamCtx(t), connect.NewRequest(&hostv1.SubscribeRequest{
		Topic: app.TopicInteraction,
	}))
	if serr == nil {
		if stream.Receive() {
			t.Fatal("session principal was able to receive events on interaction topic")
		}
		if connect.CodeOf(stream.Err()) != connect.CodePermissionDenied {
			t.Fatalf("Subscribe = %v, want permission_denied", stream.Err())
		}
	} else if connect.CodeOf(serr) != connect.CodePermissionDenied {
		t.Fatalf("Subscribe = %v, want permission_denied", serr)
	}
}

func assertNoLeakedSecrets(t *testing.T, ev *hostv1.HostEvent, secrets ...string) {
	t.Helper()
	payloadStr := string(ev.Payload)
	for _, secret := range secrets {
		if strings.Contains(payloadStr, secret) {
			t.Fatalf("I-E08 violation: resolved payload contains secret text: %s", payloadStr)
		}
	}

	var rawMap map[string]any
	if err := json.Unmarshal(ev.Payload, &rawMap); err != nil {
		t.Fatal(err)
	}
	for k := range rawMap {
		if k != "request_id" && k != "outcome" {
			t.Fatalf("I-E08 violation: unexpected field %q in resolved event payload", k)
		}
	}
}

func TestIE08ResolvedEventAndErrorsContainNoContent(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)

	secretPrompt := "SECRET_PROMPT_DO_NOT_LEAK_XYZ"
	secretDenyNote := "SECRET_DENY_NOTE_PRIVATE_123"

	reqID := addTestConfirm(backend, "op-ie08", secretPrompt)

	interClient, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, herr := hostClient.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); herr != nil {
		t.Fatal(herr)
	}

	stream, err := hostClient.Subscribe(streamCtx(t), connect.NewRequest(&hostv1.SubscribeRequest{
		Topic: app.TopicInteraction,
	}))
	if err != nil {
		t.Fatal(err)
	}

	_ = recv(t, stream)

	_, err = interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-ie08",
		Incarnation: backend.inc,
		RequestId:   reqID,
		Outcome:     app.OutcomeDenied,
		Text:        &secretDenyNote,
	}))
	if err != nil {
		t.Fatal(err)
	}

	ev := recv(t, stream).GetEvent()
	if ev == nil || ev.Type != app.EventInteractionResolved {
		t.Fatalf("event = %+v, want resolved", ev)
	}

	assertNoLeakedSecrets(t, ev, secretPrompt, secretDenyNote)
}

func TestInteractionLimitsEnforced(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)

	reqID := addTestConfirm(backend, "op-limits", "limits test")

	interClient, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, herr := hostClient.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); herr != nil {
		t.Fatal(herr)
	}

	bigNote := strings.Repeat("x", 4097)
	_, err := interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-limit-note",
		Incarnation: backend.inc,
		RequestId:   reqID,
		Outcome:     app.OutcomeDenied,
		Text:        &bigNote,
	}))
	assertConnectCode(t, err, connect.CodeResourceExhausted, errcode.InteractionTextTooLarge)

	bigAnswer := strings.Repeat("y", 16385)
	_, err = interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-limit-ans",
		Incarnation: backend.inc,
		RequestId:   reqID,
		Outcome:     app.OutcomeAllowedOnce,
		Text:        &bigAnswer,
	}))
	assertConnectCode(t, err, connect.CodeResourceExhausted, errcode.InteractionTextTooLarge)
}

func TestInteractionStaleIncarnation(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)

	reqID := addTestConfirm(backend, "op-stale", "stale incarnation test")

	interClient, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, herr := hostClient.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); herr != nil {
		t.Fatal(herr)
	}

	_, err := interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-stale",
		Incarnation: "inc-stale-previous",
		RequestId:   reqID,
		Outcome:     app.OutcomeAllowedOnce,
	}))
	assertConnectCode(t, err, connect.CodeFailedPrecondition, errcode.InteractionStaleIncarnation)
}

func TestInteractionOptionNotOffered(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)

	reqID := addTestConfirm(backend, "op-unoffered", "option not offered test")

	interClient, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, herr := hostClient.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); herr != nil {
		t.Fatal(herr)
	}

	_, err := interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-unoffered-outcome",
		Incarnation: backend.inc,
		RequestId:   reqID,
		Outcome:     "unexpected_outcome_xyz",
	}))
	assertConnectCode(t, err, connect.CodeInvalidArgument, errcode.InteractionOptionNotOffered)

	badOption := "opt-bad-123"
	_, err = interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-unoffered-opt",
		Incarnation: backend.inc,
		RequestId:   reqID,
		Outcome:     app.OutcomeAllowedOnce,
		OptionId:    &badOption,
	}))
	assertConnectCode(t, err, connect.CodeInvalidArgument, errcode.InteractionOptionNotOffered)
}

func TestInteractionRelayedRejectsSessionGrant(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)

	reqID := addTestRelayed(backend, "vreq-1", "Vendor command npm install")

	interClient, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, herr := hostClient.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); herr != nil {
		t.Fatal(herr)
	}

	_, err := interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-relayed-grant",
		Incarnation: backend.inc,
		RequestId:   reqID,
		Outcome:     app.OutcomeAllowedSession,
	}))
	assertConnectCode(t, err, connect.CodeInvalidArgument, errcode.InteractionOptionNotOffered)
}

func seedTwoQuestionInteraction(backend *fakeBackend, reqID string) {
	backend.pendingMu.Lock()
	defer backend.pendingMu.Unlock()
	backend.pendingSeq++
	backend.pending[reqID] = app.PendingInteractionView{
		RequestID:       reqID,
		Incarnation:     backend.inc,
		SessionID:       testSessionID,
		Tool:            "ask",
		Kind:            "question",
		Summary:         "Please answer both",
		OfferedOutcomes: []string{app.OutcomeAnswered, app.OutcomeCancelled},
		Questions: []app.InteractionQuestion{
			{
				QuestionID: "q1",
				Prompt:     "Select color",
				Options: []app.InteractionOption{
					{ID: testColorRed, Label: "Red"},
					{ID: "blue", Label: "Blue"},
				},
				MultiSelect: false,
				AllowText:   true,
			},
			{
				QuestionID:  "q2",
				Prompt:      "Confirm approval?",
				MultiSelect: false,
				AllowText:   true,
			},
		},
	}
}

func TestInteractionQuestionKindTwoQuestions(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)
	seedTwoQuestionInteraction(backend, testOpQ2)

	interClient, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, herr := hostClient.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); herr != nil {
		t.Fatal(herr)
	}

	listRes, err := interClient.ListPending(t.Context(), connect.NewRequest(&interactionv1.ListPendingRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listRes.Msg.Requests) != 1 || len(listRes.Msg.Requests[0].Questions) != 2 {
		t.Fatalf("expected 2 questions in PendingRequest, got: %+v", listRes.Msg.Requests)
	}
	q1 := listRes.Msg.Requests[0].Questions[0]
	q2 := listRes.Msg.Requests[0].Questions[1]
	if q1.QuestionId != "q1" || len(q1.Options) != 2 || q2.QuestionId != "q2" {
		t.Fatalf("question views mismatch: q1=%+v q2=%+v", q1, q2)
	}

	// Answering missing question q2 fails typed
	_, err = interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-q-missing",
		Incarnation: backend.inc,
		RequestId:   testOpQ2,
		Outcome:     app.OutcomeAnswered,
		Answers: []*interactionv1.QuestionAnswer{
			{QuestionId: "q1", OptionIds: []string{testColorRed}},
		},
	}))
	assertConnectCode(t, err, connect.CodeInvalidArgument, errcode.InvalidArgument)

	// Answering both questions resolves
	res, err := interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-q-both",
		Incarnation: backend.inc,
		RequestId:   testOpQ2,
		Outcome:     app.OutcomeAnswered,
		Answers: []*interactionv1.QuestionAnswer{
			{QuestionId: "q1", OptionIds: []string{testColorRed}},
			{QuestionId: "q2", Text: "confirmed"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.Outcome != app.OutcomeAnswered {
		t.Fatalf("outcome = %s, want answered", res.Msg.Outcome)
	}
}

func TestInteractionConnectWithReadMaxBytesOversizedRejected(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)

	interClient, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, herr := hostClient.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); herr != nil {
		t.Fatal(herr)
	}

	oversized := strings.Repeat("A", 70*1024)
	_, err := interClient.Answer(t.Context(), connect.NewRequest(&interactionv1.AnswerRequest{
		CommandId:   "cmd-oversized-body",
		Incarnation: backend.inc,
		RequestId:   "op-oversized",
		Outcome:     app.OutcomeAllowedOnce,
		Text:        &oversized,
	}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("expected CodeResourceExhausted from transport max bytes, got %v (err: %v)", connect.CodeOf(err), err)
	}
	if backend.answerCalls > 0 {
		t.Fatalf("expected request to be rejected before backend, but backend received %d calls", backend.answerCalls)
	}
}

func TestInteractionListPendingAndTopicCarryFull256KiBDetail(t *testing.T) {
	t.Parallel()
	_, backend, sockPath := setupInteractionServer(t)

	view := buildFull256KiBView(backend.inc)
	backend.pendingMu.Lock()
	backend.pendingSeq++
	seq := backend.pendingSeq
	backend.pending[view.RequestID] = view
	backend.pendingMu.Unlock()

	interClient, hostClient, httpClient := newInteractionClient(t, sockPath)
	t.Cleanup(httpClient.CloseIdleConnections)
	if _, err := hostClient.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}
	stream, err := hostClient.Subscribe(streamCtx(t), connect.NewRequest(&hostv1.SubscribeRequest{Topic: app.TopicInteraction}))
	if err != nil {
		t.Fatal(err)
	}
	_ = recv(t, stream).GetSnapshot()

	listRes, err := interClient.ListPending(t.Context(), connect.NewRequest(&interactionv1.ListPendingRequest{}))
	if err != nil {
		t.Fatalf("ListPending 256 KiB detail failed: %v", err)
	}
	if len(listRes.Msg.Requests) != 1 || listRes.Msg.Requests[0].Detail != view.Detail {
		t.Fatalf("ListPending Detail len=%d, want %d", len(listRes.Msg.Requests[0].GetDetail()), len(view.Detail))
	}

	assertTopicCarriesFullDetail(t, backend, stream, seq+1, &view)
}

func buildFull256KiBView(inc string) app.PendingInteractionView {
	const maxDetail = 256 * 1024
	const prefix = `{"command":"`
	const suffix = `"}`
	fullDetail := prefix + strings.Repeat("d", maxDetail-len(prefix)-len(suffix)) + suffix
	return app.PendingInteractionView{
		RequestID:       "req-256k",
		Incarnation:     inc,
		SessionID:       testSessionID,
		Kind:            testKindConfirm,
		Summary:         "Bash: " + strings.Repeat("a", 64) + "… [truncated, full input in detail]",
		Detail:          fullDetail,
		OfferedOutcomes: []string{app.OutcomeAnswered, app.OutcomeDenied},
	}
}

func assertTopicCarriesFullDetail(t *testing.T, backend *fakeBackend, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], seq uint64, view *app.PendingInteractionView) {
	t.Helper()
	payload, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if _, perr := backend.hub.Publish(app.TopicInteraction, app.Event{
		Seq:     seq,
		Type:    app.EventInteractionRequested,
		Payload: payload,
	}); perr != nil {
		t.Fatalf("Publish 256 KiB requested event: %v", perr)
	}
	ev := recv(t, stream).GetEvent()
	if ev == nil || ev.Type != app.EventInteractionRequested {
		t.Fatalf("stream event = %+v, want requested", ev)
	}
	var got app.PendingInteractionView
	if err := json.Unmarshal(ev.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Detail != view.Detail {
		t.Fatalf("topic requested Detail len=%d, want %d", len(got.Detail), len(view.Detail))
	}
}
