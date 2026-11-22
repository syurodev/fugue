package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/agy"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	agyOptionAllowAlways = "allow_always"
	agyOptionDenyAlways  = "deny_always"
	agyLabelAllow        = "Allow"
	agyLabelAllowAlways  = "Always Allow"
	agyLabelDeny         = "Deny"
	agyLabelDenyAlways   = "Never Allow"
)

type agyRelayFixture struct {
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

func newAgyRelayFixture(t *testing.T) *agyRelayFixture {
	t.Helper()
	exe := mustExe(t)
	agyLink := filepath.Join(t.TempDir(), "agy")
	if err := os.Symlink(exe, agyLink); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(200, 0))
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir:      cheapVaultDataDir(t),
		WorkspaceEnv: config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config:       testHostConfig(),
		Clock:        clk,
		IDs:          ids.NewUUIDv7(clk, ids.NewDeterministic("agy-relay")),
		Transport:    newScriptedTransport(),
		VendorBinary: exe,
		AgyBinary:    agyLink,
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
	rootDir := t.TempDir()
	ws, err := rt.CreateWorkspace(t.Context(), testAppPrincipal, "agy-ws")
	if err != nil {
		t.Fatal(err)
	}
	cfg := []byte("name = \"agy-ws\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(rootDir) + "\"\naccess = \"rw\"\n")
	if _, cfgErr := rt.UpdateWorkspaceConfig(t.Context(), testAppPrincipal, ws.ID, cfg, ws.ConfigHash); cfgErr != nil {
		t.Fatal(cfgErr)
	}
	return startAgySessionInWorkspace(t, hp, clk, ws.ID, rootDir, "agy-start")
}

func startAgySessionInWorkspace(t *testing.T, hp *HostProcess, clk *clock.Fake, workspaceID, rootDir, cmdID string) *agyRelayFixture {
	t.Helper()
	rt := hp.Runtime()
	svc, sErr := rt.VendorService()
	tools, tErr := rt.SessionTools()
	sub, subErr := rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err := errors.Join(sErr, tErr, subErr); err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	session, err := svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{
		CommandID: cmdID, Vendor: VendorAGY, WorkspaceID: workspaceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitSessionState(t, sub, session.ID, "ready")
	binding, ok := tools.tokens.bindingFor(session.ID)
	if !ok {
		t.Fatalf("missing live agy session binding for %s", session.ID)
	}
	return &agyRelayFixture{
		hp: hp, rt: rt, svc: svc, tools: tools, inter: rt.Interactions(),
		clk: clk, workspaceID: workspaceID, rootDir: rootDir, session: session, binding: binding,
	}
}

func (f *agyRelayFixture) subscribeInteractions(t *testing.T) *Subscription {
	t.Helper()
	sub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return sub
}

func (f *agyRelayFixture) sendServerRequestTurn(t *testing.T, cmdID string, rawLines ...string) {
	t.Helper()
	text := "server-request:" + strings.Join(rawLines, "\n")
	if err := f.svc.Send(t.Context(), testAppPrincipal, VendorInput{CommandID: cmdID, SessionID: f.session.ID, Text: text}); err != nil {
		t.Fatal(err)
	}
}

func (f *agyRelayFixture) answerOption(t *testing.T, cmdID, requestID, optionID string) {
	t.Helper()
	if _, err := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: cmdID, Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: requestID, Outcome: OutcomeAnswered, OptionID: optionID,
	}); err != nil {
		t.Fatal(err)
	}
}

type agyRecordedResponse struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *agy.RPCError   `json:"error"`
}

func (f *agyRelayFixture) awaitRPCResponse(t *testing.T, id string) agyRecordedResponse {
	t.Helper()
	var matched agyRecordedResponse
	awaitActor(t, func() bool {
		for _, resp := range f.readRPCResponses(t) {
			if agy.CanonicalID(resp.ID) == id {
				matched = resp
				return true
			}
		}
		return false
	})
	return matched
}

func (f *agyRelayFixture) readRPCResponses(t *testing.T) []agyRecordedResponse {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(f.rootDir, fakeAgyStdinFile)))
	if err != nil {
		return nil
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	out := make([]agyRecordedResponse, 0, len(lines))
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		var msg agyRecordedResponse
		if len(trimmed) > 0 && json.Unmarshal(trimmed, &msg) == nil && msg.Method == "" && len(msg.ID) > 0 {
			out = append(out, msg)
		}
	}
	return out
}

func readRawAgyResponseLine(t *testing.T, dir, id string) []byte {
	t.Helper()
	var found []byte
	awaitActor(t, func() bool {
		raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, fakeAgyStdinFile)))
		if err != nil {
			return false
		}
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}
			var msg agyRecordedResponse
			if json.Unmarshal(trimmed, &msg) == nil && msg.Method == "" && agy.CanonicalID(msg.ID) == id {
				found = trimmed
				return true
			}
		}
		return false
	})
	return found
}

func (f *agyRelayFixture) recordedDecisions(t *testing.T, forbidden ...string) []string {
	t.Helper()
	events, err := f.hp.vendorStore.EventsSince(t.Context(), f.session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var decisions []string
	for i := range events {
		ev := &events[i]
		assertMetadataContainsNoForbiddenPlaintext(t, &ev.Metadata, forbidden)
		if ev.Type != vendorstore.ApprovalDecided {
			continue
		}
		meta := ev.Metadata.ItemMetadata
		if meta == nil || meta.Decider != adapterOriginUser {
			t.Fatalf("agy ApprovalDecided item metadata = %+v, want user decider", meta)
		}
		decisions = append(decisions, meta.Decision)
	}
	return decisions
}

func assertMetadataContainsNoForbiddenPlaintext(t *testing.T, meta *vendorstore.EventMetadata, forbidden []string) {
	t.Helper()
	rawMeta, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range forbidden {
		if needle != "" && bytes.Contains(rawMeta, []byte(needle)) {
			t.Fatalf("event metadata leaked plaintext %q: %s", needle, rawMeta)
		}
	}
}

func TestAgyServiceProbeStartSendCancelAndClose(t *testing.T) {
	t.Parallel()
	f := newAgyRelayFixture(t)
	probes, err := f.svc.Probe(t.Context(), testAppPrincipal, []string{VendorAGY})
	if err != nil || len(probes) != 1 || probes[0].Vendor != VendorAGY || !probes[0].VersionTested || probes[0].Version != fakeAgyVersion {
		t.Fatalf("probes = %+v, %v; want tested agy %s", probes, err, fakeAgyVersion)
	}
	if !probes[0].Capability.PerRequestApproval || !probes[0].Capability.VendorSlashCommands {
		t.Fatalf("probe capability = %+v, want PerRequestApproval and VendorSlashCommands", probes[0].Capability)
	}

	sub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	verifyAgyTurnAndCancel(t, f.svc, sub, f.session.ID)

	if _, closeErr := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{
		CommandID: "agy-close-1", SessionID: f.session.ID,
	}); closeErr != nil {
		t.Fatal(closeErr)
	}
	awaitSessionState(t, sub, f.session.ID, testStateClosed)
}

func verifyAgyTurnAndCancel(t *testing.T, svc *VendorService, sub *Subscription, sessionID string) {
	t.Helper()
	if sendErr := svc.Send(t.Context(), testAppPrincipal, VendorInput{
		CommandID: "agy-send-1", SessionID: sessionID, Text: "ping",
	}); sendErr != nil {
		t.Fatal(sendErr)
	}
	awaitSessionState(t, sub, sessionID, "running")
	awaitSessionState(t, sub, sessionID, "ready")

	if sendErr := svc.Send(t.Context(), testAppPrincipal, VendorInput{
		CommandID: "agy-send-2", SessionID: sessionID, Text: fakeCLIHold,
	}); sendErr != nil {
		t.Fatal(sendErr)
	}
	awaitSessionState(t, sub, sessionID, "running")
	if _, intErr := svc.Interrupt(t.Context(), testAppPrincipal, VendorStop{
		CommandID: "agy-int-1", SessionID: sessionID,
	}); intErr != nil {
		t.Fatal(intErr)
	}
	awaitSessionState(t, sub, sessionID, "ready")
}

// TestAgyShellCommandGoesThroughRequestPermissionAndRunsWhenAllowed verifies VR-E07:
// a shell command emits session/request_permission, is relayed to the user inbox,
// and executes only after the user selects an allow option.
func TestAgyShellCommandGoesThroughRequestPermissionAndRunsWhenAllowed(t *testing.T) {
	t.Parallel()
	f := newAgyRelayFixture(t)
	interSub := f.subscribeInteractions(t)
	sessSub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sessSub.Close()

	markerPath := filepath.Join(f.rootDir, "shell-ran.txt")
	if err := f.svc.Send(t.Context(), testAppPrincipal, VendorInput{
		CommandID: "agy-shell-turn",
		SessionID: f.session.ID,
		Text:      "run-shell:" + markerPath,
	}); err != nil {
		t.Fatal(err)
	}

	prompt := awaitRequested(t, interSub)
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("shell command ran before user answered permission: %v", statErr)
	}

	f.answerOption(t, "agy-shell-allow", prompt.RequestID, approveOptionAllow)
	_, _ = interSub.Next(t.Context())

	resp := f.awaitRPCResponse(t, "perm-shell-1")
	if resp.Error != nil || string(resp.Result) != agyAllowDecisionBody {
		t.Fatalf("permission RPC response = %+v, want selected allow", resp)
	}
	awaitSessionState(t, sessSub, f.session.ID, "ready")

	gotBytes, readErr := os.ReadFile(filepath.Clean(markerPath))
	if readErr != nil || string(gotBytes) != "ran-after-permission\n" {
		t.Fatalf("shell marker = (%q, %v), want ran-after-permission", gotBytes, readErr)
	}
}

func TestAgyPermissionRelaysVerbatimWithItsOptionsAndIsAnsweredOnce(t *testing.T) {
	t.Parallel()
	f := newAgyRelayFixture(t)
	sub := f.subscribeInteractions(t)

	const cmdTail = "agy-command-tail-must-stay-in-inbox-only"
	padding := strings.Repeat("z", 4*1024)
	toolCallJSON := fmt.Sprintf(
		`{"sessionUpdate":"tool_call_update","toolCallId":"call-101","name":"run_command","kind":"execute","status":"pending","title":%q,"rawInput":{"CommandLine":%q},"locations":[{"path":"/workspace/script.sh","line":7}],"content":[{"type":"content","content":{"type":"text","text":"preview"}}]}`,
		"printf "+padding, cmdTail,
	)
	var compactToolCall bytes.Buffer
	if err := json.Compact(&compactToolCall, []byte(toolCallJSON)); err != nil {
		t.Fatal(err)
	}
	permFrame := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":"101","method":%q,"params":{"sessionId":%q,"toolCall":%s,"options":[{"optionId":%q,"name":%q,"kind":"allow_once"},{"optionId":%q,"name":%q,"kind":"allow_always"},{"optionId":%q,"name":%q,"kind":"reject_once"},{"optionId":%q,"name":%q,"kind":"reject_always"}]}}`,
		agy.MethodSessionRequestPermission, fakeCodexThreadID, toolCallJSON,
		approveOptionAllow, agyLabelAllow, agyOptionAllowAlways, agyLabelAllowAlways,
		approveOptionDeny, agyLabelDeny, agyOptionDenyAlways, agyLabelDenyAlways,
	)

	f.sendServerRequestTurn(t, "agy-verbatim-turn", permFrame)
	prompt := awaitRequested(t, sub)
	assertAgyRelayedPermissionPrompt(t, &prompt, compactToolCall.String())
	assertAgyPermissionAnsweredOnce(t, f, sub, &prompt, cmdTail, padding[:64])
}

func assertAgyRelayedPermissionPrompt(t *testing.T, prompt *PendingInteractionView, wantDetail string) {
	t.Helper()
	if len(prompt.Summary) > interaction.MaxSummaryBytes || !strings.Contains(prompt.Summary, "truncated") {
		t.Fatalf("Summary len=%d (%q), want <= %d with truncation marker", len(prompt.Summary), prompt.Summary, interaction.MaxSummaryBytes)
	}
	if prompt.Detail != wantDetail {
		t.Fatalf("Detail mismatch:\ngot:  %s\nwant: %s", prompt.Detail, wantDetail)
	}
	if prompt.GrantScopeLabel != "" || prompt.Risk != "" || prompt.EnvTag != "" {
		t.Fatalf("relayed prompt carried Fugue policy metadata: %+v", prompt)
	}
	wantOpts := []InteractionOption{
		{ID: approveOptionAllow, Label: agyLabelAllow, Description: agy.PermissionOptionKindAllowOnce},
		{ID: agyOptionAllowAlways, Label: agyLabelAllowAlways, Description: agy.PermissionOptionKindAllowAlways},
		{ID: approveOptionDeny, Label: agyLabelDeny, Description: agy.PermissionOptionKindRejectOnce},
		{ID: agyOptionDenyAlways, Label: agyLabelDenyAlways, Description: agy.PermissionOptionKindRejectAlways},
	}
	if diff := cmp.Diff(wantOpts, prompt.Options); diff != "" {
		t.Fatalf("relayed options (-want +got):\n%s", diff)
	}
}

func assertAgyPermissionAnsweredOnce(
	t *testing.T, f *agyRelayFixture, sub *Subscription,
	prompt *PendingInteractionView, cmdTail, padPrefix string,
) {
	t.Helper()
	f.answerOption(t, "agy-ans-101", prompt.RequestID, agyOptionAllowAlways)
	resolvedEv, ok := sub.Next(t.Context())
	if !ok || resolvedEv.Type != EventInteractionResolved || strings.Contains(string(resolvedEv.Payload), cmdTail) {
		t.Fatalf("resolved event = (%+v, %v)", resolvedEv, ok)
	}
	resp := f.awaitRPCResponse(t, "101")
	if resp.Error != nil || string(resp.Result) != `{"outcome":{"outcome":"selected","optionId":"allow_always"}}` {
		t.Fatalf("RPC 101 response = %+v, want selected allow_always", resp)
	}
	_, dupErr := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "agy-ans-101-dup", Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: prompt.RequestID, Outcome: OutcomeAnswered, OptionID: approveOptionAllow,
	})
	requireCode(t, dupErr, errcode.InteractionNotFound)
	if diff := cmp.Diff([]string{agyOptionAllowAlways}, f.recordedDecisions(t, cmdTail, padPrefix)); diff != "" {
		t.Fatalf("recorded decisions (-want +got):\n%s", diff)
	}
}

func TestAgyPermissionOverTheDetailBoundIsAnErrorNotADecision(t *testing.T) {
	t.Parallel()
	f := newAgyRelayFixture(t)
	sub := f.subscribeInteractions(t)

	f.sendServerRequestTurn(t, "agy-oversized", "oversized")
	assertAgyErrorResponse(t, f.awaitRPCResponse(t, "201"), "agy detail over 256 KiB")
	assertNoPendingOrRecordedAgyDecisions(t, f, sub)
}

func assertNoPendingOrRecordedAgyDecisions(t *testing.T, f *agyRelayFixture, sub *Subscription) {
	t.Helper()
	if pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal); pErr != nil || len(pending) != 0 {
		t.Fatalf("ListPending = (%+v, %v), want 0", pending, pErr)
	}
	if ev, hasEv := sub.popEvent(); hasEv {
		t.Fatalf("unexpected TopicInteraction event: %+v", ev)
	}
	if got := len(f.recordedDecisions(t)); got != 0 {
		t.Fatalf("ApprovalDecided count = %d, want 0", got)
	}
}

func assertAgyErrorResponse(t *testing.T, resp agyRecordedResponse, label string) {
	t.Helper()
	if resp.Error == nil || resp.Error.Code != agy.RPCErrorServerError || len(resp.Result) != 0 {
		t.Fatalf("%s: expected JSON-RPC -32000 error response, got %+v", label, resp)
	}
	for _, forbidden := range []string{`"selected"`, `"cancelled"`, approveOptionAllow, approveOptionDeny} {
		if strings.Contains(resp.Error.Message, forbidden) {
			t.Fatalf("%s: error message %q must not synthesize an option (%q)", label, resp.Error.Message, forbidden)
		}
	}
}

func TestAgyTurnCancelAnswersCancelledAndClosesItsTickets(t *testing.T) {
	t.Parallel()
	f := newAgyRelayFixture(t)
	interSub := f.subscribeInteractions(t)
	sessSub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sessSub.Close()

	frame1 := makeTestAgyPermissionFrame("cancel-1", "call-c1", "sleep 10")
	frame2 := makeTestAgyPermissionFrame("cancel-2", "call-c2", "sleep 20")
	f.sendServerRequestTurn(t, "agy-cancel-turn", frame1, frame2)

	p1 := awaitRequested(t, interSub)
	p2 := awaitRequested(t, interSub)

	if _, intErr := f.svc.Interrupt(t.Context(), testAppPrincipal, VendorStop{
		CommandID: "agy-cancel-int", SessionID: f.session.ID,
	}); intErr != nil {
		t.Fatal(intErr)
	}

	for _, id := range []string{"cancel-1", "cancel-2"} {
		resp := f.awaitRPCResponse(t, id)
		if resp.Error != nil || string(resp.Result) != `{"outcome":{"outcome":"cancelled"}}` {
			t.Fatalf("RPC %s response = %+v, want {\"outcome\":{\"outcome\":\"cancelled\"}}", id, resp)
		}
	}
	awaitSessionState(t, sessSub, f.session.ID, "ready")

	pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal)
	if pErr != nil || len(pending) != 0 {
		t.Fatalf("ListPending after turn cancel = (%+v, %v), want 0 stale tickets", pending, pErr)
	}
	for _, reqID := range []string{p1.RequestID, p2.RequestID} {
		_, ansErr := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
			CommandID: "stale-" + reqID, Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
			RequestID: reqID, Outcome: OutcomeAnswered, OptionID: approveOptionAllow,
		})
		if errcode.CodeOf(ansErr) != errcode.InteractionNotFound && errcode.CodeOf(ansErr) != errcode.InteractionAlreadyAnswered {
			t.Fatalf("answering cancelled ticket %s err = %v, want not_found or already_answered", reqID, ansErr)
		}
	}
	if got := len(f.recordedDecisions(t)); got != 0 {
		t.Fatalf("turn cancel recorded %d ApprovalDecided events, want 0", got)
	}
}

// TestAgyPermissionAfterTurnCancelIsAnsweredCancelled covers the cancel race:
// a request of the cancelled turn that reaches Fugue only after the user
// interrupt is answered cancelled at once and never becomes a pending ticket.
func TestAgyPermissionAfterTurnCancelIsAnsweredCancelled(t *testing.T) {
	t.Parallel()
	f := newAgyRelayFixture(t)
	interSub := f.subscribeInteractions(t)
	sessSub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sessSub.Close()
	late := makeTestAgyPermissionFrame("late-1", "call-late", "sleep 30")
	if err := f.svc.Send(t.Context(), testAppPrincipal, VendorInput{
		CommandID: "agy-late-turn", SessionID: f.session.ID, Text: "perm-after-cancel:" + late,
	}); err != nil {
		t.Fatal(err)
	}
	awaitSessionState(t, sessSub, f.session.ID, "running")
	if _, err := f.svc.Interrupt(t.Context(), testAppPrincipal, VendorStop{CommandID: "agy-late-int", SessionID: f.session.ID}); err != nil {
		t.Fatal(err)
	}
	resp := f.awaitRPCResponse(t, "late-1")
	if resp.Error != nil || string(resp.Result) != `{"outcome":{"outcome":"cancelled"}}` {
		t.Fatalf("late permission response = %+v, want {\"outcome\":{\"outcome\":\"cancelled\"}}", resp)
	}
	awaitSessionState(t, sessSub, f.session.ID, "ready")
	assertNoPendingOrRecordedAgyDecisions(t, f, interSub)
}

func makeTestAgyPermissionFrame(id, callID, title string) string {
	return fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%q,"method":%q,"params":{"sessionId":%q,"toolCall":{"toolCallId":%q,"name":"run_command","kind":"execute","title":%q},"options":[{"optionId":%q,"name":%q,"kind":"allow_once"},{"optionId":%q,"name":%q,"kind":"reject_once"}]}}`,
		id, agy.MethodSessionRequestPermission, fakeCodexThreadID, callID, title,
		approveOptionAllow, agyLabelAllow, approveOptionDeny, agyLabelDeny,
	)
}

func TestAgyRetriedPermissionReplaysTheSameResponse(t *testing.T) {
	t.Parallel()
	f := newAgyRelayFixture(t)
	sub := f.subscribeInteractions(t)

	allowFrame := makeTestAgyPermissionFrame("301", "call-301", "echo test")
	rejectFrame := makeTestAgyPermissionFrame("302", "call-302", "echo test")
	cancelFrame := makeTestAgyPermissionFrame("303", "call-303", "echo test")

	// Emit 301 twice in the same turn (in-flight duplicate) plus 302.
	f.sendServerRequestTurn(t, "agy-retry-turn", allowFrame, allowFrame, rejectFrame)
	answerFirstAgyPair(t, f, sub)

	turnID, err := f.tools.recorder.ActiveTurnID(t.Context(), f.binding.SessionID)
	if err != nil || turnID == "" {
		t.Fatalf("ActiveTurnID = (%q, %v)", turnID, err)
	}
	firstRaw := map[string][]byte{
		"301": readRawAgyResponseLine(t, f.rootDir, "301"),
		"302": readRawAgyResponseLine(t, f.rootDir, "302"),
		"303": seedAndResolveCancelledAgyPermission(t, f, turnID, "303", cancelFrame),
	}

	allLines := strings.Join([]string{allowFrame, rejectFrame, cancelFrame}, "\n")
	replaySess, replayDir, replayReqs := startHelperAgyWithRequests(t, "replay", allLines, 3)
	fallbackSess, fallbackDir, fallbackReqs := startHelperAgyWithRequests(t, "fallback", allLines, 3)
	for _, id := range []string{"301", "302", "303"} {
		assertAgyPermissionReplayMatches(t, f, turnID, id, firstRaw[id], replaySess, replayDir, replayReqs[id], fallbackSess, fallbackDir, fallbackReqs[id])
	}
	if pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal); pErr != nil || len(pending) != 0 {
		t.Fatalf("pending after replay = (%+v, %v), want 0", pending, pErr)
	}
}

func answerFirstAgyPair(t *testing.T, f *agyRelayFixture, sub *Subscription) {
	t.Helper()
	pAllow := awaitRequested(t, sub)
	pReject := awaitRequested(t, sub)
	if strings.Contains(pAllow.Detail, "call-302") {
		pAllow, pReject = pReject, pAllow
	}
	f.answerOption(t, "agy-retry-ans-301", pAllow.RequestID, approveOptionAllow)
	f.answerOption(t, "agy-retry-ans-302", pReject.RequestID, approveOptionDeny)
	for range 2 {
		ev, ok := sub.Next(t.Context())
		if !ok || ev.Type != EventInteractionResolved {
			t.Fatalf("expected resolved event, got (%+v, %v)", ev, ok)
		}
	}
	resp301 := f.awaitRPCResponse(t, "301")
	if resp301.Error != nil || string(resp301.Result) != agyAllowDecisionBody {
		t.Fatalf("RPC 301 = %+v, want selected allow", resp301)
	}
	resp302 := f.awaitRPCResponse(t, "302")
	if resp302.Error != nil || string(resp302.Result) != `{"outcome":{"outcome":"selected","optionId":"deny"}}` {
		t.Fatalf("RPC 302 = %+v, want selected deny", resp302)
	}
}

func seedAndResolveCancelledAgyPermission(t *testing.T, f *agyRelayFixture, turnID, id, line string) []byte {
	t.Helper()
	sess, dir, reqs := startHelperAgyWithRequests(t, "cancel-first", line, 1)
	req := reqs[id]
	opID, err := interaction.VendorOperationKey(f.binding.SessionID, turnID, req.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	f.tools.tombstones.putAgy(f.binding.SessionID, opID, approveArgsDigest(req.Method, req.Detail), "", false, true)
	if rErr := sess.Resolve(t.Context(), vendor.ApprovalRef{ID: req.RequestID}, vendor.Decision{Cancelled: true}); rErr != nil {
		t.Fatalf("first cancelled Resolve: %v", rErr)
	}
	raw := readRawAgyResponseLine(t, dir, id)
	if !bytes.Contains(raw, []byte(`"result":{"outcome":{"outcome":"cancelled"}}`)) {
		t.Fatalf("unexpected first cancelled JSON-RPC bytes: %s", raw)
	}
	return raw
}

func assertAgyPermissionReplayMatches(
	t *testing.T, f *agyRelayFixture, turnID, id string, firstRaw []byte,
	replaySess *agy.Session, replayDir string, req *vendor.ServerRequest,
	fallbackSess *agy.Session, fallbackDir string, fbReq *vendor.ServerRequest,
) {
	t.Helper()
	opID, kErr := interaction.VendorOperationKey(f.binding.SessionID, turnID, req.RequestID)
	if kErr != nil {
		t.Fatal(kErr)
	}
	entry, ok := f.tools.tombstones.get(f.binding.SessionID, opID)
	if !ok || entry.note != "" || entry.answers != nil {
		t.Fatalf("tombstone entry for %s = %+v, want outcome-only without labels or notes", id, entry)
	}
	f.tools.RelayServerRequest(t.Context(), f.session.ID, req, replaySess)
	if replayRaw := readRawAgyResponseLine(t, replayDir, id); !bytes.Equal(replayRaw, firstRaw) {
		t.Fatalf("id %s replay JSON-RPC mismatch:\nfirst:  %s\nreplay: %s", id, firstRaw, replayRaw)
	}
	choice, fbOK := f.tools.fallbackCodexRelayedTombstone(
		errcode.New(errcode.InteractionNotFound, "ticket already taken"),
		f.binding, opID, approveArgsDigest(fbReq.Method, fbReq.Detail), fbReq.Kind,
	)
	if !fbOK {
		t.Fatalf("id %s fallbackCodexRelayedTombstone returned false", id)
	}
	if rErr := fallbackSess.Resolve(t.Context(), vendor.ApprovalRef{ID: fbReq.RequestID}, choice.decision); rErr != nil {
		t.Fatalf("id %s fallback Resolve: %v", id, rErr)
	}
	if fallbackRaw := readRawAgyResponseLine(t, fallbackDir, id); !bytes.Equal(fallbackRaw, firstRaw) {
		t.Fatalf("id %s fallback JSON-RPC mismatch:\nfirst:    %s\nfallback: %s", id, firstRaw, fallbackRaw)
	}
}

func startHelperAgyWithRequests(t *testing.T, tag, joinedLines string, want int) (*agy.Session, string, map[string]*vendor.ServerRequest) {
	t.Helper()
	agyLink := filepath.Join(t.TempDir(), "agy")
	if err := os.Symlink(mustExe(t), agyLink); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	rawSess, err := agy.NewAdapter(nil, agy.WithBinary(agyLink)).Start(t.Context(), &vendor.StartSpec{
		Cwd: cwd, SessionID: "helper-agy-" + tag,
	})
	if err != nil {
		t.Fatalf("Start helper agy %s: %v", tag, err)
	}
	t.Cleanup(func() {
		if _, cErr := rawSess.Close(context.Background(), vendor.CloseUserRequest); cErr != nil {
			t.Errorf("close helper agy %s: %v", tag, cErr)
		}
	})
	agySess, ok := rawSess.(*agy.Session)
	if !ok {
		t.Fatalf("expected *agy.Session, got %T", rawSess)
	}
	if _, sErr := agySess.Send(t.Context(), vendor.Input{Text: "server-request:" + joinedLines}); sErr != nil {
		t.Fatalf("helper agy Send(%s): %v", tag, sErr)
	}
	return agySess, cwd, drainHelperAgyRequests(t, agySess, tag, want)
}

func drainHelperAgyRequests(t *testing.T, sess *agy.Session, tag string, want int) map[string]*vendor.ServerRequest {
	t.Helper()
	reqs := make(map[string]*vendor.ServerRequest, want)
	for len(reqs) < want {
		obs, ok := <-sess.Observations()
		if !ok {
			t.Fatalf("helper agy %s observations closed early after %d requests", tag, len(reqs))
		}
		if obs.ServerRequest != nil {
			reqCopy := *obs.ServerRequest
			reqs[reqCopy.RequestID] = &reqCopy
		}
	}
	return reqs
}

func TestAgySlashListHidesTerminalCommands(t *testing.T) {
	t.Parallel()
	f := newAgyRelayFixture(t)

	want := []string{
		"goal",
		"schedule",
		"browser",
		"plan",
		"grill-me",
		"teamwork",
		"learn",
		"boost",
		"agy-customizations",
		"antigravity-guide",
		"run-workflow",
		"swarm",
	}
	var got []string
	var rev uint64
	awaitActor(t, func() bool {
		groups, r, err := f.svc.Commands(t.Context(), testAppPrincipal, f.session.ID)
		if err != nil || len(groups) == 0 || len(groups[0].VendorCommands) == 0 {
			return false
		}
		got = groups[0].VendorCommands
		rev = r
		return true
	})
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Commands() (-want +got):\n%s", diff)
	}
	if slices.Contains(got, "compact") {
		t.Fatalf("terminal-only command 'compact' must be hidden from Commands(): %v", got)
	}
	if rev <= 0 {
		t.Fatalf("command revision = %d, want > 0", rev)
	}
}

func TestFugueNamespaceNeverReachesAgy(t *testing.T) {
	t.Parallel()
	f := newAgyRelayFixture(t)

	for i, cmd := range []string{"/fugue:status", " /fugue:help arg", "/fugue:compact"} {
		err := f.svc.Send(t.Context(), testAppPrincipal, VendorInput{
			CommandID: fmt.Sprintf("fugue-ns-%d", i),
			SessionID: f.session.ID,
			Text:      cmd,
		})
		requireCode(t, err, errcode.VendorUnknownCommand)
	}
	for i, cmd := range []string{"/fugue:team", "/fugue:solo", "/fugue:account"} {
		err := f.svc.Send(t.Context(), testAppPrincipal, VendorInput{
			CommandID: fmt.Sprintf("fugue-reg-%d", i),
			SessionID: f.session.ID,
			Text:      cmd,
		})
		requireCode(t, err, errcode.VendorCommandUnavailable)
	}
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(f.rootDir, fakeAgyStdinFile)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("fugue:")) || bytes.Contains(raw, []byte(agy.MethodSessionPrompt)) {
		t.Fatalf("/fugue: command leaked to agy process stdin:\n%s", raw)
	}
}

// TestAgyDecisionAllowFollowsTheOptionKind checks that Decision.Allow follows
// the kind of the chosen agy option and that the tombstone replay keeps it.
func TestAgyDecisionAllowFollowsTheOptionKind(t *testing.T) {
	t.Parallel()
	req := &vendor.ServerRequest{
		RequestID: "9", Method: agy.MethodSessionRequestPermission, Kind: vendor.ServerRequestConfirm,
		OptionDetails: []vendor.ServerRequestOption{
			{ID: "a1", Name: agyLabelAllow, Kind: agy.PermissionOptionKindAllowOnce},
			{ID: "a2", Name: agyLabelAllowAlways, Kind: agy.PermissionOptionKindAllowAlways},
			{ID: "r1", Name: agyLabelDeny, Kind: agy.PermissionOptionKindRejectOnce},
			{ID: "r2", Name: agyLabelDenyAlways, Kind: agy.PermissionOptionKindRejectAlways},
		},
	}
	tombs := newApproveTombstones()
	for option, wantAllow := range map[string]bool{"a1": true, "a2": true, "r1": false, "r2": false} {
		choice, ok := translateAgyRelayedOutcome(&interaction.Ticket{Outcome: interaction.OutcomeAnswered, SelectedOptionID: option}, req)
		if !ok || choice.decision.Allow != wantAllow || choice.decision.Message != option {
			t.Fatalf("option %s: choice = %+v (ok=%v), want Allow=%v", option, choice, ok, wantAllow)
		}
		svc := &SessionToolService{tombstones: tombs}
		svc.recordRelayTombstone("s1", "op-"+option, [32]byte{}, req, &choice)
		entry, found := tombs.get("s1", "op-"+option)
		if replay := codexDecisionFromTombstone(&entry); !found || replay.Allow != wantAllow || replay.Message != option || replay.Cancelled {
			t.Fatalf("option %s: tombstone replay = %+v (found=%v), want Allow=%v", option, replay, found, wantAllow)
		}
	}
}
