package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/secretstore"
)

func TestSecretServiceRedactsStoredSecretsOnTranscript(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	hp := startAppStateHost(t, t.TempDir(), clk)
	closeAppStateHost(t, hp)

	ctx := t.Context()
	if _, setupErr := hp.rt.vaultService.Setup(ctx, testAppPrincipal, []byte(testAppStateMasterUnlock)); setupErr != nil {
		t.Fatalf("Setup vault: %v", setupErr)
	}
	svc, svcErr := hp.rt.SecretService()
	if svcErr != nil {
		t.Fatalf("SecretService: %v", svcErr)
	}
	sub, subErr := hp.rt.hub.Subscribe(ctx, SubscribeParams{Topic: TopicSecrets, Principal: testAppPrincipal})
	if subErr != nil {
		t.Fatalf("Subscribe secrets: %v", subErr)
	}
	t.Cleanup(sub.Close)
	if snapData, _, ok := sub.Snapshot(); !ok || len(snapData) == 0 {
		t.Fatalf("expected initial snapshot on secrets topic, got ok=%v len=%d", ok, len(snapData))
	}

	canary := "my-custom-vault-canary-987654"
	meta := assertPutSecretAndRedaction(t, hp, svc, sub, canary)
	assertDeleteSecretAndEvent(t, svc, sub, meta.SecretID, canary)
}

func assertPutSecretAndRedaction(t *testing.T, hp *HostProcess, svc *SecretService, sub *Subscription, canary string) SecretMetaView {
	t.Helper()
	ctx := t.Context()
	val := []byte(canary)
	meta, putErr := svc.PutSecret(ctx, &PutSecretCommand{
		Principal:   testAppPrincipal,
		CommandID:   "sec-put-1",
		ScopeKind:   secretstore.ScopeGlobal,
		Name:        "MY_CUSTOM_TOKEN",
		Description: "Canary token",
		Value:       val,
	})
	if putErr != nil {
		t.Fatalf("PutSecret: %v", putErr)
	}
	if !bytes.Equal(val, make([]byte, len(val))) {
		t.Fatalf("PutSecret did not clear value buffer: %q", val)
	}
	ev, ok := sub.Next(ctx)
	if !ok || ev.Type != EventSecretPut || strings.Contains(string(ev.Payload), canary) {
		t.Fatalf("unexpected secret_put event: ev=%+v ok=%v", ev, ok)
	}
	var wire secretEventWire
	if unmErr := json.Unmarshal(ev.Payload, &wire); unmErr != nil || wire.SecretID != meta.SecretID {
		t.Fatalf("decode secret_put event = (%+v, %v)", wire, unmErr)
	}
	got := hp.rt.transcripts.redactor.Redact("token is " + canary + " end")
	if strings.Contains(got, canary) || !strings.Contains(got, "[secret:vault]") {
		t.Fatalf("Redact = %q, want [secret:vault] without canary", got)
	}
	if _, listErr := svc.ListSecrets(ctx, "cli:1", "", ""); errcode.CodeOf(listErr) != errcode.PermissionDenied {
		t.Fatalf("ListSecrets(cli) code = %v, want permission_denied", errcode.CodeOf(listErr))
	}
	return meta
}

func assertDeleteSecretAndEvent(t *testing.T, svc *SecretService, sub *Subscription, secretID, canary string) {
	t.Helper()
	ctx := t.Context()
	deleted, delErr := svc.DeleteSecret(ctx, &DeleteSecretCommand{
		Principal: testAppPrincipal,
		CommandID: "sec-del-1",
		SecretID:  secretID,
	})
	if delErr != nil || !deleted {
		t.Fatalf("DeleteSecret = (%v, %v)", deleted, delErr)
	}
	ev2, ok := sub.Next(ctx)
	if !ok || ev2.Type != EventSecretDeleted || strings.Contains(string(ev2.Payload), canary) {
		t.Fatalf("unexpected secret_deleted event: ev=%+v ok=%v", ev2, ok)
	}
}

func TestAnswerInteractionSecretValidationAndPrincipalGate(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	hp := startAppStateHost(t, t.TempDir(), clk)
	closeAppStateHost(t, hp)

	ctx := t.Context()
	if _, setupErr := hp.rt.vaultService.Setup(ctx, testAppPrincipal, []byte(testAppStateMasterUnlock)); setupErr != nil {
		t.Fatalf("Setup vault: %v", setupErr)
	}
	opID, opErr := interaction.VendorOperationKey("sess-1", "turn-sec-gate", "vr-sec-gate")
	if opErr != nil {
		t.Fatalf("VendorOperationKey: %v", opErr)
	}
	ticket, err := hp.rt.interactions.Ensure(&interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindQuestion,
		Relayed:     true,
		Summary:     "Secret prompt",
		Questions:   []interaction.Question{{ID: "q1", Question: "Token?", Secret: true}},
		Binding:     interaction.Binding{SessionID: "sess-1", TurnID: "turn-sec-gate", VendorRequestID: "vr-sec-gate"},
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Non-app principal cannot answer with secret fields.
	if _, cliErr := hp.rt.AnswerInteraction(ctx, &AnswerInteractionCommand{
		CommandID: "c-cli", Principal: "cli:1", Incarnation: hp.rt.interactions.Incarnation(),
		RequestID: ticket.RequestID, Outcome: OutcomeAnswered,
		Answers: []InteractionAnswer{{QuestionID: "q1", SecretID: "sec-1"}},
	}); errcode.CodeOf(cliErr) != errcode.PermissionDenied {
		t.Fatalf("cli secret Answer code = %v, want permission_denied", errcode.CodeOf(cliErr))
	}
	// SaveSecretName without SecretValue is rejected without touching secret_records.
	if _, saveErr := hp.rt.AnswerInteraction(ctx, &AnswerInteractionCommand{
		CommandID: "c-nosv", Principal: testAppPrincipal, Incarnation: hp.rt.interactions.Incarnation(),
		RequestID: ticket.RequestID, Outcome: OutcomeAnswered,
		Answers: []InteractionAnswer{{QuestionID: "q1", OptionIDs: []string{}, SaveSecretName: "SHOULD_NOT_SAVE"}},
	}); errcode.CodeOf(saveErr) != errcode.InvalidArgument {
		t.Fatalf("SaveSecretName without SecretValue code = %v, want invalid_argument", errcode.CodeOf(saveErr))
	}
	svc, svcErr := hp.rt.SecretService()
	if svcErr != nil {
		t.Fatalf("SecretService: %v", svcErr)
	}
	if list, listErr := svc.ListSecrets(ctx, testAppPrincipal, "", ""); listErr != nil || len(list) != 0 {
		t.Fatalf("ListSecrets after rejected Answer = (%+v, %v), want empty", list, listErr)
	}
}
