package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// fakeTaskSessions is the vendor layer of the dispatcher tests: it records
// what the dispatcher starts and sends, and starts nothing.
type fakeTaskSessions struct {
	mu         sync.Mutex
	starts     []taskSessionRequest
	sends      map[string][]string
	resumes    []fakeResume
	interrupts []string
	closed     []string
	noRoom     bool
	reserveErr error
	startErr   error
	resumeErr  error
	// sendErr fails every send after the text was recorded; holdSend blocks
	// the send inside the fake, so a test can hold the pump mid-send and
	// observe what a concurrent trigger does.
	sendErr     error
	holdSend    <-chan struct{}
	sendStarted chan struct{}
	release     <-chan struct{}
}

// fakeResume is one recorded wake resume of a detached session (Q-123).
type fakeResume struct {
	SessionID, Cwd string
}

func (f *fakeTaskSessions) HasCapacity(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.noRoom, nil
}

func (f *fakeTaskSessions) HasCapacityFor(ctx context.Context, _, _ string) (bool, error) {
	return f.HasCapacity(ctx)
}

func (f *fakeTaskSessions) ReserveSlot(context.Context, string, string, string) (func(), error) {
	f.mu.Lock()
	noRoom, reserveErr := f.noRoom, f.reserveErr
	f.mu.Unlock()
	if reserveErr != nil {
		return nil, reserveErr
	}
	if noRoom {
		return nil, ErrVendorSessionCapacity
	}
	return func() {}, nil
}

func (f *fakeTaskSessions) StartTaskSession(_ context.Context, req *taskSessionRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.starts = append(f.starts, *req)
	return nil
}

func (f *fakeTaskSessions) Send(_ context.Context, sessionID string, input vendor.Input) error {
	f.mu.Lock()
	if f.sends == nil {
		f.sends = make(map[string][]string)
	}
	f.sends[sessionID] = append(f.sends[sessionID], input.Text)
	err, hold, started, release := f.sendErr, f.holdSend, f.sendStarted, f.release
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if hold != nil {
		<-hold
	}
	if release != nil {
		<-release
	}
	return err
}

func (f *fakeTaskSessions) SendAsync(ctx context.Context, sessionID string, input vendor.Input, onDone func(error)) error {
	err := f.Send(ctx, sessionID, input)
	if onDone != nil {
		onDone(err)
	}
	return nil
}

func (f *fakeTaskSessions) Close(_ context.Context, sessionID string, _ vendor.CloseReason) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, sessionID)
	return nil
}

func (f *fakeTaskSessions) ResumeTaskSession(_ context.Context, sessionID, cwd string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, fakeResume{SessionID: sessionID, Cwd: cwd})
	return f.resumeErr
}

func (f *fakeTaskSessions) Interrupt(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupts = append(f.interrupts, sessionID)
	return nil
}

// resumeCount reports how many wake resumes the fake recorded.
func (f *fakeTaskSessions) resumeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.resumes)
}

func (f *fakeTaskSessions) setNoRoom(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noRoom = v
}

func (f *fakeTaskSessions) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func (f *fakeTaskSessions) startFor(taskID string) (taskSessionRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.starts {
		if f.starts[i].TaskID == taskID {
			return f.starts[i], true
		}
	}
	return taskSessionRequest{}, false
}

// dispatchFixture is a team fixture with the host's own start path off and a
// dispatcher of the test's over a fake vendor layer, a real store and a real
// git manager.
type dispatchFixture struct {
	*teamProposalFixture
	sessions *fakeTaskSessions
	disp     *teamDispatcher
	dataDir  string
}

// gitBinary resolves the host git for the worktree tests.
func gitBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git unavailable: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// initRepo makes dir a git repository with one commit.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "."},
		{"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.CommandContext(t.Context(), "git")
		cmd.Path = gitBinary(t)
		cmd.Args = append([]string{"git"}, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func newDispatchFixture(t *testing.T, withGit bool) *dispatchFixture {
	t.Helper()
	f := newTeamProposalFixture(t, "")
	if withGit {
		initRepo(t, f.root)
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	manager, err := gitops.NewManager(gitBinary(t), dataDir, clock.NewFake(time.Unix(300, 0)))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTaskSessions{}
	disp := newTeamDispatcher(&teamDispatcherDeps{
		Store: f.hp.vendorStore, Sessions: fake, Admit: localAdmitter{capacity: fake},
		Roots: f.rt.workspaces.domain, Git: manager, DataDir: dataDir, IDs: ids.NewSequence("disp"), Vault: f.hp.vault,
	})
	return &dispatchFixture{teamProposalFixture: f, sessions: fake, disp: disp, dataDir: dataDir}
}

// approve proposes args and approves the proposal, returning the team ID.
func (f *dispatchFixture) approve(t *testing.T, args *ProposeArgs, option, text string) string {
	t.Helper()
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, args)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if answerErr := f.answerCard(t, "dispatch-"+teamID, teamProposeToolName, teamDecisionQuestionID, option, text); answerErr != nil {
		t.Fatalf("decide: %v", answerErr)
	}
	return teamID
}

func (f *dispatchFixture) tasks(t *testing.T, teamID string) map[string]vendorstore.TaskRecord {
	t.Helper()
	_, list := f.teamSnapshot(t, teamID)
	byID := make(map[string]vendorstore.TaskRecord, len(list))
	for i := range list {
		byID[list[i].TaskID] = list[i]
	}
	return byID
}

func memberTasksArgs(n int) *ProposeArgs {
	args := validTeamProposalArgs()
	args.Tasks = args.Tasks[:1]
	for i := range n {
		args.Tasks = append(args.Tasks, teams.ProposedTask{
			ID: "m" + string(rune('a'+i)), ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole,
			Goal: "Read part " + string(rune('a'+i)), WriteScope: []string{}, Deliverable: teams.DeliverableReport,
		})
	}
	return args
}

// TestApprovedTaskStartsAVendorSessionInItsOwnWorktree pins the start path:
// each child of the lead gets a session whose working directory is its own
// worktree under the data directory, and receives its intro as one turn.
func TestApprovedTaskStartsAVendorSessionInItsOwnWorktree(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, true)
	teamID := f.approve(t, validTeamProposalArgs(), decisionApprove, "")
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	tasks := f.tasks(t, teamID)
	for _, key := range []string{teamTestTaskA, teamTestTaskB} {
		f.assertStartedChild(t, teamID, teamID+"."+key, tasks)
	}
}

func assertDirExists(t *testing.T, dir string) {
	t.Helper()
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("worktree %q missing: %v", dir, err)
	}
}

// assertStartedChild asserts one child runs in its worktree session and got
// its intro as one turn.
func (f *dispatchFixture) assertStartedChild(t *testing.T, teamID, id string, tasks map[string]vendorstore.TaskRecord) {
	t.Helper()
	req, ok := f.sessions.startFor(id)
	if !ok {
		t.Fatalf("task %s was not started", id)
	}
	if !strings.HasPrefix(req.Cwd, filepath.Join(f.dataDir, "worktrees")+string(filepath.Separator)) || !strings.HasSuffix(req.Cwd, id) {
		t.Fatalf("cwd = %q, want a worktree of %s under the data directory", req.Cwd, id)
	}
	assertDirExists(t, req.Cwd)
	if req.Role != vendorstore.RoleMember || req.TeamID != teamID || req.Vendor != VendorClaude || req.WorkspaceID != f.wsID {
		t.Fatalf("session request = %+v", req)
	}
	if task := tasks[id]; task.State != vendorstore.TaskStateRunning || task.SessionID != req.SessionID {
		t.Fatalf("task %s = %s session %q, want running in %q", id, task.State, task.SessionID, req.SessionID)
	}
	f.assertIntro(t, id, req.SessionID)
}

func (f *dispatchFixture) assertIntro(t *testing.T, id, sessionID string) {
	t.Helper()
	if intro := f.sessions.sends[sessionID]; len(intro) != 1 || !strings.Contains(intro[0], id) || !strings.Contains(intro[0], "Read the") {
		t.Fatalf("intro for %s = %q", id, intro)
	}
}

// TestStartedTaskUsesTheApprovedOrEditedModel pins OR-E13's model half on
// the start path: the model the user edited on the card is the model of the
// started session, over the task's own override.
func TestStartedTaskUsesTheApprovedOrEditedModel(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, true)
	args := validTeamProposalArgs()
	args.Tasks[1].Profile = &teams.ProfileOverride{Vendor: teams.VendorClaude, Model: "orig-model"}
	edit, err := json.Marshal(teams.EditSet{Version: teams.EditSetVersion, Roles: []teams.RoleEdit{{Name: teamTestRole, Model: teamTestModelEdited}}})
	if err != nil {
		t.Fatal(err)
	}
	teamID := f.approve(t, args, decisionApproveEdited, string(edit))
	if dispatchErr := f.disp.Dispatch(t.Context(), teamID); dispatchErr != nil {
		t.Fatalf("Dispatch: %v", dispatchErr)
	}
	for _, key := range []string{teamTestTaskA, teamTestTaskB} {
		req, ok := f.sessions.startFor(teamID + "." + key)
		if !ok || req.Model != teamTestModelEdited {
			t.Fatalf("task %s model = %q (started %v), want %q", key, req.Model, ok, teamTestModelEdited)
		}
	}
}

// TestLeadTaskBindsTheProposingSessionAndCreatesNoWorktree pins Q-117: the
// lead task runs in the session that proposed, which spawns nothing.
func TestLeadTaskBindsTheProposingSessionAndCreatesNoWorktree(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, true)
	teamID := f.approve(t, validTeamProposalArgs(), decisionApprove, "")
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	lead := f.tasks(t, teamID)[teamID+"."+teamTestLeadTask]
	if lead.State != vendorstore.TaskStateRunning || lead.SessionID != f.session.ID {
		t.Fatalf("lead task = %s session %q, want running in %q", lead.State, lead.SessionID, f.session.ID)
	}
	if _, started := f.sessions.startFor(lead.TaskID); started {
		t.Fatal("the lead task spawned a session")
	}
	role, err := sessionTeamRole(t.Context(), f.hp.vendorStore, f.session.ID)
	if err != nil || role != vendorstore.RoleLead {
		t.Fatalf("role of the proposing session = %q, %v; want lead from the store", role, err)
	}
	entries, err := os.ReadDir(filepath.Join(f.dataDir, "worktrees"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("worktree repositories = %v, %v; want the one repository of the children", entries, err)
	}
	trees, err := os.ReadDir(filepath.Join(f.dataDir, "worktrees", entries[0].Name()))
	if err != nil || len(trees) != 2 {
		t.Fatalf("worktrees = %v, %v; want one per child and none for the lead", trees, err)
	}
}

// TestStartFailsClosedWhileTheVaultIsLocked pins that a locked vault leaves
// every task queued and records nothing.
func TestStartFailsClosedWhileTheVaultIsLocked(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, true)
	teamID := f.approve(t, validTeamProposalArgs(), decisionApprove, "")
	before, _ := f.teamSnapshot(t, teamID)
	f.hp.vault.Lock()
	if err := f.disp.Dispatch(t.Context(), teamID); errcode.CodeOf(err) != errcode.VendorVaultLocked {
		t.Fatalf("Dispatch error = %v, want vault locked", err)
	}
	after, tasks := f.teamSnapshot(t, teamID)
	if after.LastSeq != before.LastSeq || f.sessions.startCount() != 0 {
		t.Fatalf("a locked vault recorded events (%d → %d) or started %d sessions", before.LastSeq, after.LastSeq, f.sessions.startCount())
	}
	for i := range tasks {
		if tasks[i].State != vendorstore.TaskStateQueued {
			t.Fatalf("task %s = %s, want queued", tasks[i].TaskID, tasks[i].State)
		}
	}
}

// TestStartOnANonGitRootFailsTheTaskWithATypedCode pins Q-119: a root that
// is not a git repository fails the child closed, never runs it unisolated.
func TestStartOnANonGitRootFailsTheTaskWithATypedCode(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, false)
	teamID := f.approve(t, validTeamProposalArgs(), decisionApprove, "")
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	tasks := f.tasks(t, teamID)
	for _, key := range []string{teamTestTaskA, teamTestTaskB} {
		if task := tasks[teamID+"."+key]; task.State != vendorstore.TaskStateFailed || task.FailureCode != failWorktree {
			t.Fatalf("task %s = %s (%q), want failed(%s)", key, task.State, task.FailureCode, failWorktree)
		}
	}
	if f.sessions.startCount() != 0 {
		t.Fatalf("%d sessions started without a worktree", f.sessions.startCount())
	}
}

// TestStartNeverReplaysATaskWhoseStartOutcomeIsUnknown pins OR-15 on the
// start path: a task assigned when the host died is unknown after recovery
// and the dispatcher never starts it again.
func TestStartNeverReplaysATaskWhoseStartOutcomeIsUnknown(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, true)
	teamID := f.approve(t, validTeamProposalArgs(), decisionApprove, "")
	taskA := teamID + "." + teamTestTaskA
	rec, _ := f.teamSnapshot(t, teamID)
	for i, ev := range []*vendorstore.AppendTeamEventInput{
		{EventID: "crash-1", TaskID: taskA, EventType: vendorstore.EventTaskAssigned, SessionID: "crashed-session"},
	} {
		next, err := f.hp.vendorStore.AppendTeamEvent(t.Context(), teamID, rec.LastSeq+int64(i), ev)
		if err != nil || next != rec.LastSeq+int64(i)+1 {
			t.Fatalf("append %s: %v", ev.EventID, err)
		}
	}
	if _, err := f.hp.vendorStore.RecoverTeams(t.Context()); err != nil {
		t.Fatalf("RecoverTeams: %v", err)
	}
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := f.tasks(t, teamID)[taskA].State; got != vendorstore.TaskStateUnknown {
		t.Fatalf("task A = %s, want unknown", got)
	}
	if _, restarted := f.sessions.startFor(taskA); restarted || f.sessions.startCount() != 1 {
		t.Fatalf("starts = %d (A restarted: %v), want only task B", f.sessions.startCount(), restarted)
	}
}

// TestDispatcherHonoursTheDirectChildLimit pins Q-118: a delegator has at
// most four direct children working; the rest wait queued until one ends.
func TestDispatcherHonoursTheDirectChildLimit(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, true)
	teamID := f.approve(t, memberTasksArgs(6), decisionApprove, "")
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := f.sessions.startCount(); got != maxRunningDirectChildren {
		t.Fatalf("started = %d, want %d", got, maxRunningDirectChildren)
	}
	first := f.sessions.starts[0]
	rec, _ := f.teamSnapshot(t, teamID)
	if _, err := f.hp.vendorStore.AppendTeamEvent(t.Context(), teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "limit-end", TaskID: first.TaskID, EventType: vendorstore.EventTaskEnded, Outcome: vendorstore.TaskOutcomeFailed, FailureCode: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch after an end: %v", err)
	}
	if got := f.sessions.startCount(); got != maxRunningDirectChildren+1 {
		t.Fatalf("started after one ended = %d, want %d", got, maxRunningDirectChildren+1)
	}
}

// TestDispatcherHoldsTasksWhileTheHostHasNoCapacity pins that a task the
// admitter refuses stays queued, not failed, and starts on the next trigger.
func TestDispatcherHoldsTasksWhileTheHostHasNoCapacity(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, true)
	f.sessions.setNoRoom(true)
	teamID := f.approve(t, validTeamProposalArgs(), decisionApprove, "")
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if queued := f.queued(t, teamID); len(queued) != 2 || f.sessions.startCount() != 0 {
		t.Fatalf("queued = %v with %d starts, want both members held", queued, f.sessions.startCount())
	}
	f.sessions.setNoRoom(false)
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch with room: %v", err)
	}
	if f.sessions.startCount() != 2 {
		t.Fatalf("starts = %d, want 2", f.sessions.startCount())
	}
}

func (f *dispatchFixture) queued(t *testing.T, teamID string) []string {
	t.Helper()
	var out []string
	all := f.tasks(t, teamID)
	for id := range all {
		if all[id].State == vendorstore.TaskStateQueued {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// TestTaskWaitsForItsDependenciesAndAnAmendedTaskForItsDelegator pins the
// eligibility rule (Q-116): a dependency must be done first, and a task an
// amend inserted is never started by the dispatcher.
func TestTaskWaitsForItsDependenciesAndAnAmendedTaskForItsDelegator(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, true)
	args := validTeamProposalArgs()
	args.Tasks[2].DependsOn = []string{teamTestTaskA}
	teamID := f.approve(t, args, decisionApprove, "")
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, started := f.sessions.startFor(teamID + "." + teamTestTaskB); started || f.sessions.startCount() != 1 {
		t.Fatalf("starts = %d (B started: %v), want only A", f.sessions.startCount(), started)
	}
	held := []vendorstore.TaskRecord{
		{TaskID: "parent", State: vendorstore.TaskStateRunning},
		{TaskID: "child", ParentTaskID: "parent", State: vendorstore.TaskStateQueued, ExplicitStart: true},
	}
	if next := nextStartable(held, map[string]bool{}); next != nil {
		t.Fatalf("an amended task is startable: %+v", next)
	}
}

// TestTaskSessionTokenIsMintedForTheTaskAndNothingElse runs the real vendor
// layer: the member session records its team and task, its token carries the
// member role, and the member never sees fugue_team_propose (§6.6, Q-117).
// The host's two-session cap holds the second member queued (Q-118).
func TestTaskSessionTokenIsMintedForTheTaskAndNothingElse(t *testing.T) {
	t.Parallel()
	f := newTeamFixture(t, "", false)
	initRepo(t, f.root)
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if answerErr := f.answerCard(t, "real-start", teamProposeToolName, teamDecisionQuestionID, decisionApprove, ""); answerErr != nil {
		t.Fatalf("approve: %v", answerErr)
	}
	// The stage A host runs two sessions at once (vendor-runtime §13): the
	// lead's and one member's. The other member waits queued, not failed.
	testutil.Poll(t, "one member running", func() bool {
		_, tasks := f.teamSnapshot(t, teamID)
		running := 0
		for i := range tasks {
			if tasks[i].HierarchyRole == vendorstore.RoleMember && tasks[i].State == vendorstore.TaskStateRunning && tasks[i].SessionID != "" {
				running++
			}
		}
		return running == 1
	})
	_, tasks := f.teamSnapshot(t, teamID)
	for i := range tasks {
		if tasks[i].HierarchyRole == vendorstore.RoleMember && tasks[i].State == vendorstore.TaskStateRunning {
			f.assertMemberSession(t, teamID, &tasks[i])
		}
	}
	if names := teamToolNames(t, f.tools, f.binding); !slices.Contains(names, teamProposeToolName) {
		t.Fatalf("the lead is offered %v, want fugue_team_propose kept", names)
	}
}

// assertMemberSession asserts a running member's session record, token role
// and tool surface.
func (f *teamProposalFixture) assertMemberSession(t *testing.T, teamID string, task *vendorstore.TaskRecord) {
	t.Helper()
	info, err := f.hp.vendorStore.Snapshot(t.Context(), task.SessionID)
	if err != nil || info.TeamID != teamID || info.TaskID != task.TaskID || info.Role != string(vendorstore.RoleMember) || info.ApprovalMode != vendorstore.ApprovalPlan {
		t.Fatalf("member session = %+v, %v", info, err)
	}
	binding, ok := f.tools.tokens.bindingFor(task.SessionID)
	if !ok || binding.Role != string(vendorstore.RoleMember) {
		t.Fatalf("member token = %+v (live %v), want the member role", binding, ok)
	}
	if names := teamToolNames(t, f.tools, binding); slices.Contains(names, teamProposeToolName) {
		t.Fatalf("a member is offered %v", names)
	}
}
