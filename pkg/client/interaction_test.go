package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	interactionv1 "github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1/interactionv1connect"
)

const (
	testInterInc         = "inc-inter-1"
	testInterReqConfirm  = "req-confirm"
	testInterReqQuestion = "req-question"
	testInterSessID      = "sess-inter-1"
	testInterStaleCode   = "interaction.stale_incarnation"
	testInterVendorRaw   = "  verbatim [link](https://evil.invalid) <script>alert(1)</script>\n\tsecond line  "
	testInterDetailRaw   = `{"command":"rm -rf /tmp/demo","raw":"<b>untouched</b>"}`
	testInterBodyRaw     = "Line 1: untouched\nLine 2: [not-a-link](https://example.com)"
	testInterCommandID1  = "cmd-inter-1"
	testInterCommandID2  = "cmd-inter-2"
	testInterCommandID3  = "cmd-inter-3"
	testInterCommandID4  = "cmd-inter-4"
)

var errClientInterScripted = errors.New("scripted interaction error")

type interactionFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	interactionv1connect.UnimplementedInteractionServiceHandler

	mu       sync.Mutex
	requests []*interactionv1.PendingRequest
	seq      uint64
	answers  []*interactionv1.AnswerRequest
	topicMsg []*hostv1.SubscribeResponse
}

func (f *interactionFake) Hello(_ context.Context, _ *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{HostVersion: "v1", ApiVersion: "v1"}), nil
}

func (f *interactionFake) Subscribe(_ context.Context, req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
	if req.Msg.GetTopic() != client.TopicInteraction {
		return connect.NewError(connect.CodeNotFound, errClientInterScripted)
	}
	f.mu.Lock()
	msgs := append([]*hostv1.SubscribeResponse(nil), f.topicMsg...)
	f.mu.Unlock()
	for _, msg := range msgs {
		if err := stream.Send(msg); err != nil {
			return fmt.Errorf("send interaction topic message: %w", err)
		}
	}
	return nil
}

func (f *interactionFake) ListPending(_ context.Context, _ *connect.Request[interactionv1.ListPendingRequest]) (*connect.Response[interactionv1.ListPendingResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&interactionv1.ListPendingResponse{
		Requests: f.requests,
		Sequence: f.seq,
	}), nil
}

func (f *interactionFake) Answer(_ context.Context, req *connect.Request[interactionv1.AnswerRequest]) (*connect.Response[interactionv1.AnswerResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, req.Msg)
	if req.Msg.GetIncarnation() != testInterInc {
		return nil, scriptedInteractionError(testInterStaleCode)
	}
	return connect.NewResponse(&interactionv1.AnswerResponse{
		RequestId: req.Msg.GetRequestId(),
		Outcome:   req.Msg.GetOutcome(),
	}), nil
}

func scriptedInteractionError(code string) error {
	cerr := connect.NewError(connect.CodeFailedPrecondition, errClientInterScripted)
	detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: code})
	if err != nil {
		panic(err)
	}
	cerr.AddDetail(detail)
	return cerr
}

func serveInteractionFake(t *testing.T, fake *interactionFake) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
	mux.Handle(interactionv1connect.NewInteractionServiceHandler(fake))
	return serveFakeOnUnixSocket(t, mux)
}

func mustJSONBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func seedFixtures(t *testing.T) (snapReq, addedReq client.PendingInteraction) {
	t.Helper()
	snapReq = client.PendingInteraction{
		RequestID:       testInterReqConfirm,
		Incarnation:     testInterInc,
		SessionID:       testInterSessID,
		Tool:            "shell",
		Kind:            client.InteractionKindConfirm,
		Risk:            "exec",
		EnvTag:          "prod",
		Summary:         testInterVendorRaw,
		Detail:          testInterDetailRaw,
		Body:            testInterBodyRaw,
		OfferedOutcomes: []string{client.InteractionOutcomeAllowedOnce, client.InteractionOutcomeAllowedSession, client.InteractionOutcomeDenied},
		GrantScopeLabel: "shell in prod",
	}
	addedReq = client.PendingInteraction{
		RequestID:       testInterReqQuestion,
		Incarnation:     testInterInc,
		SessionID:       "sess-inter-2",
		Tool:            "ask_user",
		Kind:            client.InteractionKindQuestion,
		Summary:         testInterVendorRaw,
		Body:            testInterBodyRaw,
		OfferedOutcomes: []string{client.InteractionOutcomeAnswered, client.InteractionOutcomeCancelled},
		Questions: []client.InteractionQuestion{{
			QuestionID:  "q1",
			Prompt:      testInterVendorRaw,
			MultiSelect: true,
			AllowText:   true,
			Options: []client.InteractionOption{{
				ID:          "opt-a",
				Label:       "Option A verbatim",
				Description: testInterVendorRaw,
			}},
		}},
	}
	return snapReq, addedReq
}

func seedInteractionFake(t *testing.T) *interactionFake {
	t.Helper()
	snapReq, addedReq := seedFixtures(t)
	return &interactionFake{
		seq: 7,
		requests: []*interactionv1.PendingRequest{{
			RequestId: snapReq.RequestID, Incarnation: snapReq.Incarnation, SessionId: snapReq.SessionID,
			Tool: snapReq.Tool, Kind: snapReq.Kind, Risk: snapReq.Risk, EnvTag: snapReq.EnvTag,
			Summary: snapReq.Summary, Detail: snapReq.Detail, Body: snapReq.Body,
			OfferedOutcomes: snapReq.OfferedOutcomes, GrantScopeLabel: snapReq.GrantScopeLabel,
		}},
		topicMsg: []*hostv1.SubscribeResponse{
			{Kind: &hostv1.SubscribeResponse_Snapshot{Snapshot: &hostv1.SnapshotEvent{
				Sequence: 7, IncarnationId: testInterInc, State: mustJSONBytes(t, []client.PendingInteraction{snapReq}),
			}}},
			{Kind: &hostv1.SubscribeResponse_Event{Event: &hostv1.HostEvent{
				Sequence: 8, Type: client.EventInteractionRequested, Payload: mustJSONBytes(t, addedReq),
			}}},
			{Kind: &hostv1.SubscribeResponse_Event{Event: &hostv1.HostEvent{
				Sequence: 9,
				Type:     client.EventInteractionResolved,
				Payload:  mustJSONBytes(t, client.InteractionResolvedEvent{RequestID: testInterReqConfirm, Outcome: client.InteractionOutcomeDenied}),
			}}},
			{Kind: &hostv1.SubscribeResponse_End{End: &hostv1.StreamEnd{}}},
		},
	}
}

func TestInteractionClientMethodsOverRealSocket(t *testing.T) {
	t.Parallel()
	fake := seedInteractionFake(t)
	c := serveInteractionFake(t, fake)

	list, err := c.ListPendingInteractions(t.Context())
	if err != nil {
		t.Fatalf("ListPendingInteractions: %v", err)
	}
	if list.Sequence != 7 || len(list.Requests) != 1 {
		t.Fatalf("ListPendingInteractions = %+v", list)
	}
	got := list.Requests[0]
	if got.Summary != testInterVendorRaw || got.Detail != testInterDetailRaw || got.Body != testInterBodyRaw {
		t.Fatalf("vendor text altered: got %+v", got)
	}

	assertInteractionAnswers(t, c, fake)
	assertInteractionTopicStream(t, c)
}

func assertInteractionAnswers(t *testing.T, c *client.Client, fake *interactionFake) {
	t.Helper()
	res, err := c.AnswerInteraction(t.Context(), &client.AnswerInteractionInput{
		CommandID:   testInterCommandID1,
		Incarnation: testInterInc,
		RequestID:   testInterReqConfirm,
		Outcome:     client.InteractionOutcomeDenied,
		Text:        "deny reason",
	})
	if err != nil || res.Outcome != client.InteractionOutcomeDenied {
		t.Fatalf("AnswerInteraction confirm = %+v, err=%v", res, err)
	}

	_, err = c.AnswerInteraction(t.Context(), &client.AnswerInteractionInput{
		CommandID:   testInterCommandID2,
		Incarnation: testInterInc,
		RequestID:   "req-select",
		Outcome:     client.InteractionOutcomeAllowedOnce,
		OptionID:    "opt-1",
	})
	if err != nil {
		t.Fatalf("AnswerInteraction select: %v", err)
	}

	_, err = c.AnswerInteraction(t.Context(), &client.AnswerInteractionInput{
		CommandID:   testInterCommandID3,
		Incarnation: testInterInc,
		RequestID:   testInterReqQuestion,
		Outcome:     client.InteractionOutcomeAnswered,
		Answers: []client.InteractionQuestionAnswer{{
			QuestionID: "q1",
			OptionIDs:  []string{"opt-a"},
			Text:       testInterVendorRaw,
		}},
	})
	if err != nil {
		t.Fatalf("AnswerInteraction question: %v", err)
	}

	_, err = c.AnswerInteraction(t.Context(), &client.AnswerInteractionInput{
		CommandID:   testInterCommandID4,
		Incarnation: "inc-old",
		RequestID:   testInterReqConfirm,
		Outcome:     client.InteractionOutcomeDenied,
	})
	if code, ok := client.InteractionCodeOf(err); !ok || code != testInterStaleCode {
		t.Fatalf("stale InteractionCodeOf = (%q, %v), want %q (err=%v)", code, ok, testInterStaleCode, err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.answers) != 4 {
		t.Fatalf("recorded answers = %d, want 4", len(fake.answers))
	}
	if diff := cmp.Diff("opt-1", fake.answers[1].GetOptionId()); diff != "" {
		t.Fatalf("select OptionId mismatch (-want +got):\n%s", diff)
	}
	if len(fake.answers[2].GetAnswers()) != 1 || fake.answers[2].GetAnswers()[0].GetText() != testInterVendorRaw {
		t.Fatalf("question answer altered: %+v", fake.answers[2].GetAnswers())
	}
}

func recvMsg(t *testing.T, stream *client.Stream) *client.Message {
	t.Helper()
	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("stream Recv: %v", err)
	}
	return msg
}

func assertInteractionTopicStream(t *testing.T, c *client.Client) {
	t.Helper()
	stream, err := c.Subscribe(t.Context(), client.TopicInteraction, nil)
	if err != nil {
		t.Fatalf("Subscribe interaction: %v", err)
	}
	defer func() {
		if cerr := stream.Close(); cerr != nil {
			t.Errorf("close stream: %v", cerr)
		}
	}()

	assertTopicSnapshot(t, recvMsg(t, stream))
	assertTopicRequested(t, recvMsg(t, stream))
	assertTopicResolved(t, recvMsg(t, stream))
	if _, err := stream.Recv(); !errors.Is(err, client.ErrStreamEnded) {
		t.Fatalf("final Recv = %v, want ErrStreamEnded", err)
	}
}

func assertTopicSnapshot(t *testing.T, msg *client.Message) {
	t.Helper()
	snap, err := client.DecodeInteractionSnapshot(msg.Snapshot.State)
	if err != nil || msg.Snapshot.Incarnation != testInterInc {
		t.Fatalf("snapshot = %+v, err=%v", msg.Snapshot, err)
	}
	if len(snap) != 1 || snap[0].Detail != testInterDetailRaw {
		t.Fatalf("decoded snapshot = %+v", snap)
	}
}

func assertTopicRequested(t *testing.T, msg *client.Message) {
	t.Helper()
	req, err := client.DecodeInteractionRequested(msg.Event.Payload)
	if err != nil || msg.Event.Type != client.EventInteractionRequested {
		t.Fatalf("requested = %+v, err=%v", msg.Event, err)
	}
	if req.RequestID != testInterReqQuestion || req.Questions[0].Prompt != testInterVendorRaw {
		t.Fatalf("decoded requested = %+v", req)
	}
}

func assertTopicResolved(t *testing.T, msg *client.Message) {
	t.Helper()
	resolved, err := client.DecodeInteractionResolved(msg.Event.Payload)
	if err != nil || msg.Event.Type != client.EventInteractionResolved {
		t.Fatalf("resolved = %+v, err=%v", msg.Event, err)
	}
	if resolved.RequestID != testInterReqConfirm || resolved.Outcome != client.InteractionOutcomeDenied {
		t.Fatalf("decoded resolved = %+v", resolved)
	}
}
