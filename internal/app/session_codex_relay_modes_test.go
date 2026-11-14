package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/codex"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	codexAbortDrain = "codex-drain"
	codexAbortVault = "codex-vault-lock"
	codexAbortToken = "codex-revoke"
	codexAbortClose = "codex-session-close"
)

func TestFugueNeverAnswersACodexRequestInAnyMode(t *testing.T) {
	t.Parallel()
	t.Run("vault_lock", func(t *testing.T) {
		t.Parallel()
		fVault := newCodexRelayFixture(t)
		subVault := fVault.subscribeInteractions(t)
		assertCodexAbortOnFixture(t, fVault, subVault, codexAbortVault, "604")
	})

	f := newCodexRelayFixture(t)
	f.seedPermissiveCodexPolicy(t)
	sub := f.subscribeInteractions(t)
	assertCodexRelayInEveryApprovalMode(t, f, sub)
	if _, err := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "close-modes", SessionID: f.session.ID}); err != nil {
		t.Fatal(err)
	}

	// Token revoke, session close, and expiry leave the host healthy; drain (which sets
	// owner.draining permanently) runs last on this host.
	for i, kind := range []string{codexAbortToken, codexAbortClose} {
		assertCodexAbortReturnsRPCError(t, f, sub, kind, strconv.Itoa(601+i))
	}
	assertCodexExpiryReturnsRPCError(t, f, sub)
	assertCodexAbortReturnsRPCError(t, f, sub, codexAbortDrain, "603")
}

func assertCodexRelayInEveryApprovalMode(t *testing.T, f *codexRelayFixture, sub *Subscription) {
	t.Helper()
	snap, err := f.hp.vendorStore.Snapshot(t.Context(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	rev := snap.PolicyRevision
	for i, mode := range []string{vendorstore.ApprovalPlan, vendorstore.ApprovalAsk, vendorstore.ApprovalAuto, vendorstore.ApprovalFull} {
		if updErr := f.hp.vendorStore.UpdatePolicy(t.Context(), f.session.ID, rev, mode); updErr != nil {
			t.Fatal(updErr)
		}
		rev++
		id := strconv.Itoa(500 + i)
		frame := fmt.Sprintf(`{"id":%s,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"call-%s","command":"ls"}}`,
			id, codex.MethodCommandApproval, fakeCodexThreadID, id)
		if i == 0 {
			f.sendServerRequestTurn(t, "codex-mode-turn", frame)
		} else {
			f.steerServerRequest(t, "codex-mode-"+mode, frame)
		}
		prompt := awaitRequested(t, sub)
		for _, resp := range f.readRPCResponses(t) {
			if string(resp.ID) == id {
				t.Fatalf("mode %s auto-answered Codex request %s: %+v", mode, id, resp)
			}
		}
		f.answerOption(t, "cmd-mode-"+id, prompt.RequestID, codex.DecisionAccept)
		_, _ = sub.Next(t.Context())
		resp := f.awaitRPCResponse(t, id)
		if resp.Error != nil || string(resp.Result) != `{"decision":"accept"}` {
			t.Fatalf("mode %s answer failed: %+v", mode, resp)
		}
	}
}

func (f *codexRelayFixture) seedPermissiveCodexPolicy(t *testing.T) {
	t.Helper()
	if err := f.rt.tags.Set("safe_env", false); err != nil {
		t.Fatal(err)
	}
	grantTicket, _, err := f.inter.owner.Ensure(&interaction.Spec{
		OperationID:     "op-codex-seed-grant",
		Kind:            interaction.KindConfirm,
		Summary:         "seed codex grant",
		GrantScopeLabel: "all",
		Offers:          []interaction.Outcome{interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession},
		Binding: interaction.Binding{
			SessionID: f.session.ID, InvocationID: "inv-codex-seed", AttemptID: "att-seed",
			CheckGeneration: 1, ToolName: codex.MethodCommandApproval, GrantScopeKey: "Bash:safe_env", EnvTag: "safe_env", HasTarget: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ansErr := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "cmd-codex-seed-grant", Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: grantTicket.RequestID, Outcome: OutcomeAllowedSession,
	}); ansErr != nil {
		t.Fatal(ansErr)
	}
}

func assertCodexAbortReturnsRPCError(t *testing.T, base *codexRelayFixture, sub *Subscription, kind, id string) {
	t.Helper()
	f := startCodexSessionInWorkspace(t, base.hp, base.clk, base.workspaceID, base.rootDir, "codex-start-"+kind)
	assertCodexAbortOnFixture(t, f, sub, kind, id)
}

func assertCodexAbortOnFixture(t *testing.T, f *codexRelayFixture, sub *Subscription, kind, id string) {
	t.Helper()

	frame := fmt.Sprintf(`{"id":%s,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"call-abort","command":"ls"}}`,
		id, codex.MethodCommandApproval, fakeCodexThreadID)
	f.sendServerRequestTurn(t, "codex-abort-"+kind, frame)
	_ = awaitRequested(t, sub)

	switch kind {
	case codexAbortDrain:
		f.inter.CancelPending()
	case codexAbortVault:
		f.hp.Vault().Lock()
	case codexAbortToken:
		f.tools.tokens.revoke(f.session.ID)
	case codexAbortClose:
		closed, err := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "close-abort", SessionID: f.session.ID})
		if err != nil || !closed {
			t.Fatalf("Close = (%v, %v)", closed, err)
		}
	}

	resp := f.awaitRPCResponse(t, id)
	assertCodexErrorResponse(t, resp, kind)
	if got := len(f.recordedDecisions(t)); got != 0 {
		t.Fatalf("%s recorded %d ApprovalDecided events, want 0", kind, got)
	}
	if kind != codexAbortClose && kind != codexAbortVault {
		if _, cErr := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "close-after-" + kind, SessionID: f.session.ID}); cErr != nil {
			t.Fatalf("close after %s: %v", kind, cErr)
		}
	}
}

func assertCodexExpiryReturnsRPCError(t *testing.T, base *codexRelayFixture, sub *Subscription) {
	t.Helper()
	f := startCodexSessionInWorkspace(t, base.hp, base.clk, base.workspaceID, base.rootDir, "codex-start-expiry")

	// Start a turn so ActiveTurnID is unambiguous.
	if err := f.svc.Send(t.Context(), testAppPrincipal, VendorInput{CommandID: "exp-turn", SessionID: f.session.ID, Text: fakeCLIHold}); err != nil {
		t.Fatal(err)
	}

	resolvedCh := make(chan vendor.Decision, 1)
	req := &vendor.ServerRequest{
		RequestID: "701",
		TurnID:    "t-1",
		ItemID:    "call-exp",
		Method:    codex.MethodCommandApproval,
		Kind:      vendor.ServerRequestConfirm,
		Summary:   "Command: ls",
		Detail:    `{"command":"ls"}`,
		Options:   []string{codex.DecisionAccept, codex.DecisionDecline},
		ExpiresAt: f.clk.Now().UTC().Add(5 * time.Second),
	}
	supervise.Go(t.Context(), "test.codex-expiry", func(ctx context.Context) error {
		f.tools.RelayServerRequest(ctx, f.session.ID, req, callbackResolveSession{
			onResolve: func(d vendor.Decision) { resolvedCh <- d },
		})
		return nil
	})

	_ = awaitRequested(t, sub)
	f.clk.Advance(10 * time.Second)
	f.inter.ExpireExpired()

	d := <-resolvedCh
	if d.Error == nil {
		t.Fatalf("expired request resolved with decision %+v, want error", d)
	}
	raw, err := codex.NewCodec().EncodeResponse(json.RawMessage(`701`), nil, &codex.RPCError{Code: -32000, Message: d.Error.Error()})
	if err != nil {
		t.Fatal(err)
	}
	var parsed codexRecordedResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	assertCodexErrorResponse(t, parsed, "expiry")
	if got := len(f.recordedDecisions(t)); got != 0 {
		t.Fatalf("expiry recorded %d ApprovalDecided events, want 0", got)
	}
	if _, cErr := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "close-after-expiry", SessionID: f.session.ID}); cErr != nil {
		t.Fatalf("close after expiry: %v", cErr)
	}
}

type callbackResolveSession struct {
	vendor.Session
	onResolve func(vendor.Decision)
}

func (c callbackResolveSession) Resolve(_ context.Context, _ vendor.ApprovalRef, d vendor.Decision) error {
	c.onResolve(d)
	return nil
}
