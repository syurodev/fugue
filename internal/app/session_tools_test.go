package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/environments"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	testWorkspaceID = "ws-1"
	toolEcho        = "fugue_test_echo"
	toolDeploy      = "fugue_test_deploy"
	toolLeadOnly    = "fugue_test_lead_only"
	roleLead        = "lead"
	roleWorker      = "worker"
	sessionLead     = "session-lead"
	sessionWork     = "session-worker"
	tokenSeed       = "session-token-seed"
)

// fakeSessionRecords is the vendor store as the gateway reads it: the
// approval mode and state of each session.
type fakeSessionRecords struct {
	mu       sync.Mutex
	sessions map[string]vendorstore.Session
}

func (f *fakeSessionRecords) Snapshot(_ context.Context, id string) (vendorstore.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[id]
	if !ok {
		return vendorstore.Session{}, vendorstore.ErrSessionNotFound
	}
	return session, nil
}

// setLeadMode changes the approval mode of the lead session.
func (f *fakeSessionRecords) setLeadMode(mode interaction.ApprovalMode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session := f.sessions[sessionLead]
	session.ApprovalMode = string(mode)
	f.sessions[sessionLead] = session
}

type toolFixture struct {
	svc     *SessionToolService
	inter   *InteractionService
	sub     *Subscription
	records *fakeSessionRecords
	tags    *environments.Registry

	mu   sync.Mutex
	runs []string
}

func (f *toolFixture) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.runs...)
}

func (f *toolFixture) record(tool string) func(context.Context, string, json.RawMessage) (string, error) {
	return func(_ context.Context, sessionID string, args json.RawMessage) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.runs = append(f.runs, tool+"@"+sessionID)
		return tool + "@" + sessionID + ":" + string(args), nil
	}
}

func noFacts(json.RawMessage) (FugueToolPlan, error) { return FugueToolPlan{}, nil }

// deployPlan is a tool with a target: the env argument names its tag.
func deployPlan(args json.RawMessage) (FugueToolPlan, error) {
	var in struct {
		Env string `json:"env"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return FugueToolPlan{}, errcode.Wrap(errcode.InvalidArgument, "decode deploy arguments", err)
	}
	return FugueToolPlan{
		Call:    interaction.Call{HasTarget: true, EnvTag: in.Env, GrantScopeKey: toolDeploy + ":target"},
		Summary: "deploy to " + in.Env, GrantScopeLabel: "every deploy of this session",
	}, nil
}

func newToolFixture(t *testing.T) *toolFixture {
	t.Helper()
	clk := clock.NewFake(time.Unix(100, 0))
	gen := ids.NewUUIDv7(clk, ids.NewDeterministic("tool-fixture"))
	f := &toolFixture{tags: environments.New(), records: &fakeSessionRecords{sessions: map[string]vendorstore.Session{
		sessionLead: {ID: sessionLead, Role: roleLead, State: vendorstore.StateStarting, ApprovalMode: vendorstore.ApprovalAuto, WorkspaceID: testWorkspaceID},
		sessionWork: {ID: sessionWork, Role: roleWorker, State: vendorstore.StateStarting, ApprovalMode: vendorstore.ApprovalAuto, WorkspaceID: testWorkspaceID},
	}}}
	owner, err := interaction.NewOwner(&interaction.Config{Clock: clk, IDs: gen, TagPolicy: f.tags})
	if err != nil {
		t.Fatal(err)
	}
	hub := NewEventHub(testAppIncarnation)
	if f.inter, err = NewInteractionService(owner, hub, clk); err != nil {
		t.Fatal(err)
	}
	if f.sub, err = hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindApp), Topic: TopicInteraction}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.sub.Close)
	tools := []*FugueTool{
		{Name: toolEcho, Description: "echo", InputSchema: json.RawMessage(`{"type":"object"}`), Descriptor: interaction.Descriptor{Risk: interaction.RiskRead, Confirm: interaction.ConfirmNever}, Plan: noFacts, Run: f.record(toolEcho)},
		{Name: toolDeploy, Description: "deploy", Descriptor: interaction.Descriptor{Risk: interaction.RiskWrite, Confirm: interaction.ConfirmEnvTag}, Plan: deployPlan, Run: f.record(toolDeploy)},
		{Name: toolLeadOnly, Description: "lead only", Roles: []string{roleLead}, Descriptor: interaction.Descriptor{Risk: interaction.RiskRead, Confirm: interaction.ConfirmNever}, Plan: noFacts, Run: f.record(toolLeadOnly)},
	}
	f.svc = newSessionToolService(newSessionTokens(ids.NewDeterministic(tokenSeed)), tools, f.records, f.inter, f.tags, gen)
	return f
}

func (f *toolFixture) attach(t *testing.T, sessionID, role string) (SessionBinding, string) {
	t.Helper()
	token, err := f.svc.tokens.mint(sessionID, role)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := f.svc.Attach(token.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	return binding, token.Reveal()
}

func toolNames(t *testing.T, svc *SessionToolService, binding SessionBinding) []string {
	t.Helper()
	views, err := svc.List(binding)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(views))
	for _, view := range views {
		names = append(names, view.Name)
	}
	return names
}

func TestSessionTokenOpensOnlyItsSessionAndRole(t *testing.T) {
	t.Parallel()
	f := newToolFixture(t)
	lead, leadToken := f.attach(t, sessionLead, roleLead)
	worker, workerToken := f.attach(t, sessionWork, roleWorker)

	// 256 bits from the injected randomness port, never the same twice.
	want := make([]byte, sessionTokenBytes)
	if err := ids.NewDeterministic(tokenSeed).Read(want); err != nil {
		t.Fatal(err)
	}
	if leadToken != base64.RawURLEncoding.EncodeToString(want) || leadToken == workerToken {
		t.Fatal("the session tokens do not come from the injected randomness port")
	}
	if lead.SessionID != sessionLead || lead.Role != roleLead || worker.SessionID != sessionWork || worker.Role != roleWorker {
		t.Fatalf("bindings = %+v, %+v", lead, worker)
	}
	f.assertToolsByRole(t, lead, worker)
	assertForgedBindingsOpenNothing(t, f, lead)
	f.assertWrongTokensRefused(t, leadToken)
	// A replaced token ends the old one and every binding made from it.
	if _, err := f.svc.tokens.mint(sessionLead, roleLead); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Attach(leadToken); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Errorf("a replaced token = %v, want permission_denied", err)
	}
	if _, err := f.svc.List(lead); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Errorf("a binding of a replaced token = %v, want permission_denied", err)
	}
}

// assertToolsByRole checks that each binding sees and runs the tools of its
// own role and session only.
func (f *toolFixture) assertToolsByRole(t *testing.T, lead, worker SessionBinding) {
	t.Helper()
	if diff := cmp.Diff([]string{toolDeploy, toolEcho, toolLeadOnly}, toolNames(t, f.svc, lead)); diff != "" {
		t.Errorf("lead tools (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{toolDeploy, toolEcho}, toolNames(t, f.svc, worker)); diff != "" {
		t.Errorf("worker tools (-want +got):\n%s", diff)
	}
	if _, err := f.svc.Call(t.Context(), worker, toolLeadOnly, nil); errcode.CodeOf(err) != errcode.NotFound {
		t.Errorf("a worker calling a lead tool = %v, want not_found", err)
	}
	for _, binding := range []SessionBinding{lead, worker} {
		res, err := f.svc.Call(t.Context(), binding, toolEcho, []byte(`{"n":1}`))
		if err != nil || res.IsError || res.Text != toolEcho+"@"+binding.SessionID+`:{"n":1}` {
			t.Errorf("echo for %s = %+v, %v", binding.SessionID, res, err)
		}
	}
	if _, err := f.svc.Call(t.Context(), lead, toolEcho, []byte(`{not json`)); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Errorf("malformed arguments = %v, want invalid_argument", err)
	}
}

func (f *toolFixture) assertWrongTokensRefused(t *testing.T, token string) {
	t.Helper()
	for _, bad := range []string{"", "not-a-token", token + "x", token[:len(token)-1]} {
		if _, err := f.svc.Attach(bad); errcode.CodeOf(err) != errcode.PermissionDenied || strings.Contains(err.Error(), token) {
			t.Errorf("Attach of a wrong token = %v, want a permission_denied that names no token", err)
		}
	}
}

// assertForgedBindingsOpenNothing: a binding is only what a live token made
// it. A forged one, and one with another role or session, open nothing.
func assertForgedBindingsOpenNothing(t *testing.T, f *toolFixture, lead SessionBinding) {
	t.Helper()
	forged := []SessionBinding{{SessionID: sessionLead, Role: roleLead}, lead, lead}
	forged[1].Role = roleWorker
	forged[2].SessionID = sessionWork
	for _, binding := range forged {
		if _, err := f.svc.List(binding); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Errorf("List(%+v) = %v, want permission_denied", binding, err)
		}
		if _, err := f.svc.Call(t.Context(), binding, toolEcho, nil); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Errorf("Call(%+v) = %v, want permission_denied", binding, err)
		}
	}
}

func TestSessionTokensAreStoredAsHashesAndRevoked(t *testing.T) {
	t.Parallel()
	tokens := newSessionTokens(ids.NewDeterministic(tokenSeed))
	token, err := tokens.mint(sessionLead, roleLead)
	if err != nil {
		t.Fatal(err)
	}
	if got := token.String(); strings.Contains(got, token.Reveal()) {
		t.Fatalf("a formatted token shows itself: %q", got)
	}
	for _, binding := range tokens.bySession {
		if strings.Contains(string(binding.hash[:]), token.Reveal()) || len(binding.hash) != 32 {
			t.Fatal("the registry keeps more than a hash")
		}
	}
	if _, mintErr := tokens.mint("", roleLead); errcode.CodeOf(mintErr) != errcode.InvalidArgument {
		t.Errorf("mint without a session = %v, want invalid_argument", mintErr)
	}
	tokens.revoke(sessionLead)
	if _, ok := tokens.verify(token.Reveal()); ok {
		t.Error("a revoked token still verifies")
	}
	second, err := tokens.mint(sessionWork, roleWorker)
	if err != nil {
		t.Fatal(err)
	}
	tokens.revokeAll()
	if _, ok := tokens.verify(second.Reveal()); ok {
		t.Error("a token survived the host stop")
	}
}

func (f *toolFixture) awaitPrompt(t *testing.T) PendingInteractionView {
	t.Helper()
	return awaitRequested(t, f.sub)
}

func (f *toolFixture) answer(t *testing.T, view *PendingInteractionView, outcome string) error {
	t.Helper()
	_, err := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "answer-" + view.RequestID + "-" + outcome, Principal: string(KindApp),
		Incarnation: view.Incarnation, RequestID: view.RequestID, Outcome: outcome,
	})
	return err
}

type toolCall struct {
	res  FugueToolResult
	err  error
	done chan struct{}
}

func (f *toolFixture) callDeploy(ctx context.Context, binding SessionBinding, args string) *toolCall {
	call := &toolCall{done: make(chan struct{})}
	supervise.Go(ctx, "test.tool-call", func(ctx context.Context) error {
		defer close(call.done)
		call.res, call.err = f.svc.Call(ctx, binding, toolDeploy, []byte(args))
		return nil
	})
	return call
}

func (c *toolCall) wait(t *testing.T) FugueToolResult {
	t.Helper()
	<-c.done
	if c.err != nil {
		t.Fatal(c.err)
	}
	return c.res
}

func TestFugueToolFromVendorSessionGoesThroughDecide(t *testing.T) {
	t.Parallel()
	f := newToolFixture(t)
	lead, _ := f.attach(t, sessionLead, roleLead)
	modes := []interaction.ApprovalMode{interaction.ModePlan, interaction.ModeAsk, interaction.ModeAuto, interaction.ModeFull}

	// A target whose tag requires confirmation — prod, an unknown tag, no tag
	// — asks in every mode and offers this one call only (I-03).
	for _, mode := range modes {
		for _, env := range []string{environments.TagProd, "mystery", ""} {
			f.assertHardRuleAsks(t, lead, mode, env)
		}
	}
	if got := len(f.ran()); got != len(modes)*3 {
		t.Fatalf("runs = %d, want one per allowed call", got)
	}
	f.assertModesAndGrants(t, lead)
}

// assertHardRuleAsks calls the deploy tool on a target whose tag requires
// confirmation and answers the prompt it must raise.
func (f *toolFixture) assertHardRuleAsks(t *testing.T, lead SessionBinding, mode interaction.ApprovalMode, env string) {
	t.Helper()
	f.records.setLeadMode(mode)
	call := f.callDeploy(t.Context(), lead, `{"env":"`+env+`"}`)
	view := f.awaitPrompt(t)
	if diff := cmp.Diff([]string{OutcomeAllowedOnce, OutcomeDenied}, view.OfferedOutcomes); diff != "" || view.EnvTag != env || view.Tool != toolDeploy || view.SessionID != sessionLead || view.GrantScopeLabel != "" {
		t.Fatalf("mode %s, env %q: prompt = %+v (offers -want +got):\n%s", mode, env, view, diff)
	}
	if err := f.answer(t, &view, OutcomeAllowedSession); errcode.CodeOf(err) != errcode.InteractionOptionNotOffered {
		t.Fatalf("mode %s: a session grant on a hard-rule request = %v, want option_not_offered", mode, err)
	}
	if err := f.answer(t, &view, OutcomeAllowedOnce); err != nil {
		t.Fatal(err)
	}
	if res := call.wait(t); res.IsError {
		t.Fatalf("mode %s, env %q: an allowed call failed: %+v", mode, env, res)
	}
}

func (f *toolFixture) assertModesAndGrants(t *testing.T, lead SessionBinding) {
	t.Helper()
	if err := f.tags.Set("qa", false); err != nil {
		t.Fatal(err)
	}
	f.assertPlanRefusesAndFullRuns(t, lead)
	f.assertSessionGrantSparesThePrompt(t, lead)
	f.assertTagChangeEndsTheGrant(t, lead)
}

func (f *toolFixture) assertPlanRefusesAndFullRuns(t *testing.T, lead SessionBinding) {
	t.Helper()
	before := len(f.ran())
	f.records.setLeadMode(interaction.ModePlan)
	refused, err := f.svc.Call(t.Context(), lead, toolDeploy, []byte(`{"env":"qa"}`))
	if err != nil || !refused.IsError || !strings.Contains(refused.Text, interaction.RefusePlanMode) || len(f.ran()) != before {
		t.Fatalf("plan mode write = %+v, %v; want a refusal that runs nothing", refused, err)
	}
	f.records.setLeadMode(interaction.ModeFull)
	if res, callErr := f.svc.Call(t.Context(), lead, toolDeploy, []byte(`{"env":"qa"}`)); callErr != nil || res.IsError {
		t.Fatalf("full mode on a tag that needs no confirmation = %+v, %v; want a run", res, callErr)
	}
}

func (f *toolFixture) assertSessionGrantSparesThePrompt(t *testing.T, lead SessionBinding) {
	t.Helper()
	f.records.setLeadMode(interaction.ModeAsk)
	call := f.callDeploy(t.Context(), lead, `{"env":"qa"}`)
	view := f.awaitPrompt(t)
	if diff := cmp.Diff([]string{OutcomeAllowedOnce, OutcomeAllowedSession, OutcomeDenied}, view.OfferedOutcomes); diff != "" {
		t.Fatalf("ask mode offers (-want +got):\n%s", diff)
	}
	if err := f.answer(t, &view, OutcomeAllowedSession); err != nil {
		t.Fatal(err)
	}
	call.wait(t)
	if res, err := f.svc.Call(t.Context(), lead, toolDeploy, []byte(`{"env":"qa"}`)); err != nil || res.IsError {
		t.Fatalf("a call covered by the session grant = %+v, %v; want a run without a prompt", res, err)
	}
}

// assertTagChangeEndsTheGrant: once the tag requires confirmation, the grant
// recorded above no longer counts (I-03, I-E03).
func (f *toolFixture) assertTagChangeEndsTheGrant(t *testing.T, lead SessionBinding) {
	t.Helper()
	if err := f.tags.Set("qa", true); err != nil {
		t.Fatal(err)
	}
	before := len(f.ran())
	call := f.callDeploy(t.Context(), lead, `{"env":"qa"}`)
	view := f.awaitPrompt(t)
	if diff := cmp.Diff([]string{OutcomeAllowedOnce, OutcomeDenied}, view.OfferedOutcomes); diff != "" {
		t.Fatalf("hard rule offers after a grant (-want +got):\n%s", diff)
	}
	if err := f.answer(t, &view, OutcomeDenied); err != nil {
		t.Fatal(err)
	}
	if res := call.wait(t); !res.IsError || len(f.ran()) != before {
		t.Fatalf("a denied call = %+v with %d new runs; want an error result and no run", res, len(f.ran())-before)
	}
}

func TestAbandonedToolConfirmationIsCancelled(t *testing.T) {
	t.Parallel()
	f := newToolFixture(t)
	lead, _ := f.attach(t, sessionLead, roleLead)
	ctx, cancel := context.WithCancel(t.Context())
	call := f.callDeploy(ctx, lead, `{"env":"prod"}`)
	view := f.awaitPrompt(t)
	cancel()
	<-call.done
	if errcode.CodeOf(call.err) != errcode.Canceled || len(f.ran()) != 0 {
		t.Fatalf("an abandoned call = %v with runs %v; want canceled and no run", call.err, f.ran())
	}
	if pending := f.inter.currentPendingViews(); len(pending) != 0 {
		t.Fatalf("the prompt %s outlived its call: %+v", view.RequestID, pending)
	}
	if err := f.answer(t, &view, OutcomeAllowedOnce); err == nil {
		t.Fatal("an abandoned prompt could still be answered")
	}
}

func TestAwaitResolvedReturnsAnAlreadyResolvedRequest(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	ticket, err := svc.Ensure(testSpec("op-await", "await test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "await-1", Principal: string(KindApp), Incarnation: svc.Incarnation(), RequestID: ticket.RequestID, Outcome: OutcomeDenied,
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.AwaitResolved(t.Context(), ticket.RequestID)
	if err != nil || resolved.Outcome != interaction.OutcomeDenied {
		t.Fatalf("AwaitResolved = %+v, %v", resolved, err)
	}
	if _, err := svc.AwaitResolved(t.Context(), "no-such-request"); errcode.CodeOf(err) != errcode.InteractionNotFound {
		t.Fatalf("AwaitResolved of an unknown request = %v, want not_found", err)
	}
	if len(svc.waiters) != 0 {
		t.Fatalf("waiters left behind: %d", len(svc.waiters))
	}
}

func TestSessionTokenDiesWithItsSessionAndItsPurge(t *testing.T) {
	t.Parallel()
	f := newVendorServiceFixture(t)
	tools, err := f.rt.SessionTools()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.tools) != 0 {
		t.Fatal("a normal host serves tools")
	}
	session := f.start(t, "token-1")
	tools.tokens.mu.Lock()
	binding, minted := tools.tokens.bySession[session.ID]
	tools.tokens.mu.Unlock()
	if !minted || binding.Role != vendorSessionRole || !tools.tokens.live(binding) {
		t.Fatalf("no live token for the spawned session: %+v", binding)
	}
	if owned, ownErr := f.rt.VendorOwnsPeer(1 << 30); ownErr == nil || owned {
		t.Fatalf("VendorOwnsPeer of an unknown pid = %v, %v; want an error", owned, ownErr)
	}
	if done, closeErr := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "token-close", SessionID: session.ID}); closeErr != nil || !done {
		t.Fatalf("close = %v, %v", done, closeErr)
	}
	if tools.tokens.live(binding) {
		t.Fatal("the token outlived its session")
	}
	if _, callErr := tools.Call(t.Context(), binding, toolEcho, nil); errcode.CodeOf(callErr) != errcode.PermissionDenied {
		t.Fatalf("a call after close = %v, want permission_denied", callErr)
	}
	assertPurgeRevokes(t, f, tools, session.ID)
}

// assertPurgeRevokes: a purge revokes whatever token the session still has.
func assertPurgeRevokes(t *testing.T, f *vendorServiceFixture, tools *SessionToolService, sessionID string) {
	t.Helper()
	if _, err := tools.tokens.mint(sessionID, vendorSessionRole); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.sessions.deps.Transcripts.PurgeSession(t.Context(), sessionID); err != nil && !errors.Is(err, vendorstore.ErrSessionNotFound) {
		t.Fatal(err)
	}
	tools.tokens.mu.Lock()
	_, still := tools.tokens.bySession[sessionID]
	tools.tokens.mu.Unlock()
	if still {
		t.Fatal("the token outlived the purge")
	}
}

func TestProductionWiringHonoursTheEnvTagHardRule(t *testing.T) {
	t.Parallel()
	var runs []string
	var runsMu sync.Mutex
	deploy := &FugueTool{
		Name: toolDeploy, Description: "deploy", Descriptor: interaction.Descriptor{Risk: interaction.RiskWrite, Confirm: interaction.ConfirmEnvTag}, Plan: deployPlan,
		Run: func(_ context.Context, _ string, args json.RawMessage) (string, error) {
			runsMu.Lock()
			defer runsMu.Unlock()
			runs = append(runs, string(args))
			return "deployed", nil
		},
	}
	f := newVendorServiceFixtureWith(t, []*FugueTool{deploy})
	tools, err := f.rt.SessionTools()
	if err != nil {
		t.Fatal(err)
	}
	session := f.start(t, "wiring-1")
	token, err := tools.tokens.mint(session.ID, vendorSessionRole)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tools.Attach(token.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	if res, callErr := tools.Call(t.Context(), binding, toolDeploy, []byte(`{"env":"dev"}`)); callErr != nil || res.IsError || res.Text != "deployed" {
		t.Fatalf("dev in the default mode = %+v, %v; want a run without a prompt", res, callErr)
	}
	f.assertProdAsksAndDenies(t, tools, binding)
	runsMu.Lock()
	defer runsMu.Unlock()
	if diff := cmp.Diff([]string{`{"env":"dev"}`}, runs); diff != "" {
		t.Fatalf("runs (-want +got):\n%s", diff)
	}
}

// assertProdAsksAndDenies calls the tool on prod and answers the prompt.
func (f *vendorServiceFixture) assertProdAsksAndDenies(t *testing.T, tools *SessionToolService, binding SessionBinding) {
	t.Helper()
	sub, err := f.rt.hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindApp), Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	call := make(chan FugueToolResult, 1)
	supervise.Go(t.Context(), "test.prod-call", func(ctx context.Context) error {
		res, callErr := tools.Call(ctx, binding, toolDeploy, []byte(`{"env":"prod"}`))
		if callErr != nil {
			t.Errorf("prod call: %v", callErr)
		}
		call <- res
		return nil
	})
	prompt := awaitRequested(t, sub)
	if diff := cmp.Diff([]string{OutcomeAllowedOnce, OutcomeDenied}, prompt.OfferedOutcomes); diff != "" || prompt.EnvTag != "prod" {
		t.Fatalf("prod prompt = %+v (offers -want +got):\n%s", prompt, diff)
	}
	if _, err = f.rt.Interactions().Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "wiring-deny", Principal: string(KindApp), Incarnation: prompt.Incarnation, RequestID: prompt.RequestID, Outcome: OutcomeDenied,
	}); err != nil {
		t.Fatal(err)
	}
	if res := <-call; !res.IsError {
		t.Fatalf("a denied prod call = %+v, want an error result", res)
	}
}

// awaitRequested returns the next request the user is asked.
func awaitRequested(t *testing.T, sub *Subscription) PendingInteractionView {
	t.Helper()
	for {
		ev, ok := sub.Next(t.Context())
		if !ok {
			t.Fatal("the interaction stream ended before a request")
		}
		if ev.Type != EventInteractionRequested {
			continue
		}
		var view PendingInteractionView
		if err := json.Unmarshal(ev.Payload, &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
}
