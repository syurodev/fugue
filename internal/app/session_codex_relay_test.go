package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vendoradapter/codex"
	"github.com/syurodev/fugue/internal/vendorstore"
)

type codexRelayFixture struct {
	hp          *HostProcess
	rt          *HostRuntime
	svc         *VendorService
	tools       *SessionToolService
	inter       *InteractionService
	clk         *clock.Fake
	workspaceID string
	rootDir     string
	session     VendorSessionView
	binding     SessionBinding
}

func newCodexRelayFixture(t *testing.T) *codexRelayFixture {
	t.Helper()
	exe := mustExe(t)
	codexLink := filepath.Join(t.TempDir(), "codex")
	if err := os.Symlink(exe, codexLink); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(200, 0))
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir: cheapVaultDataDir(t), WorkspaceEnv: config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config: testHostConfig(), Clock: clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("codex-relay")),
		Transport: newScriptedTransport(), VendorBinary: exe, CodexBinary: codexLink,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	unlockCheapVault(t, hp)
	return startCodexRelaySession(t, hp, clk)
}

func startCodexRelaySession(t *testing.T, hp *HostProcess, clk *clock.Fake) *codexRelayFixture {
	t.Helper()
	rt := hp.Runtime()
	rootDir := t.TempDir()
	ws, err := rt.CreateWorkspace(t.Context(), testAppPrincipal, "codex-ws")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("name = \"codex-ws\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(rootDir) + "\"\naccess = \"rw\"\n")
	if _, cfgErr := rt.UpdateWorkspaceConfig(t.Context(), testAppPrincipal, ws.ID, raw, ws.ConfigHash); cfgErr != nil {
		t.Fatal(cfgErr)
	}
	return startCodexSessionInWorkspace(t, hp, clk, ws.ID, rootDir, "codex-start")
}

func startCodexSessionInWorkspace(t *testing.T, hp *HostProcess, clk *clock.Fake, workspaceID, rootDir, cmdID string) *codexRelayFixture {
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
	sub, err := rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	session, err := svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{CommandID: cmdID, Vendor: VendorCodex, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	awaitSessionState(t, sub, session.ID, "ready")
	binding, ok := tools.tokens.bindingFor(session.ID)
	if !ok {
		t.Fatalf("missing live session binding for %s", session.ID)
	}
	return &codexRelayFixture{
		hp: hp, rt: rt, svc: svc, tools: tools, inter: rt.Interactions(),
		clk: clk, workspaceID: workspaceID, rootDir: rootDir, session: session, binding: binding,
	}
}

func (f *codexRelayFixture) subscribeInteractions(t *testing.T) *Subscription {
	t.Helper()
	sub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return sub
}

func (f *codexRelayFixture) sendServerRequestTurn(t *testing.T, cmdID string, rawLines ...string) {
	t.Helper()
	text := "server-request:" + strings.Join(rawLines, "\n")
	if err := f.svc.Send(t.Context(), testAppPrincipal, VendorInput{CommandID: cmdID, SessionID: f.session.ID, Text: text}); err != nil {
		t.Fatal(err)
	}
}

func (f *codexRelayFixture) steerServerRequest(t *testing.T, cmdID string, rawLines ...string) {
	t.Helper()
	text := "server-request:" + strings.Join(rawLines, "\n")
	if err := f.svc.Steer(t.Context(), testAppPrincipal, VendorInput{CommandID: cmdID, SessionID: f.session.ID, Text: text}); err != nil {
		t.Fatal(err)
	}
}

func (f *codexRelayFixture) answerOption(t *testing.T, cmdID, requestID, optionID string) {
	t.Helper()
	if _, err := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: cmdID, Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: requestID, Outcome: OutcomeAnswered, OptionID: optionID,
	}); err != nil {
		t.Fatal(err)
	}
}

type codexRecordedResponse struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *codex.RPCError `json:"error"`
}

func (f *codexRelayFixture) awaitRPCResponse(t *testing.T, id string) codexRecordedResponse {
	t.Helper()
	var matched codexRecordedResponse
	awaitActor(t, func() bool {
		for _, resp := range f.readRPCResponses(t) {
			if string(resp.ID) == id {
				matched = resp
				return true
			}
		}
		return false
	})
	return matched
}

func (f *codexRelayFixture) readRPCResponses(t *testing.T) []codexRecordedResponse {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(f.rootDir, fakeCodexStdinFile)))
	if err != nil {
		return nil
	}
	var out []codexRecordedResponse
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg codexRecordedResponse
		if json.Unmarshal([]byte(line), &msg) == nil && msg.Method == "" && len(msg.ID) > 0 {
			out = append(out, msg)
		}
	}
	return out
}

func (f *codexRelayFixture) recordedDecisions(t *testing.T, forbidden ...string) []string {
	t.Helper()
	events, err := f.hp.vendorStore.EventsSince(t.Context(), f.session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var decisions []string
	for i := range events {
		ev := &events[i]
		rawMeta, mErr := json.Marshal(ev.Metadata)
		if mErr != nil {
			t.Fatal(mErr)
		}
		for _, needle := range forbidden {
			if needle != "" && strings.Contains(string(rawMeta), needle) {
				t.Fatalf("event metadata leaked plaintext %q: %s", needle, rawMeta)
			}
		}
		if ev.Type != vendorstore.ApprovalDecided {
			continue
		}
		if ev.Metadata.ItemMetadata == nil || ev.Metadata.ItemMetadata.Decider != adapterOriginUser {
			t.Fatalf("ApprovalDecided item metadata = %+v, want user decider", ev.Metadata.ItemMetadata)
		}
		decisions = append(decisions, ev.Metadata.ItemMetadata.Decision)
	}
	return decisions
}

func TestCodexApprovalRelaysVerbatimWithFullDetailAndIsAnsweredOnce(t *testing.T) {
	t.Parallel()
	f := newCodexRelayFixture(t)
	assertVendorRegistryProbesClaudeAndCodex(t, f)
	sub := f.subscribeInteractions(t)

	const reasonTail = "codex-plaintext-tail-must-not-hit-metadata"
	padding := strings.Repeat("p", 6*1024)
	cmdParams := fmt.Sprintf(`{"threadId":%q,"turnId":"t-1","itemId":"call-1","command":%q,"cwd":"/workspace","reason":%q}`,
		fakeCodexThreadID, "echo "+padding, reasonTail)
	var compactParams bytes.Buffer
	if err := json.Compact(&compactParams, []byte(cmdParams)); err != nil {
		t.Fatal(err)
	}
	cmdFrame := fmt.Sprintf(`{"id":101,"method":%q,"params":%s}`, codex.MethodCommandApproval, cmdParams)
	fileFrame := fmt.Sprintf(`{"id":102,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"patch-1","reason":"apply patch"}}`,
		codex.MethodFileChangeApproval, fakeCodexThreadID)
	permFrame := fmt.Sprintf(`{"id":103,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"perm-1","cwd":"/workspace","reason":"network","permissions":{"network":true}}}`,
		codex.MethodPermissionsApproval, fakeCodexThreadID)

	f.sendServerRequestTurn(t, "codex-relay-turn", cmdFrame)
	assertCommandApprovalRelay(t, f, sub, compactParams.String(), reasonTail, padding[:64])

	cmdChoices := []string{codex.DecisionAccept, codex.DecisionAcceptForSession, codex.DecisionDecline, codex.DecisionCancel}
	permChoices := []string{codex.PermissionScopeTurn, codex.PermissionScopeSession, codex.DecisionDecline}
	f.steerAndAnswerRelayedConfirm(t, sub, fileFrame, "102", cmdChoices, codex.DecisionDecline, `{"decision":"decline"}`)
	f.steerAndAnswerRelayedConfirm(t, sub, permFrame, "103", permChoices, codex.PermissionScopeSession, `{"permissions":{"network":true},"scope":"session"}`)

	wantDecisions := []string{codex.DecisionAcceptForSession, codex.DecisionDecline, codex.PermissionScopeSession}
	if diff := cmp.Diff(wantDecisions, f.recordedDecisions(t, reasonTail, padding[:64], "apply patch")); diff != "" {
		t.Fatalf("recorded ApprovalDecided decisions (-want +got):\n%s", diff)
	}
}

func assertVendorRegistryProbesClaudeAndCodex(t *testing.T, f *codexRelayFixture) {
	t.Helper()
	probes, err := f.svc.Probe(t.Context(), testAppPrincipal, []string{VendorClaude, VendorCodex})
	if err != nil || len(probes) != 2 {
		t.Fatalf("Probe([claude, codex]) = (%+v, %v)", probes, err)
	}
	if probes[0].Vendor != VendorClaude || !probes[0].VersionTested || probes[1].Vendor != VendorCodex || !probes[1].VersionTested {
		t.Fatalf("probes = %+v, want tested claude and codex", probes)
	}
}

func assertCommandApprovalRelay(
	t *testing.T,
	f *codexRelayFixture,
	sub *Subscription,
	wantDetail, secretTail, padPrefix string,
) {
	t.Helper()
	prompt := awaitRequested(t, sub)
	assertRelayedCommandPrompt(t, &prompt, wantDetail)
	f.answerOption(t, "cmd-ans-101", prompt.RequestID, codex.DecisionAcceptForSession)
	resolvedEv, ok := sub.Next(t.Context())
	if !ok || resolvedEv.Type != EventInteractionResolved ||
		strings.Contains(string(resolvedEv.Payload), secretTail) || strings.Contains(string(resolvedEv.Payload), padPrefix) {
		t.Fatalf("resolved event = (%+v, %v)", resolvedEv, ok)
	}
	resp := f.awaitRPCResponse(t, "101")
	if resp.Error != nil || string(resp.Result) != `{"decision":"acceptForSession"}` {
		t.Fatalf("RPC 101 response = %+v, want acceptForSession", resp)
	}
	_, dupErr := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "cmd-ans-101-dup", Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: prompt.RequestID, Outcome: OutcomeAnswered, OptionID: codex.DecisionAccept,
	})
	requireCode(t, dupErr, errcode.InteractionNotFound)
}

func assertRelayedCommandPrompt(t *testing.T, prompt *PendingInteractionView, wantDetail string) {
	t.Helper()
	if len(prompt.Summary) > interaction.MaxSummaryBytes || !strings.Contains(prompt.Summary, "truncated") {
		t.Fatalf("Summary len=%d (%q), want <= %d with truncation marker", len(prompt.Summary), prompt.Summary, interaction.MaxSummaryBytes)
	}
	if prompt.Detail != wantDetail {
		t.Fatalf("Detail len=%d, want compact JSON len=%d", len(prompt.Detail), len(wantDetail))
	}
	if prompt.GrantScopeLabel != "" || prompt.Risk != "" || prompt.EnvTag != "" {
		t.Fatalf("relayed prompt carried Fugue policy metadata: %+v", prompt)
	}
	wantOpts := []InteractionOption{
		{ID: codex.DecisionAccept, Label: codex.DecisionAccept},
		{ID: codex.DecisionAcceptForSession, Label: codex.DecisionAcceptForSession},
		{ID: codex.DecisionDecline, Label: codex.DecisionDecline},
		{ID: codex.DecisionCancel, Label: codex.DecisionCancel},
	}
	if diff := cmp.Diff(wantOpts, prompt.Options); diff != "" {
		t.Fatalf("relayed options (-want +got):\n%s", diff)
	}
}

func (f *codexRelayFixture) steerAndAnswerRelayedConfirm(
	t *testing.T,
	sub *Subscription,
	rawFrame, id string,
	wantOptionIDs []string,
	chooseOption, wantResult string,
) {
	t.Helper()
	f.steerServerRequest(t, "steer-req-"+id, rawFrame)
	prompt := awaitRequested(t, sub)
	gotIDs := make([]string, 0, len(prompt.Options))
	for _, o := range prompt.Options {
		gotIDs = append(gotIDs, o.ID)
	}
	if diff := cmp.Diff(wantOptionIDs, gotIDs); diff != "" {
		t.Fatalf("options for %s (-want +got):\n%s", id, diff)
	}
	f.answerOption(t, "cmd-ans-"+id, prompt.RequestID, chooseOption)
	_, _ = sub.Next(t.Context())
	resp := f.awaitRPCResponse(t, id)
	if resp.Error != nil || string(resp.Result) != wantResult {
		t.Fatalf("RPC %s response = %+v, want %s", id, resp, wantResult)
	}
}

func TestCodexApprovalOverTheDetailBoundIsAnErrorNotADecision(t *testing.T) {
	t.Parallel()
	f := newCodexRelayFixture(t)
	sub := f.subscribeInteractions(t)

	f.sendServerRequestTurn(t, "codex-oversized", "oversized")

	resp := f.awaitRPCResponse(t, "201")
	assertCodexErrorResponse(t, resp, "detail over 256 KiB")

	if pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal); pErr != nil || len(pending) != 0 {
		t.Fatalf("ListPending = (%+v, %v), want 0", pending, pErr)
	}
	if ev, hasEv := sub.popEvent(); hasEv {
		t.Fatalf("unexpected TopicInteraction event for oversized detail: %+v", ev)
	}
	if got := len(f.recordedDecisions(t)); got != 0 {
		t.Fatalf("ApprovalDecided count = %d, want 0", got)
	}
}

func assertCodexErrorResponse(t *testing.T, resp codexRecordedResponse, label string) {
	t.Helper()
	if resp.Error == nil || resp.Error.Code != -32000 || len(resp.Result) != 0 {
		t.Fatalf("%s: expected JSON-RPC error response, got %+v", label, resp)
	}
	if strings.Contains(resp.Error.Message, codex.DecisionAccept) || strings.Contains(resp.Error.Message, codex.DecisionDecline) {
		t.Fatalf("%s: error message %q must not contain accept or decline", label, resp.Error.Message)
	}
}

func TestCodexRetriedRequestIsNotAskedTwice(t *testing.T) {
	t.Parallel()
	f := newCodexRelayFixture(t)
	sub := f.subscribeInteractions(t)

	reqLine := fmt.Sprintf(`{"id":301,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"call-retry","command":"rm -rf dist"}}`,
		codex.MethodCommandApproval, fakeCodexThreadID)
	// Emit the same request ID twice in one turn (in-flight retry), then again after resolution.
	f.sendServerRequestTurn(t, "codex-retry-turn", reqLine, reqLine)

	prompt := awaitRequested(t, sub)
	f.answerOption(t, "cmd-retry-301", prompt.RequestID, codex.DecisionDecline)
	resolvedEv, ok := sub.Next(t.Context())
	if !ok || resolvedEv.Type != EventInteractionResolved {
		t.Fatalf("expected resolved event, got (%+v, %v)", resolvedEv, ok)
	}
	resp := f.awaitRPCResponse(t, "301")
	if resp.Error != nil || string(resp.Result) != `{"decision":"decline"}` {
		t.Fatalf("RPC 301 response = %+v, want decline", resp)
	}

	// Replay the same request ID after resolution and delivery.
	f.steerServerRequest(t, "codex-retry-after", reqLine)
	if pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal); pErr != nil || len(pending) != 0 {
		t.Fatalf("pending after retry = (%+v, %v), want 0", pending, pErr)
	}
	if ev, hasEv := sub.popEvent(); hasEv {
		t.Fatalf("unexpected interaction event on retry: %+v", ev)
	}
	if got := len(f.recordedDecisions(t, "rm -rf dist")); got != 1 {
		t.Fatalf("ApprovalDecided count after retry = %d, want 1", got)
	}
	count301 := 0
	for _, r := range f.readRPCResponses(t) {
		if string(r.ID) == "301" {
			count301++
		}
	}
	if count301 != 1 {
		t.Fatalf("RPC 301 response count = %d, want 1", count301)
	}
}

// TestCodexTurnInterruptAnswersRelayedRequestWithAnError covers CancelTurn for
// Codex: an interrupt while a relayed request waits closes its ticket and
// answers Codex with a JSON-RPC error, never a synthesized decision.
func TestCodexTurnInterruptAnswersRelayedRequestWithAnError(t *testing.T) {
	t.Parallel()
	f := newCodexRelayFixture(t)
	sub := f.subscribeInteractions(t)
	cmdFrame := fmt.Sprintf(`{"id":401,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"call-401","command":"sleep 5","cwd":"/workspace"}}`,
		codex.MethodCommandApproval, fakeCodexThreadID)
	f.sendServerRequestTurn(t, "codex-int-turn", cmdFrame)
	prompt := awaitRequested(t, sub)
	if _, err := f.svc.Interrupt(t.Context(), testAppPrincipal, VendorStop{CommandID: "codex-int", SessionID: f.session.ID}); err != nil {
		t.Fatal(err)
	}
	assertCodexErrorResponse(t, f.awaitRPCResponse(t, "401"), "interrupted turn")
	if pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal); pErr != nil || len(pending) != 0 {
		t.Fatalf("ListPending after interrupt = (%+v, %v), want 0", pending, pErr)
	}
	_, ansErr := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "codex-int-stale", Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: prompt.RequestID, Outcome: OutcomeAnswered, OptionID: codex.DecisionAccept,
	})
	if code := errcode.CodeOf(ansErr); code != errcode.InteractionNotFound && code != errcode.InteractionAlreadyAnswered {
		t.Fatalf("answering the closed ticket err = %v, want not_found or already_answered", ansErr)
	}
	if got := len(f.recordedDecisions(t)); got != 0 {
		t.Fatalf("interrupt recorded %d ApprovalDecided events, want 0", got)
	}
}

// TestCodexElicitationRelaysVerbatimAndIsAnsweredOnce (F9.11, VR-05): Codex
// gates an MCP tool call with `mcpServer/elicitation/request`; the request
// reaches the user verbatim with every persistence scope the gate lists, an
// accept-for-session answer carries the measured `_meta`, and a retried
// request replays the same bytes.
func TestCodexElicitationRelaysVerbatimAndIsAnsweredOnce(t *testing.T) {
	t.Parallel()
	f := newCodexRelayFixture(t)
	sub := f.subscribeInteractions(t)
	frame := fmt.Sprintf(`{"id":403,"method":%q,"params":{"threadId":%q,"turnId":"t-1","serverName":"fugue","mode":"form","message":%q,"requestedSchema":{"type":"object","properties":{}},"_meta":{"codex_approval_kind":%q,"persist":[%q,%q],"tool_name":"fugue_test_echo","tool_params":{"msg":"hi"}}}}`,
		codex.MethodElicitationRequest, fakeCodexThreadID,
		`Allow the fugue MCP server to run tool "fugue_test_echo"?`,
		codex.ElicitationApprovalKindMcpToolCall, codex.ElicitationPersistSession, codex.ElicitationPersistAlways)
	f.sendServerRequestTurn(t, "codex-elicit-turn", frame)
	prompt := awaitRequested(t, sub)
	if prompt.Kind != string(interaction.KindConfirm) ||
		!strings.Contains(prompt.Summary, `Allow the fugue MCP server to run tool "fugue_test_echo"?`) ||
		!strings.Contains(prompt.Detail, `"serverName":"fugue"`) {
		t.Fatalf("elicitation prompt = %+v, want the message and params verbatim", prompt)
	}
	optionIDs := make([]string, 0, len(prompt.Options))
	for _, option := range prompt.Options {
		optionIDs = append(optionIDs, option.ID)
	}
	if diff := cmp.Diff([]string{codex.ElicitationOptionAccept, codex.ElicitationOptionAcceptSession, codex.ElicitationOptionAcceptAlways, codex.ElicitationOptionDecline}, optionIDs); diff != "" {
		t.Fatalf("elicitation options (-want +got):\n%s", diff)
	}
	for _, resp := range f.readRPCResponses(t) {
		if string(resp.ID) == "403" {
			t.Fatalf("codex was answered without the user: %+v", resp)
		}
	}
	f.answerOption(t, "codex-elicit-ans", prompt.RequestID, codex.ElicitationOptionAcceptSession)
	assertElicitationAnswerWithPersistSession(t, f)
	// The same request ID again (a codex in-flight retry) replays the
	// recorded decision, never a second ask.
	f.steerServerRequest(t, "codex-elicit-retry", frame)
	assertElicitationRetryReplays(t, f)
}

// assertElicitationAnswerWithPersistSession checks the answer bytes: accept
// with the measured persist-session meta.
func assertElicitationAnswerWithPersistSession(t *testing.T, f *codexRelayFixture) {
	t.Helper()
	resp := f.awaitRPCResponse(t, "403")
	if resp.Error != nil || string(resp.Result) != `{"action":"accept","_meta":{"persist":"session"}}` {
		t.Fatalf("elicitation answer = %+v, want accept with persist session", resp)
	}
}

// assertElicitationRetryReplays checks the retry left nothing pending and
// replayed the recorded accept.
func assertElicitationRetryReplays(t *testing.T, f *codexRelayFixture) {
	t.Helper()
	if pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal); pErr != nil || len(pending) != 0 {
		t.Fatalf("pending after retry = (%+v, %v), want 0", pending, pErr)
	}
	retried := f.awaitRPCResponse(t, "403")
	if retried.Error != nil || strings.Contains(string(retried.Result), "decline") {
		t.Fatalf("retried elicitation = %+v, want the replayed accept", retried)
	}
}
