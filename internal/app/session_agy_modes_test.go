package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/agy"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func TestFugueNeverAnswersAnAgyPermissionRequestInAnyMode(t *testing.T) {
	t.Parallel()
	f := newAgyRelayFixture(t)
	f.seedPermissiveAgyPolicy(t)
	sub := f.subscribeInteractions(t)
	assertAgyRelayInEveryApprovalMode(t, f, sub)
	closeAgySession(t, f, "agy-close-modes")

	assertAgyAbortReturnsRPCError(t, f, sub, codexAbortToken, "601")
	assertAgyAbortReturnsRPCError(t, f, sub, codexAbortClose, "602")
	assertAgyAbortReturnsRPCError(t, f, sub, codexAbortVault, "604")
	unlockCheapVault(t, f.hp)
	assertAgyExpiryReturnsRPCError(t, f, sub)
	assertAgyAbortReturnsRPCError(t, f, sub, codexAbortDrain, "603")
}

func closeAgySession(t *testing.T, f *agyRelayFixture, cmdID string) {
	t.Helper()
	if _, err := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: cmdID, SessionID: f.session.ID}); err != nil {
		t.Fatal(err)
	}
}

func assertAgyRelayInEveryApprovalMode(t *testing.T, f *agyRelayFixture, sub *Subscription) {
	t.Helper()
	snap, err := f.hp.vendorStore.Snapshot(t.Context(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	rev := snap.PolicyRevision
	modes := []string{vendorstore.ApprovalPlan, vendorstore.ApprovalAsk, vendorstore.ApprovalAuto, vendorstore.ApprovalFull}
	frames := buildModePermissionFrames(modes)
	if err := f.hp.vendorStore.UpdatePolicy(t.Context(), f.session.ID, rev, modes[0]); err != nil {
		t.Fatal(err)
	}
	rev++
	f.sendServerRequestTurn(t, "agy-modes-turn", frames...)

	prompts := make([]PendingInteractionView, 0, len(modes))
	for range modes {
		prompts = append(prompts, awaitRequested(t, sub))
	}
	for i, mode := range modes {
		if i > 0 {
			if err := f.hp.vendorStore.UpdatePolicy(t.Context(), f.session.ID, rev, mode); err != nil {
				t.Fatal(err)
			}
			rev++
		}
		assertOneAgyModePromptAnsweredByUser(t, f, sub, mode, &prompts[i])
	}
}

func buildModePermissionFrames(modes []string) []string {
	frames := make([]string, 0, len(modes))
	for i := range modes {
		id := strconv.Itoa(500 + i)
		frames = append(frames, fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%q,"method":%q,"params":{"sessionId":%q,"toolCall":{"toolCallId":"call-%s","name":"run_command","kind":"execute","title":"ls-%s"},"options":[{"optionId":%q,"name":%q,"kind":"allow_always"},{"optionId":%q,"name":%q,"kind":"reject_once"}]}}`,
			id, agy.MethodSessionRequestPermission, fakeCodexThreadID, id, id,
			agyOptionAllowAlways, agyLabelAllowAlways, approveOptionDeny, agyLabelDeny,
		))
	}
	return frames
}

// agyAllowDecisionBody is the exact answer body the relay delivers for the
// allow option.
const agyAllowDecisionBody = `{"outcome":{"outcome":"selected","optionId":"allow"}}`

func assertOneAgyModePromptAnsweredByUser(
	t *testing.T, f *agyRelayFixture, sub *Subscription, mode string, prompt *PendingInteractionView,
) {
	t.Helper()
	id := strings.TrimPrefix(prompt.Summary, "ls-")
	for _, resp := range f.readRPCResponses(t) {
		if agy.CanonicalID(resp.ID) == id {
			t.Fatalf("mode %s auto-answered agy permission request %s: %+v", mode, id, resp)
		}
	}
	f.answerOption(t, "agy-mode-ans-"+id, prompt.RequestID, agyOptionAllowAlways)
	_, _ = sub.Next(t.Context())
	resp := f.awaitRPCResponse(t, id)
	if resp.Error != nil || string(resp.Result) != `{"outcome":{"outcome":"selected","optionId":"allow_always"}}` {
		t.Fatalf("mode %s answer failed: %+v", mode, resp)
	}
}

func (f *agyRelayFixture) seedPermissiveAgyPolicy(t *testing.T) {
	t.Helper()
	const agyEnvTag = "agy_safe_env"
	if err := f.rt.tags.Set(agyEnvTag, false); err != nil {
		t.Fatal(err)
	}
	grantTicket, _, err := f.inter.owner.Ensure(&interaction.Spec{
		OperationID:     "op-agy-seed-grant",
		Kind:            interaction.KindConfirm,
		Summary:         "seed agy grant",
		GrantScopeLabel: "agy-all",
		Offers:          []interaction.Outcome{interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession},
		Binding: interaction.Binding{
			SessionID: f.session.ID, InvocationID: "inv-agy-seed", AttemptID: "att-agy-seed",
			CheckGeneration: 1, ToolName: agy.MethodSessionRequestPermission, GrantScopeKey: "Bash:" + agyEnvTag, EnvTag: agyEnvTag, HasTarget: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ansErr := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "cmd-agy-seed-grant", Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: grantTicket.RequestID, Outcome: OutcomeAllowedSession,
	}); ansErr != nil {
		t.Fatal(ansErr)
	}
}

func assertAgyAbortReturnsRPCError(t *testing.T, base *agyRelayFixture, sub *Subscription, kind, id string) {
	t.Helper()
	f := startAgySessionInWorkspace(t, base.hp, base.clk, base.workspaceID, base.rootDir, "agy-start-"+kind)
	assertAgyAbortOnFixture(t, f, sub, kind, id)
}

func assertAgyAbortOnFixture(t *testing.T, f *agyRelayFixture, sub *Subscription, kind, id string) {
	t.Helper()
	f.sendServerRequestTurn(t, "agy-abort-"+kind, makeTestAgyPermissionFrame(id, "call-abort", "ls"))
	_ = awaitRequested(t, sub)
	triggerAgyAbortCondition(t, f, kind)
	assertAgyErrorResponse(t, f.awaitRPCResponse(t, id), kind)
	if got := len(f.recordedDecisions(t)); got != 0 {
		t.Fatalf("%s recorded %d ApprovalDecided events, want 0", kind, got)
	}
	if kind != codexAbortClose {
		if kind == codexAbortVault {
			unlockCheapVault(t, f.hp)
		}
		closeAgySession(t, f, "agy-close-after-"+kind)
	}
}

func triggerAgyAbortCondition(t *testing.T, f *agyRelayFixture, kind string) {
	t.Helper()
	switch kind {
	case codexAbortDrain:
		f.inter.CancelPending()
	case codexAbortVault:
		f.hp.Vault().Lock()
	case codexAbortToken:
		f.tools.tokens.revoke(f.session.ID)
	case codexAbortClose:
		closeAgySession(t, f, "agy-close-abort")
	}
}

func assertAgyExpiryReturnsRPCError(t *testing.T, base *agyRelayFixture, sub *Subscription) {
	t.Helper()
	f := startAgySessionInWorkspace(t, base.hp, base.clk, base.workspaceID, base.rootDir, "agy-start-expiry")
	if err := f.svc.Send(t.Context(), testAppPrincipal, VendorInput{CommandID: "agy-exp-turn", SessionID: f.session.ID, Text: fakeCLIHold}); err != nil {
		t.Fatal(err)
	}

	resolvedCh := make(chan vendor.Decision, 1)
	req := &vendor.ServerRequest{
		RequestID: "701",
		TurnID:    "t-1",
		ItemID:    "call-exp",
		Method:    agy.MethodSessionRequestPermission,
		Kind:      vendor.ServerRequestConfirm,
		Summary:   "ls",
		Detail:    `{"toolCallId":"call-exp","name":"run_command","title":"ls"}`,
		Options:   []string{approveOptionAllow, approveOptionDeny},
		OptionDetails: []vendor.ServerRequestOption{
			{ID: approveOptionAllow, Name: agyLabelAllow, Kind: agy.PermissionOptionKindAllowOnce},
			{ID: approveOptionDeny, Name: agyLabelDeny, Kind: agy.PermissionOptionKindRejectOnce},
		},
		ExpiresAt: f.clk.Now().UTC().Add(5 * time.Second),
	}
	supervise.Go(t.Context(), "test.agy-expiry", func(ctx context.Context) error {
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
	raw, err := agy.NewCodec().EncodeResponseError(json.RawMessage(`"701"`), agy.RPCErrorServerError, d.Error.Error())
	if err != nil {
		t.Fatal(err)
	}
	var parsed agyRecordedResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	assertAgyErrorResponse(t, parsed, "expiry")
	if got := len(f.recordedDecisions(t)); got != 0 {
		t.Fatalf("expiry recorded %d ApprovalDecided events, want 0", got)
	}
	closeAgySession(t, f, "agy-close-after-expiry")
}
