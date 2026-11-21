package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/codex"
)

func seedNativePendingConfirm(t *testing.T, inter *InteractionService, sub *Subscription, sessionID, opID string) PendingInteractionView {
	t.Helper()
	ticket, _, err := inter.owner.Ensure(&interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Summary:     "native confirm",
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
		Binding: interaction.Binding{
			SessionID: sessionID, InvocationID: "inv-" + opID, AttemptID: "att-" + opID,
			CheckGeneration: 1, ToolName: "native_tool",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt := awaitRequested(t, sub)
	if prompt.RequestID != ticket.RequestID {
		t.Fatalf("native prompt request_id = %q, want %q", prompt.RequestID, ticket.RequestID)
	}
	return prompt
}

func awaitCancelledResolvedEvent(t *testing.T, sub *Subscription, requestID string) {
	t.Helper()
	for {
		ev, ok := sub.Next(t.Context())
		if !ok {
			t.Fatalf("subscription ended waiting for cancelled resolved event %s", requestID)
		}
		if ev.Type != EventInteractionResolved {
			continue
		}
		var payload struct {
			RequestID string `json:"request_id"`
			Outcome   string `json:"outcome"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.RequestID != requestID {
			continue
		}
		if payload.Outcome != OutcomeCancelled {
			t.Fatalf("resolved outcome for %s = %q, want %q", requestID, payload.Outcome, OutcomeCancelled)
		}
		return
	}
}

func assertStaleTicketRejected(t *testing.T, inter *InteractionService, requestID, optionID string) {
	t.Helper()
	_, err := inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "stale-" + requestID, Principal: testAppPrincipal, Incarnation: inter.Incarnation(),
		RequestID: requestID, Outcome: OutcomeAnswered, OptionID: optionID,
	})
	code := errcode.CodeOf(err)
	if code != errcode.InteractionNotFound && code != errcode.InteractionAlreadyAnswered {
		t.Fatalf("stale Answer(%s) code = %q (err=%v), want not_found or already_answered", requestID, code, err)
	}
}

// TestRelayedApprovalIsCancelledWhenItsTurnEnds verifies Q-161 for both
// Claude (`approve`) and Codex (`RelayServerRequest`): when a turn ends while
// the session stays open, pending relayed requests for that turn are cancelled,
// already-answered requests are untouched, and native confirms remain pending.
func TestRelayedApprovalIsCancelledWhenItsTurnEnds(t *testing.T) {
	t.Parallel()
	t.Run("claude", func(t *testing.T) {
		t.Parallel()
		testClaudeTurnEndCancelsPendingRelayed(t)
	})
	t.Run("codex", func(t *testing.T) {
		t.Parallel()
		testCodexTurnEndCancelsPendingRelayed(t)
	})
}

func testClaudeTurnEndCancelsPendingRelayed(t *testing.T) {
	t.Helper()
	f := newApproveFixture(t)
	sub := f.subscribeInteractions(t)

	native := seedNativePendingConfirm(t, f.inter, sub, f.session.ID, "op-native-claude-turn")

	ansCh := f.startApprove(t, "claude-turn-ans", "Bash", json.RawMessage(`{"command":"echo 1"}`), "tu-turn-ans")
	ansPrompt := awaitRequested(t, sub)
	f.answerAllow(t, "cmd-turn-ans", ansPrompt.RequestID)
	if res := <-ansCh; res.IsError {
		t.Fatalf("answered approve failed: %+v", res)
	}
	awaitResolvedEvent(t, sub, ansPrompt.RequestID)

	pendCh := f.startApprove(t, "claude-turn-pend", "Bash", json.RawMessage(`{"command":"sleep 10"}`), "tu-turn-pend")
	pendPrompt := awaitRequested(t, sub)

	if err := f.svc.Steer(t.Context(), testAppPrincipal, VendorInput{
		CommandID: "claude-complete-turn", SessionID: f.session.ID, Text: "complete",
	}); err != nil {
		t.Fatal(err)
	}

	awaitCancelledResolvedEvent(t, sub, pendPrompt.RequestID)
	assertGenericToolError(t, <-pendCh, "turn ended")
	assertStaleTicketRejected(t, f.inter, pendPrompt.RequestID, approveOptionAllow)

	pending, _, err := f.inter.ListPending(t.Context(), testAppPrincipal)
	if err != nil || len(pending) != 1 || pending[0].RequestID != native.RequestID {
		t.Fatalf("ListPending after turn end = (%+v, %v), want only native %s", pending, err, native.RequestID)
	}
	if got := f.recordedDecisions(t); len(got) != 1 || got[0] != approveOptionAllow {
		t.Fatalf("recorded decisions = %+v, want single allow", got)
	}
}

func testCodexTurnEndCancelsPendingRelayed(t *testing.T) {
	t.Helper()
	f := newCodexRelayFixture(t)
	sub := f.subscribeInteractions(t)

	native := seedNativePendingConfirm(t, f.inter, sub, f.session.ID, "op-native-codex-turn")

	ansFrame := fmt.Sprintf(`{"id":501,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"call-501","command":"echo 1"}}`,
		codex.MethodCommandApproval, fakeCodexThreadID)
	pendFrame := fmt.Sprintf(`{"id":502,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"call-502","command":"sleep 10"}}`,
		codex.MethodCommandApproval, fakeCodexThreadID)

	f.sendServerRequestTurn(t, "codex-turn-end-1", ansFrame)
	ansPrompt := awaitRequested(t, sub)
	f.answerOption(t, "cmd-codex-501", ansPrompt.RequestID, codex.DecisionAccept)
	awaitResolvedEvent(t, sub, ansPrompt.RequestID)
	if resp := f.awaitRPCResponse(t, "501"); resp.Error != nil {
		t.Fatalf("RPC 501 error = %+v, want accept", resp)
	}

	f.steerServerRequest(t, "codex-turn-end-2", pendFrame)
	pendPrompt := awaitRequested(t, sub)

	if err := f.svc.Steer(t.Context(), testAppPrincipal, VendorInput{
		CommandID: "codex-complete-turn", SessionID: f.session.ID, Text: "complete",
	}); err != nil {
		t.Fatal(err)
	}

	awaitCancelledResolvedEvent(t, sub, pendPrompt.RequestID)
	assertCodexErrorResponse(t, f.awaitRPCResponse(t, "502"), "turn ended")
	assertStaleTicketRejected(t, f.inter, pendPrompt.RequestID, codex.DecisionAccept)

	pending, _, err := f.inter.ListPending(t.Context(), testAppPrincipal)
	if err != nil || len(pending) != 1 || pending[0].RequestID != native.RequestID {
		t.Fatalf("ListPending after codex turn end = (%+v, %v), want only native %s", pending, err, native.RequestID)
	}
	if got := f.recordedDecisions(t); len(got) != 1 || got[0] != codex.DecisionAccept {
		t.Fatalf("recorded decisions = %+v, want single accept", got)
	}
}

func awaitResolvedEvent(t *testing.T, sub *Subscription, requestID string) {
	t.Helper()
	for {
		ev, ok := sub.Next(t.Context())
		if !ok {
			t.Fatalf("subscription ended waiting for resolved event %s", requestID)
		}
		if ev.Type != EventInteractionResolved {
			continue
		}
		var payload struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.RequestID == requestID {
			return
		}
	}
}

// TestRelayedApprovalIsCancelledWhenTheVendorAbortsTheCall verifies Q-161 for
// both Claude (`awaitAndTakeRelayed`) and Codex (`awaitAndTakeCodexRelayed`):
// cancelling the relayed call context while the session token stays live
// invalidates the operation and removes the pending ticket.
func TestRelayedApprovalIsCancelledWhenTheVendorAbortsTheCall(t *testing.T) {
	t.Parallel()
	t.Run("claude", func(t *testing.T) {
		t.Parallel()
		testClaudeVendorAbortCancelsRelayed(t)
	})
	t.Run("codex", func(t *testing.T) {
		t.Parallel()
		testCodexVendorAbortCancelsRelayed(t)
	})
}

func testClaudeVendorAbortCancelsRelayed(t *testing.T) {
	t.Helper()
	f := newApproveFixture(t)
	sub := f.subscribeInteractions(t)

	callCtx, cancelCall := context.WithCancel(t.Context())
	defer cancelCall()
	args := []byte(`{"tool_name":"Bash","input":{"command":"sleep 10"},"tool_use_id":"tu-abort-1"}`)
	resCh := make(chan FugueToolResult, 1)
	supervise.Go(t.Context(), "test.claude-abort", func(context.Context) error {
		res, err := f.tools.Call(callCtx, f.binding, approveToolName, args)
		if err != nil {
			t.Errorf("approve Call error: %v", err)
		}
		resCh <- res
		return nil
	})

	prompt := awaitRequested(t, sub)
	cancelCall()

	awaitCancelledResolvedEvent(t, sub, prompt.RequestID)
	assertGenericToolError(t, <-resCh, "vendor abort")
	if !f.tools.tokens.live(f.binding) {
		t.Fatal("session token became dead after single call abort")
	}
	if pending, _, err := f.inter.ListPending(t.Context(), testAppPrincipal); err != nil || len(pending) != 0 {
		t.Fatalf("ListPending after vendor abort = (%+v, %v), want 0", pending, err)
	}
	assertStaleTicketRejected(t, f.inter, prompt.RequestID, approveOptionAllow)
}

func testCodexVendorAbortCancelsRelayed(t *testing.T) {
	t.Helper()
	f := newCodexRelayFixture(t)
	sub := f.subscribeInteractions(t)

	sessSub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sessSub.Close()
	if sendErr := f.svc.Send(t.Context(), testAppPrincipal, VendorInput{
		CommandID: "codex-abort-hold", SessionID: f.session.ID, Text: fakeCLIHold,
	}); sendErr != nil {
		t.Fatal(sendErr)
	}
	awaitSessionState(t, sessSub, f.session.ID, "running")

	callCtx, cancelCall := context.WithCancel(t.Context())
	defer cancelCall()
	decidedCh := make(chan vendor.Decision, 1)
	doneCh := make(chan struct{})
	req := &vendor.ServerRequest{
		RequestID: "601",
		TurnID:    "turn-codex-abort",
		Kind:      vendor.ServerRequestConfirm,
		Method:    codex.MethodCommandApproval,
		Summary:   "Run command",
		Detail:    `{"command":"sleep 10"}`,
		Options:   []string{codex.DecisionAccept, codex.DecisionDecline},
	}
	supervise.Go(t.Context(), "test.codex-abort", func(context.Context) error {
		defer close(doneCh)
		f.tools.RelayServerRequest(callCtx, f.session.ID, req, callbackResolveSession{
			onResolve: func(d vendor.Decision) { decidedCh <- d },
		})
		return nil
	})

	prompt := awaitRequested(t, sub)
	cancelCall()
	<-doneCh

	awaitCancelledResolvedEvent(t, sub, prompt.RequestID)
	select {
	case d := <-decidedCh:
		if d.Error == nil || d.Allow {
			t.Fatalf("Resolve after vendor abort = %+v, want error decision", d)
		}
	default:
		t.Fatal("RelayServerRequest did not resolve Codex request on abort")
	}
	if !f.tools.tokens.live(f.binding) {
		t.Fatal("session token became dead after single server request abort")
	}
	if pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal); pErr != nil || len(pending) != 0 {
		t.Fatalf("ListPending after codex abort = (%+v, %v), want 0", pending, pErr)
	}
	assertStaleTicketRejected(t, f.inter, prompt.RequestID, codex.DecisionAccept)
}
