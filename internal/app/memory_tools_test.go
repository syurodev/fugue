package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspacememory"
)

const (
	testMemorySeedSession   = "seed"
	testMemorySignalKeyword = "signal"
)

type memoryToolTestFixture struct {
	hp       *HostProcess
	rt       *HostRuntime
	tools    *SessionToolService
	memStore *workspacememory.Store
	wsA      string
	wsB      string
	leadA    SessionBinding
	workerA  SessionBinding
	leadB    SessionBinding
}

func newMemoryToolFixture(t *testing.T, modeA string) *memoryToolTestFixture {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	deps := testHostDeps(t, cheapVaultDataDir(t), clk, newScriptedTransport())
	deps.WorkspaceEnv = config.MapEnv{config.EnvFugueHome: fugueHome}
	hp, err := StartHost(ctx, deps)
	if err != nil {
		t.Fatalf("start host: %v", err)
	}
	closeAppStateHost(t, hp)
	unlockCheapVault(t, hp)

	rt := hp.Runtime()
	memStore, err := rt.MemoryStore()
	if err != nil {
		t.Fatalf("MemoryStore: %v", err)
	}
	tools, err := rt.SessionTools()
	if err != nil {
		t.Fatalf("SessionTools: %v", err)
	}
	wsA, wsB := createMemoryTestWorkspaces(t, rt)
	return &memoryToolTestFixture{
		hp:       hp,
		rt:       rt,
		tools:    tools,
		memStore: memStore,
		wsA:      wsA,
		wsB:      wsB,
		leadA:    seedMemorySession(ctx, t, tools, hp.vendorStore, "mem-lead-a", wsA, roleLead, modeA),
		workerA:  seedMemorySession(ctx, t, tools, hp.vendorStore, "mem-worker-a", wsA, roleWorker, modeA),
		leadB:    seedMemorySession(ctx, t, tools, hp.vendorStore, "mem-lead-b", wsB, roleLead, modeA),
	}
}

func createMemoryTestWorkspaces(t *testing.T, rt *HostRuntime) (wsA, wsB string) {
	t.Helper()
	ctx := t.Context()
	wA, err := rt.workspaces.Create(ctx, string(KindApp), "mem-ws-a")
	if err != nil {
		t.Fatalf("Create wsA: %v", err)
	}
	if _, rootAErr := rt.workspaces.AddRoot(ctx, string(KindApp), wA.ID, teamTestRootName, t.TempDir(), "rw", wA.ConfigHash); rootAErr != nil {
		t.Fatalf("AddRoot wsA: %v", rootAErr)
	}
	wB, err := rt.workspaces.Create(ctx, string(KindApp), "mem-ws-b")
	if err != nil {
		t.Fatalf("Create wsB: %v", err)
	}
	if _, rootBErr := rt.workspaces.AddRoot(ctx, string(KindApp), wB.ID, teamTestRootName, t.TempDir(), "rw", wB.ConfigHash); rootBErr != nil {
		t.Fatalf("AddRoot wsB: %v", rootBErr)
	}
	return wA.ID, wB.ID
}

func seedMemorySession(
	ctx context.Context,
	t *testing.T,
	tools *SessionToolService,
	vs *vendorstore.Store,
	sessionID, wsID, role, mode string,
) SessionBinding {
	t.Helper()
	if err := vs.CreateSession(ctx, &vendorstore.Session{
		ID: sessionID, Vendor: VendorClaude, AccountID: vendorDefaultAccount,
		Role: role, State: vendorstore.StateReady, ApprovalMode: mode,
		WorkspaceID: wsID, WorkspaceRoot: teamTestRootName,
	}); err != nil {
		t.Fatalf("CreateSession %s: %v", sessionID, err)
	}
	tok, err := tools.tokens.mint(sessionID, role)
	if err != nil {
		t.Fatalf("mint token %s: %v", sessionID, err)
	}
	binding, err := tools.Attach(tok.Reveal())
	if err != nil {
		t.Fatalf("Attach %s: %v", sessionID, err)
	}
	return binding
}

func TestWME04SessionTokenScopingAndWorkspaceArgRefused(t *testing.T) {
	t.Parallel()
	f := newMemoryToolFixture(t, vendorstore.ApprovalAuto)
	ctx := t.Context()

	if _, err := f.memStore.Add(ctx, &workspacememory.AddInput{
		WorkspaceID: f.wsA, Kind: workspacememory.KindFact, Label: "alpha-secret-only-name",
		Summary: "Workspace A owns database migration zero.", Body: "Details for alpha only.",
		Author: workspacememory.AuthorUser, Provenance: workspacememory.Provenance{Principal: string(KindApp)},
	}); err != nil {
		t.Fatalf("Add wsA: %v", err)
	}
	entryB, err := f.memStore.Add(ctx, &workspacememory.AddInput{
		WorkspaceID: f.wsB, Kind: workspacememory.KindFact, Label: "bravo-exclusive-token",
		Summary: "Workspace B isolated finding zebra.", Body: "Details for bravo only.",
		Author: workspacememory.AuthorUser, Provenance: workspacememory.Provenance{Principal: string(KindApp)},
	})
	if err != nil {
		t.Fatalf("Add wsB: %v", err)
	}

	assertSessionSeesOnlyOwnWorkspace(t, f, entryB.ID)
	assertWorkspaceArgRefused(t, f)
}

func assertSessionSeesOnlyOwnWorkspace(t *testing.T, f *memoryToolTestFixture, otherEntryID string) {
	t.Helper()
	ctx := t.Context()
	ovA, err := f.tools.Call(ctx, f.leadA, memoryOverviewToolName, []byte(`{}`))
	if err != nil || ovA.IsError {
		t.Fatalf("fugue_memory_overview wsA = (%+v, %v)", ovA, err)
	}
	if !strings.Contains(ovA.Text, "alpha-secret-only-name") || strings.Contains(ovA.Text, "bravo-exclusive-token") {
		t.Fatalf("overview A leaked or missed workspace entries:\n%s", ovA.Text)
	}

	srA, err := f.tools.Call(ctx, f.workerA, memorySearchToolName, []byte(`{"query":"zebra"}`))
	if err != nil || srA.IsError {
		t.Fatalf("fugue_memory_search wsA = (%+v, %v)", srA, err)
	}
	if strings.Contains(srA.Text, "bravo-exclusive-token") || strings.Contains(srA.Text, otherEntryID) {
		t.Fatalf("search in wsA exposed wsB entry:\n%s", srA.Text)
	}

	getOther, err := f.tools.Call(ctx, f.leadA, memoryGetToolName, []byte(`{"id":"`+otherEntryID+`"}`))
	if err != nil || !getOther.IsError {
		t.Fatalf("cross-workspace get = (%+v, %v), want error tool result", getOther, err)
	}
}

func assertWorkspaceArgRefused(t *testing.T, f *memoryToolTestFixture) {
	t.Helper()
	ctx := t.Context()
	if _, err := f.tools.Call(ctx, f.leadA, memorySearchToolName, []byte(`{"query":"alpha","workspace_id":"`+f.wsB+`"}`)); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("workspace_id search error = %v, want invalid_argument", err)
	}
	if _, err := f.tools.Call(ctx, f.leadA, memoryGetToolName, []byte(`{"label":"alpha-secret-only-name","workspace_id":"`+f.wsB+`"}`)); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("workspace_id get error = %v, want invalid_argument", err)
	}
}

func TestWME04MetadataAndEnvelope(t *testing.T) {
	t.Parallel()
	f := newMemoryToolFixture(t, vendorstore.ApprovalAuto)
	seedWME04Entries(t, f)

	assertWME04ToolResults(t, f)
	listed, err := f.tools.List(t.Context(), f.workerA)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertMemoryToolDescriptions(t, listed)
}

func assertWME04ToolResults(t *testing.T, f *memoryToolTestFixture) {
	t.Helper()
	ctx := t.Context()
	ov, err := f.tools.Call(ctx, f.leadA, memoryOverviewToolName, []byte(`{}`))
	if err != nil || ov.IsError {
		t.Fatalf("overview = (%+v, %v)", ov, err)
	}
	assertEnvelopeAndMetadata(t, "overview", ov.Text, "TRUSTED_USER_PINNED", "UNTRUSTED_AGENT_NOTE_ONE")

	sr, err := f.tools.Call(ctx, f.workerA, memorySearchToolName, []byte(`{"query":"signal"}`))
	if err != nil || sr.IsError {
		t.Fatalf("search = (%+v, %v)", sr, err)
	}
	assertEnvelopeAndMetadata(t, "search", sr.Text, "TRUSTED_VERIFIED_FACT", "UNTRUSTED_AGENT_SEARCH_ONE")
	assertDistinctNonces(t, "search", sr.Text, 2)

	getUnv, err := f.tools.Call(ctx, f.workerA, memoryGetToolName, []byte(`{"label":"unverified-search-one"}`))
	if err != nil || getUnv.IsError {
		t.Fatalf("get unverified = (%+v, %v)", getUnv, err)
	}
	assertEnvelopeAndMetadata(t, "get unverified", getUnv.Text, "", "UNTRUSTED_AGENT_SEARCH_ONE")

	getVer, err := f.tools.Call(ctx, f.workerA, memoryGetToolName, []byte(`{"label":"verified-signal"}`))
	if err != nil || getVer.IsError {
		t.Fatalf("get verified = (%+v, %v)", getVer, err)
	}
	if strings.Contains(getVer.Text, "⟪untrusted") || !strings.Contains(getVer.Text, "TRUSTED_VERIFIED_FACT") || !strings.Contains(getVer.Text, "verified=true") {
		t.Fatalf("verified get unexpected enveloping or missing metadata:\n%s", getVer.Text)
	}
}

func seedWME04Entries(t *testing.T, f *memoryToolTestFixture) {
	t.Helper()
	ctx := t.Context()
	entries := []workspacememory.AddInput{
		{
			WorkspaceID: f.wsA, Kind: workspacememory.KindPinned, Label: "verified-pin",
			Summary: "Trusted pinned summary.", Body: "TRUSTED_USER_PINNED",
			Author: workspacememory.AuthorUser, Provenance: workspacememory.Provenance{Principal: string(KindApp)},
		},
		{
			WorkspaceID: f.wsA, Kind: workspacememory.KindPinned, Label: "unverified-pin",
			Summary: "Unverified pinned summary.", Body: "UNTRUSTED_AGENT_NOTE_ONE",
			Author: workspacememory.AuthorAgent, Provenance: workspacememory.Provenance{Principal: PrincipalSession, SessionID: testMemorySeedSession},
		},
		{
			WorkspaceID: f.wsA, Kind: workspacememory.KindFact, Label: "verified-signal",
			Summary: "Verified TRUSTED_VERIFIED_FACT signal token.", Body: "TRUSTED_VERIFIED_FACT with signal keyword.",
			Keywords: []string{testMemorySignalKeyword}, Author: workspacememory.AuthorUser,
			Provenance: workspacememory.Provenance{Principal: string(KindApp)},
		},
		{
			WorkspaceID: f.wsA, Kind: workspacememory.KindNote, Label: "unverified-search-one",
			Summary: "Unverified UNTRUSTED_AGENT_SEARCH_ONE finding one.", Body: "UNTRUSTED_AGENT_SEARCH_ONE with signal keyword.",
			Keywords: []string{testMemorySignalKeyword}, Author: workspacememory.AuthorAgent,
			Provenance: workspacememory.Provenance{Principal: PrincipalSession, SessionID: testMemorySeedSession},
		},
		{
			WorkspaceID: f.wsA, Kind: workspacememory.KindNote, Label: "unverified-search-two",
			Summary: "Unverified UNTRUSTED_AGENT_SEARCH_TWO finding two.", Body: "UNTRUSTED_AGENT_SEARCH_TWO with signal keyword.",
			Keywords: []string{testMemorySignalKeyword}, Author: workspacememory.AuthorAgent,
			Provenance: workspacememory.Provenance{Principal: PrincipalSession, SessionID: testMemorySeedSession},
		},
	}
	for i := range entries {
		if _, err := f.memStore.Add(ctx, &entries[i]); err != nil {
			t.Fatalf("seed entry %s: %v", entries[i].Label, err)
		}
	}
}

func assertEnvelopeAndMetadata(t *testing.T, label, text, trustedNeedle, untrustedNeedle string) {
	t.Helper()
	for _, meta := range []string{"verified=", "author=", "freshness=", "revision="} {
		if !strings.Contains(text, meta) {
			t.Fatalf("%s missing %q metadata in:\n%s", label, meta, text)
		}
	}
	if trustedNeedle != "" && !strings.Contains(text, trustedNeedle) {
		t.Fatalf("%s missing trusted content %q in:\n%s", label, trustedNeedle, text)
	}
	if untrustedNeedle != "" {
		assertEnclosedUntrustedBlock(t, label, text, untrustedNeedle)
	}
	if _, err := os.Stat(text); err == nil {
		t.Fatal("unexpected file path side effect")
	}
}

func assertEnclosedUntrustedBlock(t *testing.T, label, text, untrustedNeedle string) {
	t.Helper()
	needleIdx := strings.Index(text, untrustedNeedle)
	if needleIdx < 0 {
		t.Fatalf("%s missing untrusted content %q in:\n%s", label, untrustedNeedle, text)
	}
	openIdx := strings.LastIndex(text[:needleIdx], "⟪untrusted ")
	closeRel := strings.Index(text[needleIdx:], "⟪/untrusted ")
	if openIdx < 0 || closeRel < 0 {
		t.Fatalf("%s unverified content %q not enclosed in untrusted envelope:\n%s", label, untrustedNeedle, text)
	}
	block := text[openIdx : needleIdx+closeRel]
	if !strings.Contains(block, "source=memory") || strings.Contains(block[1:], "⟪untrusted ") || strings.Contains(block, "⟪/untrusted ") {
		t.Fatalf("%s envelope boundary/attributes invalid around %q:\n%s", label, untrustedNeedle, text)
	}
}

func assertDistinctNonces(t *testing.T, label, text string, wantMin int) {
	t.Helper()
	re := regexp.MustCompile(`⟪untrusted ([0-9a-f]{8}) source=memory entry="[^"]+"⟫`)
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) < wantMin {
		t.Fatalf("%s envelope matches = %d, want >= %d\n%s", label, len(matches), wantMin, text)
	}
	seen := map[string]struct{}{}
	for _, m := range matches {
		nonce := m[1]
		if _, dup := seen[nonce]; dup {
			t.Fatalf("%s reused nonce %q:\n%s", label, nonce, text)
		}
		seen[nonce] = struct{}{}
		if !strings.Contains(text, "⟪/untrusted "+nonce+"⟫") {
			t.Fatalf("%s missing matching closing tag for nonce %q", label, nonce)
		}
	}
}

func assertMemoryToolDescriptions(t *testing.T, listed []FugueToolView) {
	t.Helper()
	byName := map[string]string{}
	for _, v := range listed {
		byName[v.Name] = v.Description
	}
	for _, name := range []string{memoryOverviewToolName, memorySearchToolName, memoryGetToolName, memoryWriteToolName} {
		desc, ok := byName[name]
		if !ok {
			t.Fatalf("missing tool %q in List output", name)
		}
		lower := strings.ToLower(desc)
		if !strings.Contains(lower, "verified") || !strings.Contains(lower, "stale") {
			t.Fatalf("%s description must warn about unverified/stale entries: %q", name, desc)
		}
		if !strings.Contains(lower, "update") || !strings.Contains(lower, "invalidate") {
			t.Fatalf("%s description must instruct update/invalidate instead of contradicting: %q", name, desc)
		}
		if !strings.Contains(lower, "authority") {
			t.Fatalf("%s description must state memory never grants authority: %q", name, desc)
		}
	}
	if !strings.Contains(byName[memorySearchToolName], memoryOverviewToolName) {
		t.Fatalf("%s description must point to %s: %q", memorySearchToolName, memoryOverviewToolName, byName[memorySearchToolName])
	}
}

func TestMemoryWritePolicyAndProvenance(t *testing.T) {
	t.Parallel()
	t.Run("auto does not prompt and records role provenance", func(t *testing.T) {
		t.Parallel()
		assertAutoWriteAndUserVerifyProvenance(t)
	})
	t.Run("ask prompts and plan refuses", func(t *testing.T) {
		t.Parallel()
		assertAskPromptsAndPlanRefuses(t)
	})
}

func assertAutoWriteAndUserVerifyProvenance(t *testing.T) {
	t.Helper()
	f := newMemoryToolFixture(t, vendorstore.ApprovalAuto)
	ctx := t.Context()
	for _, tc := range []struct {
		binding SessionBinding
		role    string
		label   string
	}{
		{f.leadA, roleLead, "lead-note"},
		{f.workerA, roleWorker, "worker-note"},
	} {
		res, err := f.tools.Call(ctx, tc.binding, memoryWriteToolName, []byte(`{"op":"add","kind":"note","label":"`+tc.label+`","summary":"Written by `+tc.role+`.","body":"Agent body for `+tc.role+`."}`))
		if err != nil || res.IsError {
			t.Fatalf("%s write = (%+v, %v)", tc.role, res, err)
		}
		assertAgentProvenance(t, f, tc.binding.SessionID, tc.role, tc.label)
	}
	assertHostUserWriteVerified(t, f)
}

func assertAgentProvenance(t *testing.T, f *memoryToolTestFixture, sessionID, role, label string) {
	t.Helper()
	entry, getErr := f.memStore.GetByLabel(t.Context(), f.wsA, label)
	if getErr != nil {
		t.Fatalf("GetByLabel %s: %v", label, getErr)
	}
	if entry.Verified || entry.Author != workspacememory.AuthorAgent || entry.Provenance.Role != role || entry.Provenance.SessionID != sessionID {
		t.Fatalf("%s entry metadata/provenance = %+v", role, entry)
	}
}

func assertHostUserWriteVerified(t *testing.T, f *memoryToolTestFixture) {
	t.Helper()
	memSvc, err := f.rt.MemoryService()
	if err != nil {
		t.Fatalf("MemoryService: %v", err)
	}
	userEntry, err := memSvc.WriteEntry(t.Context(), string(KindApp), &MemoryWriteCommand{
		WorkspaceID: f.wsA, Op: MemoryOpAdd, Kind: MemoryKindDecision,
		Label: "user-host-decision", Summary: "User verified via Host API.", Body: "User decision body.",
	})
	if err != nil {
		t.Fatalf("user WriteEntry: %v", err)
	}
	if !userEntry.Verified || userEntry.Author != MemoryAuthorUser {
		t.Fatalf("user entry = %+v, want verified user entry", userEntry)
	}
}

type memoryWriteOutcome struct {
	res FugueToolResult
	err error
}

func assertAskPromptsAndPlanRefuses(t *testing.T) {
	t.Helper()
	f := newMemoryToolFixture(t, vendorstore.ApprovalAsk)
	ctx := t.Context()
	sub, err := f.rt.hub.Subscribe(ctx, SubscribeParams{Principal: string(KindApp), Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	writeCh := make(chan memoryWriteOutcome, 1)
	supervise.Go(ctx, "test.memory-ask-write", func(callCtx context.Context) error {
		res, callErr := f.tools.Call(callCtx, f.leadA, memoryWriteToolName, []byte(`{"op":"add","kind":"note","label":"ask-note","summary":"Requires ask confirmation.","body":"Confirmed body."}`))
		writeCh <- memoryWriteOutcome{res: res, err: callErr}
		return nil
	})
	prompt := awaitMemoryPromptOrFail(t, sub, writeCh)
	if _, ansErr := f.rt.Interactions().Answer(ctx, &AnswerInteractionCommand{
		CommandID: "cmd-mem-ask-allow", Principal: string(KindApp), Incarnation: f.rt.Interactions().Incarnation(),
		RequestID: prompt.RequestID, Outcome: OutcomeAllowedOnce,
	}); ansErr != nil {
		t.Fatalf("Answer allow: %v", ansErr)
	}
	if out := <-writeCh; out.err != nil || out.res.IsError {
		t.Fatalf("ask write failed after allow: (%+v, %v)", out.res, out.err)
	}

	planSession := seedMemorySession(ctx, t, f.tools, f.hp.vendorStore, "mem-plan-a", f.wsA, roleLead, vendorstore.ApprovalPlan)
	planRes, planErr := f.tools.Call(ctx, planSession, memoryWriteToolName, []byte(`{"op":"add","kind":"note","label":"plan-note","summary":"Should refuse in plan mode.","body":"Denied body."}`))
	if planErr != nil || !planRes.IsError || !strings.Contains(planRes.Text, "plan_mode") {
		t.Fatalf("plan mode write = (%+v, %v), want plan_mode refusal", planRes, planErr)
	}
}

func awaitMemoryPromptOrFail(t *testing.T, sub *Subscription, writeCh <-chan memoryWriteOutcome) PendingInteractionView {
	t.Helper()
	ctx := t.Context()
	promptCh := make(chan PendingInteractionView, 1)
	supervise.Go(ctx, "test.await-memory-prompt", func(waitCtx context.Context) error {
		for {
			ev, ok := sub.Next(waitCtx)
			if !ok {
				return nil
			}
			if ev.Type != EventInteractionRequested {
				continue
			}
			var view PendingInteractionView
			if err := json.Unmarshal(ev.Payload, &view); err != nil {
				return fmt.Errorf("decode interaction event: %w", err)
			}
			promptCh <- view
			return nil
		}
	})
	select {
	case p := <-promptCh:
		return p
	case out := <-writeCh:
		t.Fatalf("ask write completed without prompting: (%+v, %v)", out.res, out.err)
		return PendingInteractionView{}
	}
}
