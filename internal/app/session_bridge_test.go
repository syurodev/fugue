package app

// F9.11 evidence: the fugue bridge attached to Codex and agy sessions. The
// Fugue tools of a Codex or agy session go through Decide exactly like
// Claude's (VR-13), vendor approvals stay relayed verbatim with the bridge
// attached (VR-05), and the session token is in neither argv nor any file
// nor the logs (host-api §7).

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/agy"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendoradapter/codex"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// bridgeTokenSeed derives the session tokens of the bridged hosts: the n-th
// spawn's token is the n-th 32 bytes of the same stream. No test reads a
// token from the host.
const bridgeTokenSeed = "bridge-token-seed"

// bridgedDeployTool is the deploy tool of the full-host fixtures: its target
// tag rides the arguments, so the env-tag hard rule (I-03) applies.
func bridgedDeployTool(ran *int, mu *sync.Mutex) *FugueTool {
	return &FugueTool{
		Name: toolDeploy, Description: "Deploy.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"env":{"type":"string"}},"additionalProperties":false}`),
		Descriptor:  interaction.Descriptor{Risk: interaction.RiskWrite, Confirm: interaction.ConfirmEnvTag},
		Plan:        deployPlan,
		Run: func(context.Context, string, json.RawMessage) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			*ran++
			return deployedOKText, nil
		},
	}
}

// startBridgedVendorHost starts a host with the bridge dep set — production
// wiring always sets it — and one deploy tool registered for tests. The
// vendor binaries are symlinks named after each CLI, so the fake's --version
// reports the tested version of that vendor.
func startBridgedVendorHost(t *testing.T, ran *int, mu *sync.Mutex) (*HostProcess, *VendorService, *SessionToolService) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(300, 0))
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir: cheapVaultDir(t), WorkspaceEnv: config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config: testHostConfig(), Clock: clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("bridge-host")),
		Random:    ids.NewDeterministic(bridgeTokenSeed),
		Transport: newScriptedTransport(), VendorBinary: exe,
		CodexBinary:    vendorLink(t, exe, "codex"),
		AgyBinary:      vendorLink(t, exe, "agy"),
		Bridge:         VendorBridge{Binary: exe, Socket: filepath.Join(t.TempDir(), "host.sock")},
		TestFugueTools: []*FugueTool{bridgedDeployTool(ran, mu)},
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
	rt := hp.Runtime()
	svc, err := rt.VendorService()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := rt.SessionTools()
	if err != nil {
		t.Fatal(err)
	}
	return hp, svc, tools
}

// startBridgedSession starts a vendor session of vendor in its own workspace
// root and returns the fixture parts the tests need.
func startBridgedSession(t *testing.T, rt *HostRuntime, svc *VendorService, vendorName, cmdID, vendorEnum string) (string, string, SessionBinding) {
	t.Helper()
	root := t.TempDir()
	ws, err := rt.CreateWorkspace(t.Context(), testAppPrincipal, vendorName+"-ws")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("name = \"" + vendorName + "-ws\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(root) + "\"\naccess = \"rw\"\n")
	if _, cfgErr := rt.UpdateWorkspaceConfig(t.Context(), testAppPrincipal, ws.ID, raw, ws.ConfigHash); cfgErr != nil {
		t.Fatal(cfgErr)
	}
	sub, err := rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	session, err := svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{CommandID: cmdID, Vendor: vendorEnum, WorkspaceID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	awaitSessionState(t, sub, session.ID, "ready")
	tools, err := rt.SessionTools()
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := tools.tokens.bindingFor(session.ID)
	if !ok {
		t.Fatalf("missing live session binding for %s", session.ID)
	}
	return session.ID, root, binding
}

// assertHardRuleAsksForVendor: the deploy tool on a target tagged prod asks
// in every approval mode and runs once allowed (VR-13, I-03).
func assertHardRuleAsksForVendor(t *testing.T, hp *HostProcess, tools *SessionToolService, binding SessionBinding, ran *int, mu *sync.Mutex) {
	t.Helper()
	inter := hp.Runtime().Interactions()
	sub, err := hp.Runtime().Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	snap, err := hp.vendorStore.Snapshot(t.Context(), binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	rev := snap.PolicyRevision
	for i, mode := range []string{vendorstore.ApprovalPlan, vendorstore.ApprovalAsk, vendorstore.ApprovalAuto, vendorstore.ApprovalFull} {
		if updErr := hp.vendorStore.UpdatePolicy(t.Context(), binding.SessionID, rev, mode); updErr != nil {
			t.Fatal(updErr)
		}
		rev++
		before := ranOf(ran, mu)
		call := callSessionTool(t, tools, binding, toolDeploy, `{"env":"prod"}`)
		prompt := awaitRequested(t, sub)
		if prompt.Tool != toolDeploy || prompt.EnvTag != "prod" {
			t.Fatalf("mode %s: prompt = %+v, want the deploy tool with the prod tag", mode, prompt)
		}
		if _, ansErr := inter.Answer(t.Context(), &AnswerInteractionCommand{
			CommandID: fmt.Sprintf("bridge-ans-%d", i), Principal: testAppPrincipal, Incarnation: inter.Incarnation(),
			RequestID: prompt.RequestID, Outcome: OutcomeAllowedOnce,
		}); ansErr != nil {
			t.Fatal(ansErr)
		}
		if res := call.wait(t); res.IsError || res.Text != deployedOKText {
			t.Fatalf("mode %s: an allowed call = %+v", mode, res)
		}
		if got := ranOf(ran, mu); got != before+1 {
			t.Fatalf("mode %s: runs = %d, want %d", mode, got, before+1)
		}
	}
}

// ranOf reads the run counter under its lock; a plain *int read would race
// with the tool's own goroutine.
func ranOf(ran *int, mu *sync.Mutex) int {
	mu.Lock()
	defer mu.Unlock()
	return *ran
}

func callSessionTool(t *testing.T, tools *SessionToolService, binding SessionBinding, name, args string) *toolCall {
	t.Helper()
	call := &toolCall{done: make(chan struct{})}
	supervise.Go(t.Context(), "test.bridge-tool-call", func(ctx context.Context) error {
		defer close(call.done)
		call.res, call.err = tools.Call(ctx, binding, name, []byte(args))
		return nil
	})
	return call
}

func TestFugueToolFromCodexSessionGoesThroughDecide(t *testing.T) {
	t.Parallel()
	var ran int
	var mu sync.Mutex
	hp, _, tools := startBridgedVendorHost(t, &ran, &mu)
	_, _, binding := startBridgedSession(t, hp.Runtime(), vendorServiceOf(t, hp), VendorCodex, "bridge-codex-start", VendorCodex)
	assertHardRuleAsksForVendor(t, hp, tools, binding, &ran, &mu)
}

func TestFugueToolFromAgySessionGoesThroughDecide(t *testing.T) {
	t.Parallel()
	var ran int
	var mu sync.Mutex
	hp, _, tools := startBridgedVendorHost(t, &ran, &mu)
	_, _, binding := startBridgedSession(t, hp.Runtime(), vendorServiceOf(t, hp), VendorAGY, "bridge-agy-start", VendorAGY)
	assertHardRuleAsksForVendor(t, hp, tools, binding, &ran, &mu)
}

func vendorServiceOf(t *testing.T, hp *HostProcess) *VendorService {
	t.Helper()
	svc, err := hp.Runtime().VendorService()
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestVendorApprovalsStayRelayedWithTheBridgeAttached: with the bridge
// attached, a vendor approval still reaches the user verbatim and is answered
// once, and the session's bridge serves no approve tool (VR-05, VR-13).
func TestVendorApprovalsStayRelayedWithTheBridgeAttached(t *testing.T) {
	t.Parallel()
	t.Run("codex", func(t *testing.T) {
		t.Parallel()
		var ran int
		var mu sync.Mutex
		hp, svc, tools := startBridgedVendorHost(t, &ran, &mu)
		sub := subscribeInteractionsOf(t, hp)
		sessionID, rootDir, binding := startBridgedSession(t, hp.Runtime(), vendorServiceOf(t, hp), VendorCodex, "relay-codex-start", VendorCodex)
		assertNoApproveTool(t, tools, binding, "codex")
		frame := fmt.Sprintf(`{"id":401,"method":"item/commandExecution/requestApproval","params":{"threadId":%q,"turnId":"t-1","itemId":"call-401","command":"ls"}}`, fakeCodexThreadID)
		sendServerRequestFor(t, svc, sessionID, "relay-codex-turn", frame)
		prompt := awaitRequested(t, sub)
		if prompt.Kind != string(interaction.KindConfirm) || !strings.Contains(prompt.Detail, "ls") {
			t.Fatalf("codex relay prompt = %+v, want the command verbatim", prompt)
		}
		answerInteraction(t, hp, prompt.RequestID, "relay-codex-ans", codex.DecisionAccept)
		resp := awaitVendorRPCResponse(t, rootDir, fakeCodexStdinFile, "401")
		if resp == nil || resp.Error != nil || string(resp.Result) != `{"decision":"accept"}` {
			t.Fatalf("codex answer reached the vendor = %+v", resp)
		}
	})
	t.Run("agy", func(t *testing.T) {
		t.Parallel()
		var ran int
		var mu sync.Mutex
		hp, svc, tools := startBridgedVendorHost(t, &ran, &mu)
		sub := subscribeInteractionsOf(t, hp)
		sessionID, rootDir, binding := startBridgedSession(t, hp.Runtime(), vendorServiceOf(t, hp), VendorAGY, "relay-agy-start", VendorAGY)
		assertNoApproveTool(t, tools, binding, "agy")
		frame := fmt.Sprintf(`{"jsonrpc":"2.0","id":"402","method":"session/request_permission","params":{"sessionId":%q,"toolCall":{"toolCallId":"call-402","name":"run_command","kind":"execute","title":"ls-402"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"},{"optionId":"deny","name":"Deny","kind":"reject_once"}]}}`, fakeCodexThreadID)
		sendServerRequestFor(t, svc, sessionID, "relay-agy-turn", frame)
		prompt := awaitRequested(t, sub)
		if prompt.Kind != string(interaction.KindConfirm) || !strings.Contains(prompt.Detail, "ls-402") {
			t.Fatalf("agy relay prompt = %+v, want the tool call verbatim", prompt)
		}
		answerInteraction(t, hp, prompt.RequestID, "relay-agy-ans", approveOptionAllow)
		resp := awaitVendorRPCResponse(t, rootDir, fakeAgyStdinFile, "402")
		if resp == nil || resp.Error != nil || string(resp.Result) != agyAllowDecisionBody {
			t.Fatalf("agy answer reached the vendor = %+v", resp)
		}
	})
}

func subscribeInteractionsOf(t *testing.T, hp *HostProcess) *Subscription {
	t.Helper()
	sub, err := hp.Runtime().Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return sub
}

func sendServerRequestFor(t *testing.T, svc *VendorService, sessionID, cmdID, frame string) {
	t.Helper()
	if err := svc.Send(t.Context(), testAppPrincipal, VendorInput{CommandID: cmdID, SessionID: sessionID, Text: "server-request:" + frame}); err != nil {
		t.Fatal(err)
	}
}

func answerInteraction(t *testing.T, hp *HostProcess, requestID, cmdID, optionID string) {
	t.Helper()
	inter := hp.Runtime().Interactions()
	if _, err := inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: cmdID, Principal: testAppPrincipal, Incarnation: inter.Incarnation(),
		RequestID: requestID, Outcome: OutcomeAnswered, OptionID: optionID,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertNoApproveTool(t *testing.T, tools *SessionToolService, binding SessionBinding, label string) {
	t.Helper()
	views, err := tools.List(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Name == approveToolName {
			t.Errorf("%s bridge lists the approve tool", label)
		}
	}
}

// vendorRPCResponse is one JSON-RPC response the fake vendor recorded.
type vendorRPCResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func awaitVendorRPCResponse(t *testing.T, root, stdinFile, id string) *vendorRPCResponse {
	t.Helper()
	var matched *vendorRPCResponse
	awaitActor(t, func() bool {
		resp := readVendorRPCResponses(t, root, stdinFile, id)
		if resp != nil {
			matched = resp
			return true
		}
		return false
	})
	if matched == nil {
		if raw, readErr := fs.ReadFile(os.DirFS(root), stdinFile); readErr == nil {
			t.Logf("recorded vendor stdin: %s", raw)
		}
	}
	return matched
}

// readVendorRPCResponses reads the fake vendor's recorded stdin and returns
// the response with the id.
func readVendorRPCResponses(t *testing.T, root, stdinFile, id string) *vendorRPCResponse {
	t.Helper()
	raw, err := fs.ReadFile(os.DirFS(root), stdinFile)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(raw), "\n") {
		var resp vendorRPCResponse
		if json.Unmarshal([]byte(line), &resp) != nil {
			continue
		}
		if strings.Trim(string(resp.ID), `"`) == id {
			return &resp
		}
	}
	return nil
}

// TestBridgeRefusesAnotherSessionsToken (F9.11 evidence): a token binds its
// bridge to exactly its own session and role (host-api §7, HA-06) — the
// approve relay stays Claude-only, each binding runs only its own session's
// tools, and a replaced or foreign token opens nothing.
type bridgeBindings struct {
	lead, worker, codex, agy SessionBinding
}

// attachBridgeBindings mints and attaches a token per fixture session.
func attachBridgeBindings(t *testing.T, f *toolFixture) bridgeBindings {
	t.Helper()
	lead, _ := f.attach(t, sessionLead, roleLead)
	worker, _ := f.attach(t, sessionWork, roleWorker)
	return bridgeBindings{
		lead:   lead,
		worker: worker,
		codex:  attachBridgeSession(t, f, sessionCodexBridge),
		agy:    attachBridgeSession(t, f, sessionAgyBridge),
	}
}

func attachBridgeSession(t *testing.T, f *toolFixture, sessionID string) SessionBinding {
	t.Helper()
	token, err := f.svc.tokens.mint(sessionID, roleWorker)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := f.svc.Attach(token.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

// TestBridgeRefusesAnotherSessionsToken (F9.11 evidence): a token binds its
// bridge to exactly its own session and role (host-api §7, HA-06) — the
// approve relay stays Claude-only, each binding runs only its own session's
// tools, and a replaced or foreign token opens nothing.
func TestBridgeRefusesAnotherSessionsToken(t *testing.T) {
	t.Parallel()
	f := newToolFixture(t)
	f.records.mu.Lock()
	f.records.sessions[sessionCodexBridge] = vendorstore.Session{ID: sessionCodexBridge, Vendor: VendorCodex, Role: roleWorker, State: vendorstore.StateStarting, ApprovalMode: vendorstore.ApprovalAuto, WorkspaceID: testWorkspaceID}
	f.records.sessions[sessionAgyBridge] = vendorstore.Session{ID: sessionAgyBridge, Vendor: VendorAGY, Role: roleWorker, State: vendorstore.StateStarting, ApprovalMode: vendorstore.ApprovalAuto, WorkspaceID: testWorkspaceID}
	f.records.mu.Unlock()
	b := attachBridgeBindings(t, f)

	// Each bridge sees only its own session's tools; the approve relay is
	// Claude's alone, because Codex and agy approvals arrive as server
	// requests (VR-05).
	assertBridgeToolLists(t, f, &b)
	// A tool call runs for the binding's own session, never another's.
	assertBridgeRunsOwnSession(t, f, &b)
	// A binding forged from another session's identity opens nothing, and a
	// replaced token ends the bridge connection that carried the old one,
	// exactly as for Claude.
	assertForgedAndReplacedTokensRefused(t, f, &b)
}

const (
	sessionCodexBridge = "session-codex-bridge"
	sessionAgyBridge   = "session-agy-bridge"
)

// assertBridgeToolLists checks what each bridge's tools/list shows.
func assertBridgeToolLists(t *testing.T, f *toolFixture, b *bridgeBindings) {
	t.Helper()
	assertNoApproveTool(t, f.svc, b.codex, "codex")
	assertNoApproveTool(t, f.svc, b.agy, "agy")
	if diff := cmp.Diff([]string{approveToolName, toolDeploy, toolEcho, toolLeadOnly}, toolNames(t, f.svc, b.lead)); diff != "" {
		t.Errorf("lead tools (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{toolDeploy, toolEcho}, toolNames(t, f.svc, b.codex)); diff != "" {
		t.Errorf("codex tools (-want +got):\n%s", diff)
	}
}

// assertBridgeRunsOwnSession: the deploy tool runs for the binding's own
// session only. The qa tag is set non-confirming first: an unknown tag would
// ask and block.
func assertBridgeRunsOwnSession(t *testing.T, f *toolFixture, b *bridgeBindings) {
	t.Helper()
	if err := f.tags.Set("qa", false); err != nil {
		t.Fatal(err)
	}
	if res := f.callDeploy(t.Context(), b.codex, `{"env":"qa"}`).wait(t); res.IsError {
		t.Fatalf("codex deploy = %+v", res)
	}
	if res := f.callDeploy(t.Context(), b.agy, `{"env":"qa"}`).wait(t); res.IsError {
		t.Fatalf("agy deploy = %+v", res)
	}
	for _, run := range f.ran() {
		if strings.Contains(run, sessionLead) || strings.Contains(run, sessionWork) {
			t.Errorf("another session's bridge ran a tool for %q", run)
		}
	}
	if _, err := f.svc.Call(t.Context(), b.codex, approveToolName, []byte(`{"tool_name":"Bash","input":{},"tool_use_id":"x"}`)); errcode.CodeOf(err) != errcode.NotFound {
		t.Errorf("approve over the codex bridge = %v, want not found", err)
	}
}

// assertForgedAndReplacedTokensRefused: a forged binding and a replaced
// token open nothing.
func assertForgedAndReplacedTokensRefused(t *testing.T, f *toolFixture, b *bridgeBindings) {
	t.Helper()
	forged := SessionBinding{SessionID: sessionCodexBridge, Role: roleWorker, hash: b.worker.hash}
	if _, err := f.svc.List(t.Context(), forged); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Errorf("a forged binding = %v, want permission_denied", err)
	}
	if _, err := f.svc.tokens.mint(sessionCodexBridge, roleWorker); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.List(t.Context(), b.codex); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Errorf("a replaced codex token = %v, want permission_denied", err)
	}
}

// vendorLink symlinks exe under a CLI's file name, the way the fake CLIs
// recognize which vendor they are playing.
func vendorLink(t *testing.T, exe, name string) string {
	t.Helper()
	link := filepath.Join(t.TempDir(), name)
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	return link
}

// TestCodexAndAgyFugueToolCallsNormalizeLikeClaude (F9.11 evidence): a Fugue
// tool call from every vendor normalizes into the same transcript tool_call
// item kind and status transitions as Claude's, with each vendor's own tool
// naming recorded (transcript §3–4).
func TestCodexAndAgyFugueToolCallsNormalizeLikeClaude(t *testing.T) {
	t.Parallel()
	for _, want := range []struct {
		vendor string
		shapes []toolCallShape
		names  []string
	}{
		{vendor: "claude", shapes: replayClaudeFugueToolCall(t), names: []string{"mcp__fugue__fugue_test_echo"}},
		{vendor: "codex", shapes: replayCodexFugueToolCall(t), names: []string{"fugue/fugue_test_echo"}},
		// agy advertises the server's tool name as the call's name.
		{vendor: "agy", shapes: replayAgyFugueToolCall(t), names: []string{"fugue_test_echo"}},
	} {
		if len(want.shapes) == 0 {
			t.Fatalf("%s: the fugue tool call produced no tool_call item", want.vendor)
		}
		names := make([]string, 0, len(want.shapes))
		for _, shape := range want.shapes {
			if shape.kind != string(transcript.ToolCall) {
				t.Errorf("%s: kind = %q, want tool_call", want.vendor, shape.kind)
			}
			if shape.status != toolSucceededStatus {
				t.Errorf("%s: status = %q, want succeeded", want.vendor, shape.status)
			}
			names = append(names, shape.name)
		}
		if !slices.Equal(names, want.names) {
			t.Errorf("%s: tool names = %v, want %v", want.vendor, names, want.names)
		}
	}
}

// toolSucceededStatus is the terminal status a completed tool call takes.
const toolSucceededStatus = "succeeded"

// toolCallShape is the vendor-independent projection of one tool_call item.
type toolCallShape struct{ kind, name, status string }

// collectToolCalls keeps the latest state of each tool_call item: a vendor
// may record the call and then update it through to its result.
func collectToolCalls(t *testing.T, records []transcript.Item, updates []vendor.Update) []toolCallShape {
	t.Helper()
	latest := map[string]toolCallShape{}
	order := []string{}
	for i := range records {
		if records[i].Kind != transcript.ToolCall {
			continue
		}
		meta, ok := records[i].Metadata.(transcript.ToolCallMetadata)
		if !ok {
			t.Fatalf("a tool_call item carries %T metadata", records[i].Metadata)
		}
		if _, seen := latest[records[i].ID]; !seen {
			order = append(order, records[i].ID)
		}
		latest[records[i].ID] = toolCallShape{string(records[i].Kind), meta.ToolName, meta.Status}
	}
	for i := range updates {
		if updates[i].After.Kind != transcript.ToolCall {
			continue
		}
		meta, ok := updates[i].After.Metadata.(transcript.ToolCallMetadata)
		if !ok {
			t.Fatalf("a tool_call update carries %T metadata", updates[i].After.Metadata)
		}
		if _, seen := latest[updates[i].After.ID]; !seen {
			order = append(order, updates[i].After.ID)
		}
		latest[updates[i].After.ID] = toolCallShape{string(updates[i].After.Kind), meta.ToolName, meta.Status}
	}
	out := make([]toolCallShape, 0, len(order))
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out
}

// replayClaudeFugueToolCall feeds the bridge tool_use and its result through
// the Claude normalizer.
func replayClaudeFugueToolCall(t *testing.T) []toolCallShape {
	t.Helper()
	n := claude.NewNormalizer("s")
	n.BeginTurn("t1")
	obs, err := n.Normalize([]byte(`{"type":"assistant","message":{"role":"assistant","id":"m1","model":"m","content":[{"type":"tool_use","id":"tu1","name":"mcp__fugue__fugue_test_echo","input":{"msg":"hi"}}]},"session_id":"s"}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := n.Normalize([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":[{"type":"text","text":"echo:hi"}]}]},"session_id":"s"}`))
	if err != nil {
		t.Fatal(err)
	}
	obs.Records = append(obs.Records, result.Records...)
	obs.Updates = append(obs.Updates, result.Updates...)
	return collectToolCalls(t, obs.Records, obs.Updates)
}

// replayCodexFugueToolCall feeds the fugue mcp_tool_call item through the
// Codex normalizer.
func replayCodexFugueToolCall(t *testing.T) []toolCallShape {
	t.Helper()
	n := codex.NewNormalizer("s")
	n.BeginTurn("t1")
	obs := vendor.Observation{}
	for _, line := range [][]byte{
		[]byte(`{"method":"item/started","params":{"threadId":"th","turnId":"t1","item":{"type":"mcpToolCall","id":"item-1","server":"fugue","tool":"fugue_test_echo","status":"inProgress","arguments":{"msg":"hi"}}}}`),
		[]byte(`{"method":"item/completed","params":{"threadId":"th","turnId":"t1","item":{"type":"mcpToolCall","id":"item-1","server":"fugue","tool":"fugue_test_echo","status":"completed","result":{"content":[{"type":"text","text":"echo:hi"}]}}}}`),
	} {
		o, err := n.Normalize(line)
		if err != nil {
			t.Fatal(err)
		}
		obs.Records = append(obs.Records, o.Records...)
		obs.Updates = append(obs.Updates, o.Updates...)
	}
	return collectToolCalls(t, obs.Records, obs.Updates)
}

// replayAgyFugueToolCall feeds the fugue tool_call update through the agy
// normalizer.
func replayAgyFugueToolCall(t *testing.T) []toolCallShape {
	t.Helper()
	n := agy.NewNormalizer("s")
	n.BeginTurn("t1", "")
	obs := vendor.Observation{}
	for _, line := range [][]byte{
		[]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"acs","update":{"sessionUpdate":"tool_call","toolCallId":"call-1","name":"fugue_test_echo","kind":"read","status":"pending","title":"fugue_test_echo","rawInput":{"msg":"hi"}}}}`),
		[]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"acs","update":{"sessionUpdate":"tool_call_update","toolCallId":"call-1","status":"completed","rawOutput":{"output":"echo:hi"}}}}`),
	} {
		o, err := n.Normalize(line)
		if err != nil {
			t.Fatal(err)
		}
		obs.Records = append(obs.Records, o.Records...)
		obs.Updates = append(obs.Updates, o.Updates...)
	}
	return collectToolCalls(t, obs.Records, obs.Updates)
}
