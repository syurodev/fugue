package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

var errSimulatedSpawn = errcode.New(errcode.Internal, "simulated spawn failure")

type vaultLockFixture struct {
	hp      *HostProcess
	rt      *HostRuntime
	tools   *SessionToolService
	binding SessionBinding
	runsMu  sync.Mutex
	runs    int
}

func newVaultLockFixture(t *testing.T) *vaultLockFixture {
	t.Helper()
	f := &vaultLockFixture{}
	deploy := &FugueTool{
		Name: toolDeploy, Description: "Deploy.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"env":{"type":"string"}},"additionalProperties":false}`),
		Descriptor:  interaction.Descriptor{Risk: interaction.RiskWrite, Confirm: interaction.ConfirmEnvTag},
		Plan:        deployPlan,
		Run: func(context.Context, string, json.RawMessage) (string, error) {
			f.runsMu.Lock()
			defer f.runsMu.Unlock()
			f.runs++
			return "deployed-ok", nil
		},
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.Real{}
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir: t.TempDir(), WorkspaceEnv: config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config: testHostConfig(), Clock: clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("vault-lock-test")),
		Transport: newScriptedTransport(), VendorBinary: exe, TestFugueTools: []*FugueTool{deploy},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	tools, err := hp.Runtime().SessionTools()
	if err != nil {
		t.Fatal(err)
	}
	f.hp, f.rt, f.tools, f.binding = hp, hp.Runtime(), tools, seedLockedSession(t, tools)
	return f
}

func seedLockedSession(t *testing.T, tools *SessionToolService) SessionBinding {
	t.Helper()
	vs, ok := tools.sessions.(*vendorstore.Store)
	if !ok {
		t.Fatalf("tools.sessions = %T, want *vendorstore.Store", tools.sessions)
	}
	if err := vs.CreateSession(t.Context(), &vendorstore.Session{
		ID: "sess-vault-lock", Vendor: VendorClaude, AccountID: vendorDefaultAccount,
		Role: vendorSessionRole, State: vendorstore.StateReady, ApprovalMode: vendorstore.ApprovalAuto,
		WorkspaceID: testWorkspaceID, WorkspaceRoot: "default",
	}); err != nil {
		t.Fatal(err)
	}
	secret, err := tools.tokens.mint("sess-vault-lock", vendorSessionRole)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tools.Attach(secret.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func (f *vaultLockFixture) assertLockedProdDenied(t *testing.T, label string) {
	t.Helper()
	res, err := f.tools.Call(t.Context(), f.binding, toolDeploy, []byte(`{"env":"prod"}`))
	if err != nil || !res.IsError {
		t.Fatalf("%s Call = %+v, %v; want error result", label, res, err)
	}
	pending, _, err := f.rt.Interactions().ListPending(t.Context(), string(KindApp))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("%s pending = %d, want 0", label, len(pending))
	}
}

func (f *vaultLockFixture) startProdCall(t *testing.T, sub *Subscription) (PendingInteractionView, <-chan FugueToolResult) {
	t.Helper()
	callCh := make(chan FugueToolResult, 1)
	supervise.Go(t.Context(), "test.vault-lock-call", func(ctx context.Context) error {
		r, callErr := f.tools.Call(ctx, f.binding, toolDeploy, []byte(`{"env":"prod"}`))
		if callErr != nil {
			t.Errorf("prod call: %v", callErr)
		}
		callCh <- r
		return nil
	})
	return awaitRequested(t, sub), callCh
}

// TestSessionToolConfirmationFailsClosedWhileVaultLocked verifies that
// confirmation fails closed initially (before unlock), when vault state is
// unknown, and after Lock (which also cancels pending confirmations).
func TestSessionToolConfirmationFailsClosedWhileVaultLocked(t *testing.T) {
	t.Parallel()
	f := newVaultLockFixture(t)

	// 1. Initial state is locked, and unknown vault state fails closed.
	f.assertLockedProdDenied(t, "initial locked")
	savedVault := f.hp.vaultRef.Swap(nil)
	f.assertLockedProdDenied(t, "unknown vault")
	f.hp.vaultRef.Store(savedVault)

	// 2. Unlock, open a confirmation, then Lock to cancel it.
	if _, err := f.hp.Vault().Setup(t.Context(), redact.NewSecret("vault-lock-test-password")); err != nil {
		t.Fatal(err)
	}
	sub, err := f.rt.hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindApp), Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)

	prompt, callCh := f.startProdCall(t, sub)
	f.hp.Vault().Lock()
	if got := <-callCh; !got.IsError {
		t.Fatalf("pending call after Lock = %+v, want error result", got)
	}
	if _, ansErr := f.rt.Interactions().Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "ans-after-lock", Principal: string(KindApp), Incarnation: prompt.Incarnation, RequestID: prompt.RequestID, Outcome: OutcomeAllowedOnce,
	}); errcode.CodeOf(ansErr) == "" {
		t.Fatal("answering cancelled confirmation after Lock succeeded unexpectedly")
	}
	f.assertLockedProdDenied(t, "post-Lock")

	// 3. Unlock restores confirmations so an allowed call runs the tool.
	if unlockErr := f.hp.Vault().Unlock(t.Context(), redact.NewSecret("vault-lock-test-password")); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	prompt, callCh = f.startProdCall(t, sub)
	if _, ansErr := f.rt.Interactions().Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "ans-after-unlock", Principal: string(KindApp), Incarnation: prompt.Incarnation, RequestID: prompt.RequestID, Outcome: OutcomeAllowedOnce,
	}); ansErr != nil {
		t.Fatal(ansErr)
	}
	if got := <-callCh; got.IsError || got.Text != "deployed-ok" {
		t.Fatalf("call after unlock = %+v, want deployed-ok", got)
	}
	f.runsMu.Lock()
	defer f.runsMu.Unlock()
	if f.runs != 1 {
		t.Fatalf("tool runs = %d, want 1", f.runs)
	}
}

func assertTokenStillRunsEcho(t *testing.T, tools *SessionToolService, binding SessionBinding, stage string) {
	t.Helper()
	if !tools.tokens.live(binding) {
		t.Fatalf("%s: binding no longer live", stage)
	}
	res, err := tools.Call(t.Context(), binding, toolEcho, []byte(`{}`))
	if err != nil || res.IsError || res.Text != "ok" {
		t.Fatalf("%s: Call = %+v, %v", stage, res, err)
	}
	if len(tools.tokens.pending) != 0 {
		t.Fatalf("%s: leaked pending tokens = %d", stage, len(tools.tokens.pending))
	}
}

// TestRejectedResumeKeepsLiveSessionToken verifies that a rejected Resume
// (live actor, reserveSlot FailedPrecondition, or spawn failure) leaves an
// existing session's token binding untouched and discards only the newly
// minted pending token.
func TestRejectedResumeKeepsLiveSessionToken(t *testing.T) {
	t.Parallel()
	echo := NewReadOnlyTool(toolEcho, "Echo.", json.RawMessage(`{"type":"object"}`),
		func(context.Context, string, json.RawMessage) (string, error) { return "ok", nil })
	f := newVendorServiceFixtureWith(t, []*FugueTool{echo})
	tools, err := f.rt.SessionTools()
	if err != nil {
		t.Fatal(err)
	}
	s1 := f.start(t, "resume-token-s1")
	s1Secret, err := tools.tokens.mint(s1.ID, vendorSessionRole)
	if err != nil {
		t.Fatal(err)
	}
	s1Binding, err := tools.Attach(s1Secret.Reveal())
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.svc.Resume(t.Context(), testAppPrincipal, &ResumeVendorSession{CommandID: "resume-live", SessionID: s1.ID})
	requireCode(t, err, errcode.VendorSessionState)
	assertTokenStillRunsEcho(t, tools, s1Binding, "live actor rejected Resume")

	capSpec := &vendor.StartSpec{SessionID: s1.ID, Cwd: t.TempDir()}
	var capToken string
	err = f.svc.spawn(capSpec, vendorSessionRole, func() error {
		capToken = capSpec.SessionToken.Reveal()
		return f.svc.sessions.Resume(t.Context(), s1.ID, capSpec)
	})
	requireCode(t, err, errcode.FailedPrecondition)
	assertTokenStillRunsEcho(t, tools, s1Binding, "reserveSlot rejected spawn")
	if _, ok := tools.tokens.verify(capToken); ok {
		t.Fatal("reserveSlot-rejected spawn's pending token remained valid")
	}

	failSpec := &vendor.StartSpec{SessionID: s1.ID, Cwd: t.TempDir()}
	var failToken string
	err = f.svc.spawn(failSpec, vendorSessionRole, func() error {
		failToken = failSpec.SessionToken.Reveal()
		return errSimulatedSpawn
	})
	if !errors.Is(err, errSimulatedSpawn) {
		t.Fatalf("spawn error = %v, want %v", err, errSimulatedSpawn)
	}
	assertTokenStillRunsEcho(t, tools, s1Binding, "errored spawn")
	if _, ok := tools.tokens.verify(failToken); failToken == "" || ok {
		t.Fatalf("failed spawn pending token valid=%v, len=%d", ok, len(failToken))
	}
}

// TestSessionToolCallRechecksTokenAfterConfirmation verifies that revoking or
// replacing a session token while a confirmation wait is open denies the call
// at the execution boundary before running the tool.
func TestSessionToolCallRechecksTokenAfterConfirmation(t *testing.T) {
	t.Parallel()
	cases := map[string]func(f *toolFixture){
		"revoked": func(f *toolFixture) { f.svc.tokens.revoke(sessionLead) },
		"replaced": func(f *toolFixture) {
			if _, err := f.svc.tokens.mint(sessionLead, roleLead); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, invalidate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newToolFixture(t)
			lead, _ := f.attach(t, sessionLead, roleLead)

			call := f.callDeploy(t.Context(), lead, `{"env":"prod"}`)
			prompt := f.awaitPrompt(t)

			invalidate(f)
			if ansErr := f.answer(t, &prompt, OutcomeAllowedOnce); ansErr != nil {
				t.Fatal(ansErr)
			}

			<-call.done
			if errcode.CodeOf(call.err) != errcode.PermissionDenied {
				t.Fatalf("call error = %v (code %q), want permission_denied", call.err, errcode.CodeOf(call.err))
			}
			if got := f.ran(); len(got) != 0 {
				t.Fatalf("tool ran after token %s: %v", name, got)
			}
		})
	}
}
