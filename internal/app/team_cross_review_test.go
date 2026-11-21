package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/ocr"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

const (
	crossReviewTestBrief = "Cross review brief."
	testOCRBinaryPath    = "/usr/local/bin/ocr"
)

type crossReviewFakeAdapter struct {
	mu       sync.Mutex
	calls    []crossReviewStartCall
	response func(prompt string) string
}

type crossReviewStartCall struct {
	sessionID string
	cwd       string
	prompt    string
}

func (a *crossReviewFakeAdapter) Probe(context.Context, vendor.AccountHome) (vendor.Probe, error) {
	return vendor.Probe{Path: "/bin/fake", Version: "1.0.0", VersionTested: true, Login: vendor.LoginActive}, nil
}

func (a *crossReviewFakeAdapter) Capability() vendor.Capability {
	return vendor.Capability{}
}

func (a *crossReviewFakeAdapter) Start(_ context.Context, spec *vendor.StartSpec) (vendor.Session, error) {
	return &crossReviewFakeSession{adapter: a, sessionID: spec.SessionID, cwd: spec.Cwd}, nil
}

func (a *crossReviewFakeAdapter) Resume(context.Context, *vendor.StartSpec) (vendor.Session, error) {
	return nil, vendor.ErrUnsupported
}

type crossReviewFakeSession struct {
	adapter   *crossReviewFakeAdapter
	sessionID string
	cwd       string
	ch        chan vendor.Observation
	once      sync.Once
}

func (s *crossReviewFakeSession) Observations() <-chan vendor.Observation {
	if s.ch == nil {
		s.ch = make(chan vendor.Observation, 16)
		s.ch <- vendor.Observation{Init: &vendor.Init{SessionID: s.sessionID}}
	}
	return s.ch
}

func (s *crossReviewFakeSession) Send(_ context.Context, input vendor.Input) (vendor.TurnRef, error) {
	s.adapter.mu.Lock()
	s.adapter.calls = append(s.adapter.calls, crossReviewStartCall{sessionID: s.sessionID, cwd: s.cwd, prompt: input.Text})
	respFn := s.adapter.response
	s.adapter.mu.Unlock()

	reply := "[]"
	if respFn != nil {
		reply = respFn(input.Text)
	}
	turnID := "turn-" + s.sessionID
	s.ch <- vendor.Observation{
		Records: []transcript.Item{{
			ID:        "msg-" + s.sessionID,
			SessionID: s.sessionID,
			TurnID:    turnID,
			Kind:      transcript.AssistantMessage,
			Metadata:  transcript.AssistantMessageMetadata{},
			Content:   transcript.AssistantMessageContent{Text: reply},
		}},
	}
	s.ch <- vendor.Observation{TurnEnd: &vendor.TurnEnd{TurnID: turnID, Outcome: "completed"}}
	return vendor.TurnRef{ID: turnID}, nil
}

func (s *crossReviewFakeSession) Steer(context.Context, vendor.TurnRef, vendor.Input) error {
	return nil
}

func (s *crossReviewFakeSession) Interrupt(context.Context, vendor.TurnRef) error { return nil }

func (s *crossReviewFakeSession) Resolve(context.Context, vendor.ApprovalRef, vendor.Decision) error {
	return nil
}

func (s *crossReviewFakeSession) Close(context.Context, vendor.CloseReason) (process.StopProof, error) {
	s.once.Do(func() {
		if s.ch != nil {
			close(s.ch)
		}
	})
	return process.StopProof{}, nil
}

func newCrossReviewFixture(t *testing.T, reviewerAdapter *crossReviewFakeAdapter, ocrProbe ocr.ProbeResult) *gateDiffFixture {
	t.Helper()
	f := newTeamFixture(t, "", false)
	initRepo(t, f.root)
	repoDir, err := filepath.EvalSymlinks(f.root)
	if err != nil {
		t.Fatal(err)
	}
	teamID := strings.ReplaceAll("team-cr-"+t.Name(), "/", "-")
	if len(teamID) > 40 {
		teamID = teamID[:40]
	}
	const leadID, memID, memSess = "t-lead", "t-mem", "sess-cr-mem"
	seedCrossReviewTeam(t, f, teamID, leadID, memID, memSess)
	wt := createGateWorktree(t, f, teamID, repoDir, memID)
	wireCrossReviewGate(t, f, reviewerAdapter, ocrProbe)
	return &gateDiffFixture{
		teamProposalFixture: f,
		teamID:              teamID,
		taskID:              memID,
		sessID:              memSess,
		binding:             mustAttachRole(t, f, memSess, vendorstore.RoleMember),
		wtDir:               wt,
		repoDir:             repoDir,
		repoID:              repoIDFor(repoDir),
	}
}

func seedCrossReviewTeam(t *testing.T, f *teamProposalFixture, teamID, leadID, memID, memSess string) {
	t.Helper()
	ctx := t.Context()
	roleBytes := mustMarshalJSON(t, teamDecisionPayload{
		Decision: decisionApprove,
		Roles: []decidedRole{
			{Name: gateTestRoleCoder, Vendor: VendorClaude, Permission: string(teams.PermissionWrite), CrossReview: true},
			{Name: "reviewer", Vendor: VendorCodex, Permission: string(teams.PermissionRead)},
		},
	})
	propBytes := mustMarshalJSON(t, teams.ProposalInput{
		Title: "CR", Brief: crossReviewTestBrief,
		Roles: []teams.ProposedRole{
			{Name: gateTestRoleCoder, Vendor: VendorClaude, Permission: teams.PermissionWrite},
			{Name: "reviewer", Vendor: VendorCodex, Permission: teams.PermissionRead},
		},
	})
	specDefault := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: gateTestLeadGoal})
	specPayload := mustMarshalJSON(t, teams.ProtectedTaskContent{
		Goal:       "cross review test",
		WriteScope: []string{"pkg/**", "docs/**"},
	})
	if err := f.hp.vendorStore.CreateSession(ctx, &vendorstore.Session{
		ID: memSess, Vendor: VendorClaude, AccountID: vendorDefaultAccount,
		Role: string(vendorstore.RoleMember), State: vendorstore.StateReady, ApprovalMode: string(interaction.ModeAuto),
		TeamID: teamID, TaskID: memID, WorkspaceID: f.wsID, WorkspaceRoot: teamTestRootName,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.rt.transcripts.RegisterSession(memSess); err != nil {
		t.Fatal(err)
	}
	rec, _, err := f.hp.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: f.wsID, SessionID: f.session.ID,
		ProposedEventID: "cr-prop", ProposedPayload: propBytes,
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: leadID, HierarchyRole: vendorstore.RoleLead, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "cr-l", ProtectedSpec: specDefault},
			{TaskID: memID, ParentTaskID: leadID, HierarchyRole: vendorstore.RoleMember, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "cr-m", ProtectedSpec: specPayload},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	seq := rec.LastSeq
	events := []vendorstore.AppendTeamEventInput{
		{EventID: "cr-start", EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes},
		{EventID: "cr-a", TaskID: memID, EventType: vendorstore.EventTaskAssigned, SessionID: memSess},
		{EventID: "cr-r", TaskID: memID, EventType: vendorstore.EventTaskStarted},
	}
	for i := range events {
		var appErr error
		seq, appErr = f.hp.vendorStore.AppendTeamEvent(ctx, teamID, seq, &events[i])
		if appErr != nil {
			t.Fatal(appErr)
		}
	}
}

func wireCrossReviewGate(t *testing.T, f *teamProposalFixture, reviewerAdapter *crossReviewFakeAdapter, ocrProbe ocr.ProbeResult) {
	t.Helper()
	ctx := t.Context()
	adapters := map[string]vendor.Adapter{
		VendorClaude: &crossReviewFakeAdapter{},
		VendorCodex:  reviewerAdapter,
	}
	vs, err := NewVendorSessions(ctx, &VendorSessionDeps{
		Store:       f.hp.vendorStore,
		Transcripts: f.rt.transcripts,
		Adapters:    adapters,
		Vault:       f.hp.vault,
		Clock:       f.hp.deps.Clock,
		IDs:         f.hp.deps.IDs,
		HostConfig: func(context.Context) (workspace.HostConfig, error) {
			return workspace.HostConfig{PromptPacingMinSeconds: 0, PromptPacingMaxSeconds: 0}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := vs.CloseAll(context.Background()); closeErr != nil {
			t.Logf("CloseAll: %v", closeErr)
		}
	})
	vendorSvc := &VendorService{
		store:    f.hp.vendorStore,
		sessions: vs,
		adapters: adapters,
		vault:    f.hp.vault,
		hostEnv:  []string{"PATH=/usr/bin:/bin"},
		tokens:   f.rt.vendors.tokens,
	}
	f.rt.teamTasks.resultGate = newHostResultGate(f.hp.vendorStore, f.rt.teamTasks.git, f.rt.teamTasks.dataDir,
		f.rt.workspaces.domain, f.rt.interactions, f.rt.tags, f.rt.heavy, vs, vendorSvc, ocrProbe, f.hp.deps.IDs)
}

func writeWorktreeFiles(t *testing.T, wtDir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		abs := filepath.Join(wtDir, rel)
		if mkErr := os.MkdirAll(filepath.Dir(abs), 0o700); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wErr := os.WriteFile(abs, []byte(body), 0o600); wErr != nil {
			t.Fatal(wErr)
		}
	}
}

func readCrossReviewReport(t *testing.T, f *gateDiffFixture) CrossReviewReport {
	t.Helper()
	raw, found, err := f.hp.vendorStore.OpenLatestTaskEventPayload(t.Context(), f.teamID, f.taskID, vendorstore.EventTaskCheckCompleted)
	if err != nil || !found {
		t.Fatalf("OpenLatestTaskEventPayload = (%t, %v), want found", found, err)
	}
	var report CrossReviewReport
	if decErr := json.Unmarshal(raw, &report); decErr != nil {
		t.Fatal(decErr)
	}
	return report
}

func TestCrossReviewBundlesWithOCRAndVerifiesPositions(t *testing.T) {
	t.Parallel()
	previewJSON, err := os.ReadFile(filepath.Join("..", "ocr", "testdata", "preview.json"))
	if err != nil {
		t.Fatal(err)
	}
	ruleJSON, err := os.ReadFile(filepath.Join("..", "ocr", "testdata", "rule.json"))
	if err != nil {
		t.Fatal(err)
	}
	reviewer := &crossReviewFakeAdapter{
		response: func(prompt string) string {
			if strings.Contains(prompt, "pkg/auth/session.go") {
				return `[{"file":"pkg/auth/session.go","line_start":3,"line_end":3,"severity":"high","rule":"sec-token-leak","description":"secret logged"},
				         {"file":"pkg/auth/session.go","line_start":3,"line_end":3,"severity":"low","rule":"nit","description":"drop low"},
				         {"file":"pkg/auth/session.go","line_start":99,"line_end":99,"severity":"medium","rule":"bad-pos","description":"out of diff"}]`
			}
			return `[{"file":"pkg/api/handler.go","line_start":3,"line_end":3,"severity":"medium","rule":"err-wrap","description":"wrap err"}]`
		},
	}
	f := newCrossReviewFixture(t, reviewer, ocr.ProbeResult{Found: true, Path: testOCRBinaryPath, Version: "1.14.0"})
	writeWorktreeFiles(t, f.wtDir, map[string]string{
		"pkg/auth/session.go": "package auth\n\nfunc Verify() bool { return true }\n",
		"pkg/api/handler.go":  "package api\n\nfunc Handle() error { return nil }\n",
		"docs/notes.md":       "# notes\n",
	})
	gate, ok := f.rt.teamTasks.resultGate.(*hostResultGate)
	if !ok {
		t.Fatal("resultGate is not *hostResultGate")
	}
	var ocrCmds []string
	gate.runCheck = func(_ context.Context, _, command string) (string, int, error) {
		ocrCmds = append(ocrCmds, command)
		if strings.Contains(command, "delegate preview") {
			return string(previewJSON), 0, nil
		}
		if strings.Contains(command, "delegate rule") {
			return string(ruleJSON), 0, nil
		}
		return "", 1, nil
	}
	if _, doneErr := f.rt.teamTasks.Done(t.Context(), f.sessID, "implemented auth and api", nil, taskStatusSucceeded); doneErr != nil {
		t.Fatalf("Done: %v", doneErr)
	}
	assertOCRReviewOutcome(t, f, reviewer, ocrCmds)
}

func assertOCRReviewOutcome(t *testing.T, f *gateDiffFixture, reviewer *crossReviewFakeAdapter, ocrCmds []string) {
	t.Helper()
	assertReviewerSessions(t, f, reviewer, ocrCmds)
	assertOCRReviewReport(t, f)
}

func assertReviewerSessions(t *testing.T, f *gateDiffFixture, reviewer *crossReviewFakeAdapter, ocrCmds []string) {
	t.Helper()
	if len(ocrCmds) != 2 || !strings.Contains(ocrCmds[0], "delegate preview") || !strings.Contains(ocrCmds[1], "delegate rule") {
		t.Fatalf("unexpected ocr commands: %v", ocrCmds)
	}
	if len(reviewer.calls) != 2 {
		t.Fatalf("reviewer calls = %d, want 2 bundles", len(reviewer.calls))
	}
	for _, call := range reviewer.calls {
		snap, snapErr := f.hp.vendorStore.Snapshot(t.Context(), call.sessionID)
		if snapErr != nil {
			t.Fatal(snapErr)
		}
		if snap.Vendor != VendorCodex || snap.ApprovalMode != string(interaction.ModePlan) || snap.Role != string(vendorstore.RoleMember) {
			t.Fatalf("reviewer session = %+v, want codex + plan + member", snap)
		}
	}
}

func assertOCRReviewReport(t *testing.T, f *gateDiffFixture) {
	t.Helper()
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(t.Context(), f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	if mem, _ := findTask(tasks, f.taskID); mem.State != vendorstore.TaskStateReview {
		t.Fatalf("task state = %s, want review", mem.State)
	}
	report := readCrossReviewReport(t, f)
	if len(report.Findings) != 2 || report.Coverage.DroppedLowCount != 1 || report.Coverage.DroppedPositionCount != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if !report.Coverage.UsedOCR || report.Coverage.ReviewedFiles != 2 || report.Coverage.TotalFiles != 3 {
		t.Fatalf("coverage = %+v, want OCR 2/3", report.Coverage)
	}
	if len(report.Coverage.SkipReasons) != 1 || !strings.Contains(report.Coverage.SkipReasons[0], "docs/notes.md") {
		t.Fatalf("skip reasons = %v, want docs/notes.md", report.Coverage.SkipReasons)
	}
}

func TestCrossReviewFallsBackWithoutOCR(t *testing.T) {
	t.Parallel()
	previewJSON, err := os.ReadFile(filepath.Join("..", "ocr", "testdata", "preview.json"))
	if err != nil {
		t.Fatal(err)
	}
	ruleJSON, err := os.ReadFile(filepath.Join("..", "ocr", "testdata", "rule.json"))
	if err != nil {
		t.Fatal(err)
	}
	unknownPreview, err := os.ReadFile(filepath.Join("..", "ocr", "testdata", "preview_unknown_schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		probe     ocr.ProbeResult
		ocrReply  string
		wantOCR   bool
		wantNote  string
		wantTotal int
	}{
		{
			name:      "newer_ocr_supported_schema",
			probe:     ocr.ProbeResult{Found: true, Path: testOCRBinaryPath, Version: "1.14.0"},
			wantOCR:   true,
			wantNote:  "OCR",
			wantTotal: 3,
		},
		{
			name:      "unknown_schema_version",
			probe:     ocr.ProbeResult{Found: true, Path: testOCRBinaryPath, Version: "1.15.0"},
			ocrReply:  string(unknownPreview),
			wantOCR:   false,
			wantNote:  "OCR 1.15.0: schema 99 not supported",
			wantTotal: 2,
		},
		{
			name:      "missing_binary",
			probe:     ocr.ProbeResult{Found: false},
			wantOCR:   false,
			wantNote:  "no OCR",
			wantTotal: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCrossReviewFallbackCase(t, tc.probe, tc.ocrReply, string(previewJSON), string(ruleJSON), tc.wantOCR, tc.wantNote, tc.wantTotal)
		})
	}
}

func runCrossReviewFallbackCase(
	t *testing.T,
	probe ocr.ProbeResult,
	ocrReply, previewJSON, ruleJSON string,
	wantOCR bool,
	wantNote string,
	wantTotal int,
) {
	t.Helper()
	reviewer := &crossReviewFakeAdapter{}
	f := newCrossReviewFixture(t, reviewer, probe)
	writeWorktreeFiles(t, f.wtDir, map[string]string{
		"pkg/a/one.go": "package x\n",
		"pkg/b/two.go": "package x\n",
	})
	gate, ok := f.rt.teamTasks.resultGate.(*hostResultGate)
	if !ok {
		t.Fatal("resultGate is not *hostResultGate")
	}
	gate.runCheck = func(_ context.Context, _, cmd string) (string, int, error) {
		if wantOCR {
			if strings.Contains(cmd, "delegate rule") {
				return ruleJSON, 0, nil
			}
			return previewJSON, 0, nil
		}
		return ocrReply, 0, nil
	}
	if _, doneErr := f.rt.teamTasks.Done(t.Context(), f.sessID, "done", nil, taskStatusSucceeded); doneErr != nil {
		t.Fatalf("Done: %v", doneErr)
	}
	if len(reviewer.calls) != 2 {
		t.Fatalf("reviewer calls = %d, want 2", len(reviewer.calls))
	}
	report := readCrossReviewReport(t, f)
	if report.Coverage.UsedOCR != wantOCR || !strings.Contains(report.Coverage.ModeLabel, wantNote) ||
		report.Coverage.ReviewedFiles != 2 || report.Coverage.TotalFiles != wantTotal {
		t.Fatalf("coverage = %+v, want UsedOCR=%t note~=%q 2/%d", report.Coverage, wantOCR, wantNote, wantTotal)
	}
}
