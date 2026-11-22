package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	gateTestRoleCoder = "coder"
	gateTestLeadGoal  = "Lead gate"
	gateStatusSuccess = "succeeded"
)

var (
	errGateTestTool        = errors.New("gate tool error")
	errGateTestConcurrency = errors.New("unexpected check concurrency")
)

type gateDiffFixture struct {
	*teamProposalFixture
	teamID  string
	taskID  string
	sessID  string
	binding SessionBinding
	wtDir   string
	repoDir string
	repoID  string
}

func newGateDiffFixture(t *testing.T, writeScope, checks []string, mode string) *gateDiffFixture {
	t.Helper()
	f := newTeamFixture(t, "", false)
	initRepo(t, f.root)
	repoDir, err := filepath.EvalSymlinks(f.root)
	if err != nil {
		t.Fatal(err)
	}
	teamID := strings.ReplaceAll("team-gate-"+t.Name(), "/", "-")
	if len(teamID) > 40 {
		teamID = teamID[:40]
	}
	const leadID, memID, memSess = "t-lead", "t-mem", "sess-gate-mem"
	seedGateTeamAndSession(t, f, teamID, leadID, memID, memSess, mode, writeScope, checks)
	wt := createGateWorktree(t, f, teamID, repoDir, memID)
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

func seedGateTeamAndSession(
	t *testing.T,
	f *teamProposalFixture,
	teamID, leadID, memID, memSess, mode string,
	writeScope, checks []string,
) {
	t.Helper()
	ctx := t.Context()
	roleBytes := mustMarshalJSON(t, teamDecisionPayload{
		Decision: decisionApprove,
		Roles:    []decidedRole{{Name: gateTestRoleCoder, Vendor: VendorClaude, Permission: string(teams.PermissionWrite)}},
	})
	propBytes := mustMarshalJSON(t, teams.ProposalInput{
		Title: "T", Brief: "Shared brief.",
		Roles: []teams.ProposedRole{{Name: gateTestRoleCoder, Vendor: VendorClaude, Permission: teams.PermissionWrite}},
	})
	specDefault := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: gateTestLeadGoal})
	specPayload := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "gate test", WriteScope: writeScope, Checks: checks})
	if err := f.hp.vendorStore.CreateSession(ctx, &vendorstore.Session{
		ID: memSess, Vendor: VendorClaude, AccountID: vendorDefaultAccount,
		Role: string(vendorstore.RoleMember), State: vendorstore.StateReady, ApprovalMode: mode,
		TeamID: teamID, TaskID: memID, WorkspaceID: f.wsID, WorkspaceRoot: teamTestRootName,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.rt.transcripts.RegisterSession(memSess); err != nil {
		t.Fatal(err)
	}
	rec, _, err := f.hp.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: f.wsID, SessionID: f.session.ID,
		ProposedEventID: "prop-ev", ProposedPayload: propBytes,
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: leadID, HierarchyRole: vendorstore.RoleLead, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c1", ProtectedSpec: specDefault},
			{TaskID: memID, ParentTaskID: leadID, HierarchyRole: vendorstore.RoleMember, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "c2", ProtectedSpec: specPayload},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	seq := rec.LastSeq
	events := []vendorstore.AppendTeamEventInput{
		{EventID: "start-ev", EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes},
		{EventID: "ev-1", TaskID: memID, EventType: vendorstore.EventTaskAssigned, SessionID: memSess},
		{EventID: "ev-2", TaskID: memID, EventType: vendorstore.EventTaskStarted},
	}
	for i := range events {
		var appErr error
		seq, appErr = f.hp.vendorStore.AppendTeamEvent(ctx, teamID, seq, &events[i])
		if appErr != nil {
			t.Fatal(appErr)
		}
	}
}

func createGateWorktree(t *testing.T, f *teamProposalFixture, teamID, repoDir, memID string) string {
	t.Helper()
	ctx := t.Context()
	gm := f.rt.teamTasks.git
	headSHA, err := gm.WorktreeHead(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if branchErr := gm.CreateTeamBranch(ctx, repoDir, teamID, headSHA); branchErr != nil {
		t.Fatal(branchErr)
	}
	teamBranch, err := gitops.TeamBranchName(teamID)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := gm.CreateTaskWorktree(ctx, f.rt.teamTasks.dataDir, repoDir, repoIDFor(repoDir), memID, teamBranch)
	if err != nil {
		t.Fatal(err)
	}
	return wt
}

func (g *gateDiffFixture) writeWorktreeFile(t *testing.T, rel, content string) {
	t.Helper()
	full := filepath.Join(g.wtDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (g *gateDiffFixture) callDone(ctx context.Context) (FugueToolResult, error) {
	return g.tools.Call(ctx, g.binding, taskDoneToolName, []byte(`{"summary":"done","status":"succeeded"}`))
}

func (g *gateDiffFixture) startDoneAsync(t *testing.T) <-chan error {
	t.Helper()
	doneCh := make(chan error, 1)
	supervise.Go(t.Context(), "gate-test-done", func(ctx context.Context) error {
		res, callErr := g.callDone(ctx)
		if callErr == nil && res.IsError {
			callErr = fmt.Errorf("%w: %s", errGateTestTool, res.Text)
		}
		doneCh <- callErr
		return nil
	})
	return doneCh
}

func (g *gateDiffFixture) awaitPendingCard(t *testing.T, needle string, doneCh <-chan error) PendingInteractionView {
	t.Helper()
	var pending PendingInteractionView
	testutil.Spin(t, "interaction card: "+needle, func() bool {
		select {
		case earlyErr := <-doneCh:
			t.Fatalf("callDone returned before card %q: %v", needle, earlyErr)
		default:
		}
		list, _, listErr := g.rt.interactions.ListPending(t.Context(), testAppPrincipal)
		if listErr != nil {
			return false
		}
		for i := range list {
			if strings.Contains(list[i].Summary, needle) {
				pending = list[i]
				return true
			}
		}
		return false
	})
	return pending
}

func (g *gateDiffFixture) answerCard(t *testing.T, cmdID string, pending *PendingInteractionView, outcome interaction.Outcome) {
	t.Helper()
	if _, err := g.rt.interactions.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: cmdID, RequestID: pending.RequestID, Incarnation: pending.Incarnation,
		Outcome: string(outcome), Principal: testAppPrincipal,
	}); err != nil {
		t.Fatalf("Answer(%s): %v", outcome, err)
	}
}

func (g *gateDiffFixture) setEnvTagRule(t *testing.T, match, tag string) {
	t.Helper()
	ctx := t.Context()
	ws, err := g.rt.workspaces.domain.GetWorkspace(ctx, g.wsID)
	if err != nil {
		t.Fatal(err)
	}
	rawCfg := fmt.Appendf(nil, "[[roots]]\nname = %q\npath = %q\naccess = \"rw\"\n\n[policy]\nenv_tags = [{ match = %q, tag = %q }]\n", teamTestRootName, ws.Roots[0].Path, match, tag)
	updated, err := g.rt.workspaces.domain.UpdateWorkspaceConfig(ctx, g.wsID, rawCfg, ws.ConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.rt.workspaces.domain.ConfirmWorkspaceConfig(ctx, g.wsID, updated.ConfigHash); err != nil {
		t.Fatal(err)
	}
}

func (g *gateDiffFixture) taskRecord(t *testing.T) vendorstore.TaskRecord {
	t.Helper()
	_, tasks, err := g.hp.vendorStore.TeamSnapshot(t.Context(), g.teamID)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := findTask(tasks, g.taskID)
	if !ok {
		t.Fatalf("task %s not found", g.taskID)
	}
	return *rec
}

func TestWriteScopeBouncesOnceThenFails(t *testing.T) {
	t.Parallel()
	g := newGateDiffFixture(t, []string{"allowed/**"}, nil, vendorstore.ApprovalAuto)

	g.writeWorktreeFile(t, "outside/bad.txt", "nope\n")
	if res, err := g.callDone(t.Context()); err != nil || res.IsError {
		t.Fatalf("first done = (%+v, %v)", res, err)
	}
	if rec := g.taskRecord(t); rec.State != vendorstore.TaskStateRunning || rec.ScopeBounces != 1 {
		t.Fatalf("after 1st bounce: state=%s bounces=%d, want running/1", rec.State, rec.ScopeBounces)
	}
	rows, err := g.hp.vendorStore.PendingMailboxRows(t.Context(), g.teamID, g.taskID)
	if err != nil || len(rows) != 1 || rows[0].Kind != vendorstore.MailboxKindSend {
		t.Fatalf("mailbox rows after 1st bounce = (%+v, %v), want 1 send row", rows, err)
	}

	g.writeWorktreeFile(t, "outside/bad2.txt", "still nope\n")
	if res, err := g.callDone(t.Context()); err != nil || res.IsError {
		t.Fatalf("second done = (%+v, %v)", res, err)
	}
	if rec := g.taskRecord(t); rec.State != vendorstore.TaskStateFailed || rec.FailureCode != "scope" {
		t.Fatalf("after 2nd offense: state=%s code=%q, want failed/scope", rec.State, rec.FailureCode)
	}

	assertInScopePasses(t)
}

func assertInScopePasses(t *testing.T) {
	t.Helper()
	g2 := newGateDiffFixture(t, []string{"allowed/**"}, nil, vendorstore.ApprovalAuto)
	g2.writeWorktreeFile(t, "allowed/ok.go", "package allowed\n")
	if res, doneErr := g2.callDone(t.Context()); doneErr != nil || res.IsError {
		t.Fatalf("in-scope done = (%+v, %v)", res, doneErr)
	}
	if rec := g2.taskRecord(t); rec.State != vendorstore.TaskStateReview {
		t.Fatalf("in-scope state = %s, want review", rec.State)
	}
}

func TestSecretScanPausesWithoutRevealingTheValue(t *testing.T) {
	t.Parallel()
	g := newGateDiffFixture(t, []string{"**"}, nil, vendorstore.ApprovalAuto)

	syntheticSecret := "ghp_" + "AbCdEfGhIjKlMnOpQrStUvWxYz1234567890"
	g.writeWorktreeFile(t, "config.txt", "token = "+syntheticSecret+"\n")
	doneCh := g.startDoneAsync(t)
	pending := g.awaitPendingCard(t, "secret", doneCh)

	if strings.Contains(pending.Summary, syntheticSecret) || strings.Contains(pending.Detail, syntheticSecret) {
		t.Fatal("secret scan card leaked raw secret value")
	}
	if !strings.Contains(pending.Summary, "config.txt") {
		t.Fatalf("secret scan summary = %q, want file name config.txt", pending.Summary)
	}
	g.answerCard(t, "cmd-secret-deny", &pending, interaction.OutcomeDenied)
	if err := <-doneCh; err != nil {
		t.Fatalf("callDone: %v", err)
	}
	if rec := g.taskRecord(t); rec.State != vendorstore.TaskStateFailed || rec.FailureCode != "secret_detected" {
		t.Fatalf("after secret deny: state=%s code=%q, want failed/secret_detected", rec.State, rec.FailureCode)
	}
}

func TestOversizeDiffNeedsExplicitApproval(t *testing.T) {
	t.Parallel()
	g := newGateDiffFixture(t, []string{"**"}, nil, vendorstore.ApprovalAuto)

	var big strings.Builder
	for i := range 5050 {
		fmt.Fprintf(&big, "line %d\n", i)
	}
	g.writeWorktreeFile(t, "big.txt", big.String())
	doneCh := g.startDoneAsync(t)
	pending := g.awaitPendingCard(t, "large", doneCh)
	g.answerCard(t, "cmd-oversize-allow", &pending, interaction.OutcomeAllowedOnce)
	if err := <-doneCh; err != nil {
		t.Fatalf("callDone: %v", err)
	}
	if rec := g.taskRecord(t); rec.State != vendorstore.TaskStateReview {
		t.Fatalf("after oversize approve: state=%s, want review", rec.State)
	}
}

func TestChecksRunThroughPolicyAndHeavySemaphore(t *testing.T) {
	t.Parallel()
	g := newGateDiffFixture(t, []string{"**"}, []string{"deploy prod-api", "echo ok"}, vendorstore.ApprovalFull)
	g.setEnvTagRule(t, "prod-*", "prod")

	gate, ok := g.rt.teamTasks.resultGate.(*hostResultGate)
	if !ok {
		t.Fatal("resultGate is not *hostResultGate")
	}
	gate.heavy = newHeavySemaphore(&fakeHostResources{ramBytes: 16 << 30, cores: 4}, nil, g.clk, nil, g.rt.goTask)
	var activeChecks atomic.Int32
	gate.runCheck = func(_ context.Context, _, command string) (string, int, error) {
		if n := activeChecks.Add(1); n < 1 {
			return "", 1, fmt.Errorf("%w: %d", errGateTestConcurrency, n)
		}
		defer activeChecks.Add(-1)
		return "ran: " + command, 0, nil
	}

	g.writeWorktreeFile(t, "ok.txt", "ok\n")
	doneCh := g.startDoneAsync(t)
	pending := g.awaitPendingCard(t, "deploy prod-api", doneCh)
	if slices.ContainsFunc(pending.Options, func(o InteractionOption) bool { return o.ID == string(interaction.OutcomeAllowedSession) }) {
		t.Fatalf("prod check offered allow_session, want allow_once only: %+v", pending.Options)
	}
	g.answerCard(t, "cmd-prod-allow", &pending, interaction.OutcomeAllowedOnce)
	if err := <-doneCh; err != nil {
		t.Fatalf("callDone: %v", err)
	}
	if rec := g.taskRecord(t); rec.State != vendorstore.TaskStateReview {
		t.Fatalf("after allowed checks: state=%s, want review", rec.State)
	}
	assertRecordedCheckToolCalls(t, g, 2)

	// Deny stops execution and ends task failed(checks_failed).
	gDeny := newGateDiffFixture(t, []string{"**"}, []string{"deploy unknown-target"}, vendorstore.ApprovalFull)
	gDeny.setEnvTagRule(t, "unknown-*", "mystery")
	gDeny.writeWorktreeFile(t, "x.txt", "x\n")
	denyCh := gDeny.startDoneAsync(t)
	denyPending := gDeny.awaitPendingCard(t, "deploy unknown-target", denyCh)
	gDeny.answerCard(t, "cmd-unknown-deny", &denyPending, interaction.OutcomeDenied)
	if err := <-denyCh; err != nil {
		t.Fatalf("deny callDone: %v", err)
	}
	if rec := gDeny.taskRecord(t); rec.State != vendorstore.TaskStateFailed || rec.FailureCode != "checks_failed" {
		t.Fatalf("after denied check: state=%s code=%q, want failed/checks_failed", rec.State, rec.FailureCode)
	}
}

func assertRecordedCheckToolCalls(t *testing.T, g *gateDiffFixture, want int) {
	t.Helper()
	entries, err := g.rt.transcripts.TranscriptPage(t.Context(), TranscriptPageParams{Session: g.sessID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var got int
	for i := range entries {
		if entries[i].Kind == transcript.ToolCall && entries[i].Metadata.Tool == checkToolName && entries[i].Metadata.Status == gateStatusSuccess {
			got++
		}
	}
	if got != want {
		t.Fatalf("recorded succeeded check tool_call items = %d, want %d", got, want)
	}
}

func TestWriteScopeEdgeCasesAndUnconfiguredGate(t *testing.T) {
	t.Parallel()
	if v, err := (unconfiguredResultGate{}).Evaluate(t.Context(), "", ""); err != nil || v.Ready {
		t.Fatalf("unconfiguredResultGate.Evaluate = (%+v, %v), want Ready=false, nil", v, err)
	}
	assertScopePathAndPatternHelpers(t)

	g := newGateDiffFixture(t, []string{}, nil, vendorstore.ApprovalAuto)
	g.writeWorktreeFile(t, "any.txt", "changed\n")
	if res, err := g.callDone(t.Context()); err != nil || res.IsError {
		t.Fatalf("empty-scope first done = (%+v, %v)", res, err)
	}
	if rec := g.taskRecord(t); rec.State != vendorstore.TaskStateRunning || rec.ScopeBounces != 1 {
		t.Fatalf("empty-scope 1st bounce: state=%s bounces=%d, want running/1", rec.State, rec.ScopeBounces)
	}
	if res, err := g.callDone(t.Context()); err != nil || res.IsError {
		t.Fatalf("empty-scope second done = (%+v, %v)", res, err)
	}
	if rec := g.taskRecord(t); rec.State != vendorstore.TaskStateFailed || rec.FailureCode != "scope" {
		t.Fatalf("empty-scope 2nd offense: state=%s code=%q, want failed/scope", rec.State, rec.FailureCode)
	}
}

func assertScopePathAndPatternHelpers(t *testing.T) {
	t.Helper()
	const sampleFile = "a/b.go"
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"./" + sampleFile, sampleFile},
		{"../escape", ""},
		{".", ""},
		{"/abs/path", ""},
	} {
		if got := normalizeScopePath(tc.raw); got != tc.want {
			t.Errorf("normalizeScopePath(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	for _, tc := range []struct {
		file, pat string
		want      bool
	}{
		{sampleFile, "", false},
		{sampleFile, ".", true},
		{sampleFile, "a/**", true},
		{sampleFile, "a/", true},
		{"pkg/sub/f.go", "pkg", true},
		{"other/f.go", "pkg", false},
		{"top.go", "*.go", true},
	} {
		if got := matchWriteScopePattern(tc.file, tc.pat); got != tc.want {
			t.Errorf("matchWriteScopePattern(%q, %q) = %v, want %v", tc.file, tc.pat, got, tc.want)
		}
	}
}
