package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/pkg/client"
)

// restartedTeam is a real host restarted after the first run died with a
// member task running: the member is unknown and the vault is still locked.
type restartedTeam struct {
	c       *client.Client
	done    <-chan error
	dataDir string
	lead    client.Session
	teamID  string
	member  client.TaskNode
}

// crashWithRunningMember runs a host until its member task is running, kills
// it there (the crash point is the real process exit) and starts the next
// host on the same data directory.
func crashWithRunningMember(t *testing.T) (*restartedTeam, context.Context) {
	t.Helper()
	dataDir, fugueHome := crashTestDirs(t)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	ctx1, cancel1 := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel1()
	done1 := startCrashSubprocess(t, ctx1, dataDir, fugueHome, config.CrashPointTeamTaskRunning)
	c1 := dialCrashClient(t, ctx1, dataDir, done1)
	defer c1.Close()
	if _, err := c1.UnlockVault(ctx1, []byte(cheapvault.MasterPassword)); err != nil {
		t.Fatalf("UnlockVault: %v", err)
	}
	ws := setupCrashTeamWorkspace(t, ctx1, c1, repoDir)
	lead, err := c1.StartSession(ctx1, "failux-lead", client.VendorClaude, ws.ID, failureUXRepoRoot, client.SessionProfile{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sendErr := c1.Send(ctx1, "failux-propose", lead.ID, "CRASH_TEST_PROPOSE_TEAM"); sendErr != nil {
		t.Fatalf("Send: %v", sendErr)
	}
	proposal := awaitPendingProposalCard(t, ctx1, c1)
	if aErr := tryAnswerFailureUXCard(ctx1, c1, &proposal, "approve"); aErr != nil {
		t.Logf("approval returned (the host dies on approval): %v", aErr)
	}
	awaitCrashExit(t, ctx1, done1)

	ctx2 := failureUXCtx(t)
	done2 := startCrashSubprocess(t, ctx2, dataDir, fugueHome, "")
	c2 := dialCrashClient(t, ctx2, dataDir, done2)
	t.Cleanup(c2.Close)
	return &restartedTeam{c: c2, done: done2, dataDir: dataDir, lead: lead}, ctx2
}

// awaitUnknownMember waits, vault still locked, for the restarted host to
// have marked the member task unknown and returns it.
func (r *restartedTeam) awaitUnknownMember(t *testing.T, ctx context.Context) {
	t.Helper()
	r.teamID = awaitFailureUXTeam(t, ctx, r.c)
	testutil.Spin(t, "member task unknown after the restart", func() bool {
		tv, members := failureUXMembers(t, ctx, r.c, r.teamID)
		if len(members) != 1 || members[0].State != "unknown" {
			return false
		}
		r.member = members[0]
		return tv.VaultLocked
	})
}

// dirtyMemberWorktree leaves one uncommitted file in the only task worktree.
func (r *restartedTeam) dirtyMemberWorktree(t *testing.T) {
	t.Helper()
	trees, err := filepath.Glob(filepath.Join(r.dataDir, "worktrees", "*", "*"))
	if err != nil || len(trees) != 1 {
		t.Fatalf("task worktrees = (%v, %v), want exactly one", trees, err)
	}
	if wErr := os.WriteFile(filepath.Join(trees[0], "wip.txt"), []byte("work in progress\n"), 0o600); wErr != nil {
		t.Fatal(wErr)
	}
}

// TestFailureUXUnknownTaskNoticeAndRecoveryDecisionsOverARealHost is the
// "unknown task" half of the activation §3 "unknown task or result gate
// refusal" row (A-E05): after a real host restart the running member is
// unknown and not rerun, its delegator gets the notice (worktree state and the
// choices) once the vault opens, and each user decision (resume, keep_diff,
// cancel) works through RecoverTask.
func TestFailureUXUnknownTaskNoticeAndRecoveryDecisionsOverARealHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ decision, wantState string }{
		{decision: "resume", wantState: failureUXTaskRunning},
		{decision: "keep_diff", wantState: "review"},
		{decision: "cancel", wantState: "cancelled"},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			t.Parallel()
			runUnknownTaskDecision(t, tc.decision, tc.wantState)
		})
	}
}

func runUnknownTaskDecision(t *testing.T, decision, wantState string) {
	t.Helper()
	r, ctx := crashWithRunningMember(t)
	r.awaitUnknownMember(t, ctx)
	r.dirtyMemberWorktree(t)
	if _, err := r.c.UnlockVault(ctx, []byte(cheapvault.MasterPassword)); err != nil {
		t.Fatalf("UnlockVault: %v", err)
	}

	// The wake pump resumes the detached lead session, the delegator, and
	// delivers the notice to it.
	notice := awaitFailureUXText(t, ctx, r.c, r.lead.ID, failureUXUser, "fugue_task_recover")
	for _, want := range []string{r.member.TaskID, "head=", "status=dirty (1 changed)", "fugue_task_recover(task_id, resume|keep_diff|cancel)"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("delegator notice = %q, want %q", notice, want)
		}
	}
	if _, members := failureUXMembers(t, ctx, r.c, r.teamID); members[0].State != "unknown" {
		t.Fatalf("member before any decision = %q, want unknown (never rerun automatically)", members[0].State)
	}

	node, err := r.c.RecoverTask(ctx, &client.RecoverTaskInput{
		CommandID: "failux-recover", TeamID: r.teamID, TaskID: r.member.TaskID, Decision: decision, Reason: "failure ux",
	})
	if err != nil || node.State != wantState {
		t.Fatalf("RecoverTask(%s) = (%+v, %v), want state %q", decision, node, err, wantState)
	}
	assertRecoveredMember(t, ctx, r, decision)
	awaitCleanExit(t, ctx, r.c, r.done)
}

func assertRecoveredMember(t *testing.T, ctx context.Context, r *restartedTeam, decision string) {
	t.Helper()
	if decision == "keep_diff" {
		tv, err := r.c.GetTeam(ctx, &client.GetTeamInput{TeamID: r.teamID, DiffTaskID: r.member.TaskID})
		if err != nil || !strings.Contains(tv.DiffText, "wip.txt") {
			t.Fatalf("kept diff = (%q, %v), want it to contain wip.txt", tv.DiffText, err)
		}
	}
	// The decision is final: a second one is refused with the typed code.
	_, err := r.c.RecoverTask(ctx, &client.RecoverTaskInput{
		CommandID: "failux-recover-again", TeamID: r.teamID, TaskID: r.member.TaskID, Decision: "cancel",
	})
	if code, ok := client.TasksCodeOf(err); !ok || code != string(errcode.TasksTaskNotUnknown) {
		t.Fatalf("second RecoverTask = %v (%q), want %s (%v)", err, code, errcode.TasksTaskNotUnknown, connect.CodeOf(err))
	}
}
