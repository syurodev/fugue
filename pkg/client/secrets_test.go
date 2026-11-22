package client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	interactionv1 "github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1/interactionv1connect"
	secretsv1 "github.com/syurodev/fugue/pkg/pb/fugue/secrets/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/secrets/v1/secretsv1connect"
)

const testSecretID2 = "sec-2"

type fakeSecretServer struct {
	secretsv1connect.UnimplementedSecretServiceHandler
	lastScope secretsv1.SecretScopeKind
	lastPut   *secretsv1.PutSecretRequest
	lastDel   *secretsv1.DeleteSecretRequest
}

func (f *fakeSecretServer) ListSecrets(_ context.Context, req *connect.Request[secretsv1.ListSecretsRequest]) (*connect.Response[secretsv1.ListSecretsResponse], error) {
	f.lastScope = req.Msg.GetScopeKind()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	return connect.NewResponse(&secretsv1.ListSecretsResponse{
		Secrets: []*secretsv1.SecretMeta{{
			SecretId:    "sec-1",
			ScopeKind:   secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_GLOBAL,
			Name:        "OPENAI_API_KEY",
			Description: "Key",
			CreatedAt:   timestamppb.New(now),
			UpdatedAt:   timestamppb.New(now),
		}},
	}), nil
}

func (f *fakeSecretServer) PutSecret(_ context.Context, req *connect.Request[secretsv1.PutSecretRequest]) (*connect.Response[secretsv1.PutSecretResponse], error) {
	f.lastPut = req.Msg
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	return connect.NewResponse(&secretsv1.PutSecretResponse{
		Secret: &secretsv1.SecretMeta{
			SecretId:    testSecretID2,
			ScopeKind:   req.Msg.GetScopeKind(),
			ScopeId:     req.Msg.GetScopeId(),
			Name:        req.Msg.GetName(),
			Description: req.Msg.GetDescription(),
			CreatedAt:   timestamppb.New(now),
			UpdatedAt:   timestamppb.New(now),
		},
	}), nil
}

func (f *fakeSecretServer) DeleteSecret(_ context.Context, req *connect.Request[secretsv1.DeleteSecretRequest]) (*connect.Response[secretsv1.DeleteSecretResponse], error) {
	f.lastDel = req.Msg
	return connect.NewResponse(&secretsv1.DeleteSecretResponse{Deleted: true}), nil
}

type fakeSecretInteractionServer struct {
	interactionv1connect.UnimplementedInteractionServiceHandler
	lastAns *interactionv1.AnswerRequest
}

func (f *fakeSecretInteractionServer) ListPending(_ context.Context, _ *connect.Request[interactionv1.ListPendingRequest]) (*connect.Response[interactionv1.ListPendingResponse], error) {
	return connect.NewResponse(&interactionv1.ListPendingResponse{
		Requests: []*interactionv1.PendingRequest{{
			RequestId: "req-sec",
			Kind:      "question",
			Questions: []*interactionv1.Question{{
				QuestionId: "q1",
				Prompt:     "Enter API token",
				AllowText:  true,
				Secret:     true,
			}},
		}},
		Sequence: 1,
	}), nil
}

func (f *fakeSecretInteractionServer) Answer(_ context.Context, req *connect.Request[interactionv1.AnswerRequest]) (*connect.Response[interactionv1.AnswerResponse], error) {
	f.lastAns = req.Msg
	return connect.NewResponse(&interactionv1.AnswerResponse{RequestId: req.Msg.GetRequestId(), Outcome: req.Msg.GetOutcome()}), nil
}

func TestSecretsClientMethodsAndTopicDecoders(t *testing.T) {
	t.Parallel()
	secSrv := &fakeSecretServer{}
	intSrv := &fakeSecretInteractionServer{}
	mux := http.NewServeMux()
	mux.Handle(secretsv1connect.NewSecretServiceHandler(secSrv))
	mux.Handle(interactionv1connect.NewInteractionServiceHandler(intSrv))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := &Client{hc: ts.Client(), base: ts.URL}
	assertListAndPutSecret(t, c, secSrv)
	assertDeleteSecret(t, c, secSrv)
	assertSecretsTopicDecoders(t)
	assertSecretInteractionClient(t, c)
}

func assertListAndPutSecret(t *testing.T, c *Client, secSrv *fakeSecretServer) {
	t.Helper()
	ctx := t.Context()
	items, listErr := c.ListSecrets(ctx, SecretScopeGlobal, "")
	if listErr != nil || len(items) != 1 || items[0].Name != "OPENAI_API_KEY" || secSrv.lastScope != secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_GLOBAL {
		t.Fatalf("ListSecrets = (%+v, %v)", items, listErr)
	}
	val := []byte("super-secret-value")
	putMeta, putErr := c.PutSecret(ctx, &PutSecretInput{
		CommandID: "cmd-1", ScopeKind: SecretScopeWorkspace, ScopeID: "ws-1",
		Name: "DB_PASS", Description: "DB", Value: val,
	})
	if putErr != nil || putMeta.SecretID != testSecretID2 || putMeta.ScopeKind != SecretScopeWorkspace {
		t.Fatalf("PutSecret = (%+v, %v)", putMeta, putErr)
	}
	if !bytes.Equal(val, make([]byte, len(val))) {
		t.Fatalf("PutSecret did not clear input value: %x", val)
	}
	if _, nilErr := c.PutSecret(ctx, nil); nilErr == nil {
		t.Fatal("PutSecret(nil) succeeded, want error")
	}
}

func assertDeleteSecret(t *testing.T, c *Client, secSrv *fakeSecretServer) {
	t.Helper()
	del, delErr := c.DeleteSecret(t.Context(), "cmd-2", testSecretID2)
	if delErr != nil || !del || secSrv.lastDel.GetSecretId() != testSecretID2 {
		t.Fatalf("DeleteSecret = (%v, %v)", del, delErr)
	}
}

func assertSecretsTopicDecoders(t *testing.T) {
	t.Helper()
	snap, snapErr := DecodeSecretsSnapshot([]byte(`{"secrets":[{"secret_id":"s1","scope_kind":"global","name":"K"}]}`))
	if snapErr != nil || len(snap.Secrets) != 1 || snap.Secrets[0].Name != "K" {
		t.Fatalf("DecodeSecretsSnapshot = (%+v, %v)", snap, snapErr)
	}
	if _, badSnapErr := DecodeSecretsSnapshot([]byte(`{bad`)); badSnapErr == nil {
		t.Fatal("DecodeSecretsSnapshot invalid JSON succeeded")
	}
	ev, evErr := DecodeSecretEvent([]byte(`{"secret_id":"s1","scope_kind":"global","name":"K"}`))
	if evErr != nil || ev.SecretID != "s1" {
		t.Fatalf("DecodeSecretEvent = (%+v, %v)", ev, evErr)
	}
	if _, badEvErr := DecodeSecretEvent([]byte(`{bad`)); badEvErr == nil {
		t.Fatal("DecodeSecretEvent invalid JSON succeeded")
	}
}

func assertSecretInteractionClient(t *testing.T, c *Client) {
	t.Helper()
	ctx := t.Context()
	pending, listErr := c.ListPendingInteractions(ctx)
	if listErr != nil || len(pending.Requests) != 1 || !pending.Requests[0].Questions[0].Secret {
		t.Fatalf("ListPendingInteractions secret = (%+v, %v)", pending, listErr)
	}
	ansVal := []byte("secret-answer-123")
	if _, ansErr := c.AnswerInteraction(ctx, &AnswerInteractionInput{
		CommandID:   "cmd-ans",
		Incarnation: "inc-1",
		RequestID:   "req-sec",
		Outcome:     InteractionOutcomeAnswered,
		Answers: []InteractionQuestionAnswer{{
			QuestionID:     "q1",
			SecretValue:    ansVal,
			SaveSecretName: "SAVED_TOK",
			SaveScopeKind:  SecretScopeGlobal,
		}},
	}); ansErr != nil {
		t.Fatalf("AnswerInteraction = %v", ansErr)
	}
	if !bytes.Equal(ansVal, make([]byte, len(ansVal))) {
		t.Fatalf("AnswerInteraction did not clear SecretValue: %x", ansVal)
	}
}
