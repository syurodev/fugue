package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

type approveFixture struct {
	hp      *HostProcess
	rt      *HostRuntime
	svc     *VendorService
	tools   *SessionToolService
	inter   *InteractionService
	clk     *clock.Fake
	session VendorSessionView
	binding SessionBinding
}

func newApproveFixture(t *testing.T) *approveFixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(100, 0))
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir: t.TempDir(), WorkspaceEnv: config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config: testHostConfig(), Clock: clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("approve-test")),
		Transport: newScriptedTransport(), VendorBinary: exe,
		Bridge: VendorBridge{Binary: exe, Socket: filepath.Join(t.TempDir(), "host.sock")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	if _, setupErr := hp.Vault().Setup(t.Context(), redact.NewSecret("approve-test-password")); setupErr != nil {
		t.Fatal(setupErr)
	}
	return startApproveSession(t, hp, clk)
}

func startApproveSession(t *testing.T, hp *HostProcess, clk *clock.Fake) *approveFixture {
	t.Helper()
	rt := hp.Runtime()
	svc, err := rt.VendorService()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := rt.SessionTools()
	if err != nil {
		t.Fatal(err)
	}
	ws, err := rt.CreateWorkspace(t.Context(), testAppPrincipal, "approve-ws")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("name = \"approve-ws\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(t.TempDir()) + "\"\naccess = \"rw\"\n")
	if _, cfgErr := rt.UpdateWorkspaceConfig(t.Context(), testAppPrincipal, ws.ID, raw, ws.ConfigHash); cfgErr != nil {
		t.Fatal(cfgErr)
	}
	sub, err := rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	session, err := svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{CommandID: "approve-start", Vendor: VendorClaude, WorkspaceID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	awaitSessionState(t, sub, session.ID, "ready")
	if sendErr := svc.Send(t.Context(), testAppPrincipal, VendorInput{CommandID: "approve-turn", SessionID: session.ID, Text: fakeCLIHold}); sendErr != nil {
		t.Fatal(sendErr)
	}
	tools.tokens.mu.Lock()
	binding, ok := tools.tokens.bySession[session.ID]
	tools.tokens.mu.Unlock()
	if !ok {
		t.Fatalf("missing live session binding for %s", session.ID)
	}
	return &approveFixture{hp: hp, rt: rt, svc: svc, tools: tools, inter: rt.Interactions(), clk: clk, session: session, binding: binding}
}

func (f *approveFixture) subscribeInteractions(t *testing.T) *Subscription {
	t.Helper()
	sub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return sub
}

func (f *approveFixture) startApprove(t *testing.T, name, toolName string, input json.RawMessage, toolUseID string) <-chan FugueToolResult {
	t.Helper()
	args, err := json.Marshal(struct {
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	}{ToolName: toolName, Input: input, ToolUseID: toolUseID})
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan FugueToolResult, 1)
	supervise.Go(t.Context(), "test."+name, func(ctx context.Context) error {
		res, callErr := f.tools.Call(ctx, f.binding, approveToolName, args)
		if callErr != nil {
			t.Errorf("%s Call error: %v", name, callErr)
		}
		ch <- res
		return nil
	})
	return ch
}

func (f *approveFixture) answerAllow(t *testing.T, cmdID, requestID string) {
	t.Helper()
	if _, err := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: cmdID, Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: requestID, Outcome: OutcomeAnswered, OptionID: approveOptionAllow,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *approveFixture) answerDeny(t *testing.T, cmdID, requestID, note string) {
	t.Helper()
	if _, err := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: cmdID, Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: requestID, Outcome: OutcomeDenied, Text: note,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *approveFixture) recordedDecisions(t *testing.T) []string {
	t.Helper()
	events, err := f.hp.vendorStore.EventsSince(t.Context(), f.session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var decisions []string
	for i := range events {
		ev := &events[i]
		if ev.Type != vendorstore.ApprovalDecided {
			continue
		}
		rawMeta, mErr := json.Marshal(ev.Metadata)
		if mErr != nil {
			t.Fatal(mErr)
		}
		if strings.Contains(string(rawMeta), "plaintext-must-not-hit-metadata") || strings.Contains(string(rawMeta), "echo hi") {
			t.Fatalf("ApprovalDecided metadata leaked plaintext input: %s", rawMeta)
		}
		if ev.Metadata.ItemMetadata == nil || ev.Metadata.ItemMetadata.Decider != "user" {
			t.Fatalf("ApprovalDecided item metadata = %+v, want user decider", ev.Metadata.ItemMetadata)
		}
		decisions = append(decisions, ev.Metadata.ItemMetadata.Decision)
	}
	return decisions
}

func TestClaudeApprovalRelaysVerbatimAndReturnsAllowOrDenyWithNote(t *testing.T) {
	t.Parallel()
	f := newApproveFixture(t)
	assertClaudeBridgeArgvAndList(t, f)
	sub := f.subscribeInteractions(t)

	allowInput := json.RawMessage(`{"command":"echo hi","nested":[1,2]}`)
	allowRes := f.runVerbatimAllow(t, sub, allowInput)
	assertAllowResult(t, allowRes, allowInput)

	denyInput := json.RawMessage(`{"path":"/tmp/x","secret":"plaintext-must-not-hit-metadata"}`)
	denyCh := f.startApprove(t, "deny", "Write", denyInput, "tu:deny:2")
	denyPrompt := awaitRequested(t, sub)
	f.answerDeny(t, "cmd-deny-2", denyPrompt.RequestID, "use workspace root instead")
	assertDenyResult(t, <-denyCh, "use workspace root instead")

	if diff := cmp.Diff([]string{approveOptionAllow, approveOptionDeny}, f.recordedDecisions(t)); diff != "" {
		t.Fatalf("recorded ApprovalDecided decisions (-want +got):\n%s", diff)
	}
}

func assertClaudeBridgeArgvAndList(t *testing.T, f *approveFixture) {
	t.Helper()
	argv, err := claude.BuildArgv(claude.DefaultBinary, &vendor.StartSpec{
		Bridge: vendor.Bridge{Binary: f.svc.bridge.Binary, Socket: f.svc.bridge.Socket},
	})
	if err != nil || !slices.Contains(argv, "--permission-prompt-tool") || !slices.Contains(argv, "mcp__fugue__approve") {
		t.Fatalf("BuildArgv = (%q, %v), want --permission-prompt-tool mcp__fugue__approve", argv, err)
	}
	listed, err := f.tools.List(f.binding)
	if err != nil || len(listed) != 1 || listed[0].Name != approveToolName {
		t.Fatalf("List = (%+v, %v), want [approve]", listed, err)
	}
}

func (f *approveFixture) runVerbatimAllow(t *testing.T, sub *Subscription, allowInput json.RawMessage) FugueToolResult {
	t.Helper()
	ch := f.startApprove(t, "allow", "Bash", allowInput, "tu:allow:1")
	prompt := awaitRequested(t, sub)
	if !strings.Contains(prompt.Summary, "Bash") || !strings.Contains(prompt.Summary, `"echo hi"`) {
		t.Fatalf("relayed allow summary = %q, want tool_name and input verbatim", prompt.Summary)
	}
	if prompt.GrantScopeLabel != "" || prompt.Risk != "" || prompt.EnvTag != "" {
		t.Fatalf("relayed prompt carried Fugue policy metadata: %+v", prompt)
	}
	if diff := cmp.Diff([]string{OutcomeAnswered, OutcomeDenied}, prompt.OfferedOutcomes); diff != "" {
		t.Fatalf("offered outcomes (-want +got):\n%s", diff)
	}
	f.answerAllow(t, "cmd-allow-1", prompt.RequestID)
	return <-ch
}

func assertAllowResult(t *testing.T, res FugueToolResult, wantInput json.RawMessage) {
	t.Helper()
	if res.IsError {
		t.Fatalf("allow returned tool error: %+v", res)
	}
	var got struct {
		Behavior     string          `json:"behavior"`
		UpdatedInput json.RawMessage `json:"updatedInput"`
	}
	if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Behavior != approveOptionAllow || string(got.UpdatedInput) != string(wantInput) {
		t.Fatalf("allow response = %s, want behavior=allow with original updatedInput", res.Text)
	}
}

func assertDenyResult(t *testing.T, res FugueToolResult, wantMsg string) {
	t.Helper()
	if res.IsError {
		t.Fatalf("deny returned tool error: %+v", res)
	}
	var got struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Behavior != approveOptionDeny || got.Message != wantMsg {
		t.Fatalf("deny response = %s, want behavior=deny with message %q", res.Text, wantMsg)
	}
}

func (f *approveFixture) seedPermissivePolicy(t *testing.T) {
	t.Helper()
	if err := f.rt.tags.Set("safe_env", false); err != nil {
		t.Fatal(err)
	}
	grantTicket, _, err := f.inter.owner.Ensure(&interaction.Spec{
		OperationID:     "op-seed-grant",
		Kind:            interaction.KindConfirm,
		Summary:         "seed grant",
		GrantScopeLabel: "all",
		Offers:          []interaction.Outcome{interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession},
		Binding: interaction.Binding{
			SessionID: f.session.ID, InvocationID: "inv-seed", AttemptID: "att-seed",
			CheckGeneration: 1, ToolName: "Bash", GrantScopeKey: "Bash:safe_env", EnvTag: "safe_env", HasTarget: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ansErr := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "cmd-seed-grant", Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: grantTicket.RequestID, Outcome: OutcomeAllowedSession,
	}); ansErr != nil {
		t.Fatal(ansErr)
	}
}

func TestFugueNeverAnswersAVendorRequestInAnyMode(t *testing.T) {
	t.Parallel()
	f := newApproveFixture(t)
	f.seedPermissivePolicy(t)
	sub := f.subscribeInteractions(t)

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
		ch := f.startApprove(t, "mode-"+mode, "Bash", json.RawMessage(`{"command":"ls"}`), fmt.Sprintf("tu-mode-%d", i))
		prompt := awaitRequested(t, sub)
		select {
		case res := <-ch:
			t.Fatalf("mode %s auto-answered approve: %+v", mode, res)
		default:
		}
		f.answerAllow(t, fmt.Sprintf("cmd-mode-%d", i), prompt.RequestID)
		if res := <-ch; res.IsError {
			t.Fatalf("mode %s answer failed: %+v", mode, res)
		}
	}

	assertAbortWaitToolError(t, "revoke", f, sub, func() { f.tools.tokens.revoke(f.session.ID) })
	assertAbortWaitToolError(t, "vault lock", newApproveFixture(t), nil, nil)
	assertAbortWaitToolError(t, "drain", newApproveFixture(t), nil, nil)
}

func assertAbortWaitToolError(t *testing.T, kind string, f *approveFixture, sub *Subscription, trigger func()) {
	t.Helper()
	if sub == nil {
		sub = f.subscribeInteractions(t)
	}
	ch := f.startApprove(t, kind, "Bash", json.RawMessage(`{"command":"ls"}`), "tu-"+kind)
	_ = awaitRequested(t, sub)
	switch kind {
	case "vault lock":
		f.hp.Vault().Lock()
	case "drain":
		f.inter.CancelPending()
	default:
		trigger()
	}
	assertGenericToolError(t, <-ch, kind)
	if kind == "vault lock" {
		lockedRes, callErr := f.tools.Call(t.Context(), f.binding, approveToolName, []byte(`{"tool_name":"Bash","input":{},"tool_use_id":"tu-locked-entry"}`))
		if callErr != nil {
			t.Fatal(callErr)
		}
		assertGenericToolError(t, lockedRes, "already locked")
	}
}

func assertGenericToolError(t *testing.T, res FugueToolResult, label string) {
	t.Helper()
	if !res.IsError || res.Text != approveUnavailableMessage ||
		strings.Contains(res.Text, approveOptionAllow) || strings.Contains(res.Text, approveOptionDeny) {
		t.Fatalf("%s returned %+v, want fixed generic tool error", label, res)
	}
}

func TestApprovalWaitsWithoutAClientAndIsAnsweredOnce(t *testing.T) {
	t.Parallel()
	f := newApproveFixture(t)

	// No client is subscribed to TopicInteraction; advance the fake clock while waiting.
	ch := f.startApprove(t, "no-client", "Read", json.RawMessage(`{"path":"go.mod"}`), "tu-wait-1")
	awaitActor(t, func() bool {
		pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal)
		return pErr == nil && len(pending) == 1
	})
	f.clk.Advance(time.Hour)
	select {
	case res := <-ch:
		t.Fatalf("approve returned before any client answered: %+v", res)
	default:
	}

	pending, _, err := f.inter.ListPending(t.Context(), testAppPrincipal)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListPending = (%+v, %v), want 1 waiting prompt", pending, err)
	}
	f.answerAllow(t, "cmd-wait-allow", pending[0].RequestID)
	res := <-ch
	if res.IsError || !strings.Contains(res.Text, `"behavior":"allow"`) {
		t.Fatalf("after client connected = %+v", res)
	}

	// After delivery TakeRelayed has already removed the ticket; a late Answer
	// cannot re-trigger delivery or reuse the request ID.
	_, err = f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "cmd-wait-dup", Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: pending[0].RequestID, Outcome: OutcomeAnswered, OptionID: approveOptionAllow,
	})
	if errcode.CodeOf(err) != errcode.InteractionNotFound {
		t.Fatalf("second answer after delivery = %v, want not_found", err)
	}
}

func TestRetriedVendorRequestIsNotAskedTwice(t *testing.T) {
	t.Parallel()
	f := newApproveFixture(t)
	sub := f.subscribeInteractions(t)

	input := json.RawMessage(`{"command":"rm -rf build"}`)
	firstCh := f.startApprove(t, "first", "Bash", input, "tu-retry-1")
	prompt := awaitRequested(t, sub)
	f.answerDeny(t, "cmd-retry-deny", prompt.RequestID, "keep build dir")
	firstRes := <-firstCh
	if firstRes.IsError || firstRes.Text != `{"behavior":"deny","message":"keep build dir"}` {
		t.Fatalf("first response = %+v", firstRes)
	}
	resolvedEv, ok := sub.Next(t.Context())
	if !ok || resolvedEv.Type != EventInteractionResolved {
		t.Fatalf("expected resolved event for first request, got (%+v, %v)", resolvedEv, ok)
	}

	// Retry with the same key returns the same decision without creating a second prompt.
	retryRes := <-f.startApprove(t, "retry", "Bash", input, "tu-retry-1")
	if retryRes != firstRes {
		t.Fatalf("retry response = %+v, want %+v", retryRes, firstRes)
	}
	if pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal); pErr != nil || len(pending) != 0 {
		t.Fatalf("pending after retry = (%+v, %v), want 0", pending, pErr)
	}
	if ev, hasEv := sub.popEvent(); hasEv {
		t.Fatalf("unexpected interaction event on retry: %+v", ev)
	}
	if got := len(f.recordedDecisions(t)); got != 1 {
		t.Fatalf("ApprovalDecided count after retry = %d, want 1", got)
	}
	assertTombstonesBoundedAndFreedOnClose(t, f)
}

func assertTombstonesBoundedAndFreedOnClose(t *testing.T, f *approveFixture) {
	t.Helper()
	for i := range approveMaxTombstonesPerSess + 10 {
		op := fmt.Sprintf("op-bound-%03d", i)
		f.tools.tombstones.put(f.session.ID, op, [32]byte{byte(i)}, true, "")
	}
	f.tools.tombstones.mu.Lock()
	sessEntries := len(f.tools.tombstones.sessions[f.session.ID].entries)
	f.tools.tombstones.mu.Unlock()
	if sessEntries != approveMaxTombstonesPerSess {
		t.Fatalf("tombstone entry count = %d, want bounded %d", sessEntries, approveMaxTombstonesPerSess)
	}
	closed, err := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "cmd-close-retry", SessionID: f.session.ID})
	if err != nil || !closed {
		t.Fatalf("Close = (%v, %v)", closed, err)
	}
	f.tools.tombstones.mu.Lock()
	_, exists := f.tools.tombstones.sessions[f.session.ID]
	f.tools.tombstones.mu.Unlock()
	if exists {
		t.Fatalf("session tombstones were not freed on session close")
	}
}
