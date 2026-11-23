package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/pkg/client"
)

func TestCrashPointVendorTurnActiveRecovery(t *testing.T) {
	t.Parallel()
	dataDir, fugueHome := crashTestDirs(t)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	ctx1, cancel1 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel1()
	done1 := startCrashSubprocess(t, ctx1, dataDir, fugueHome, config.CrashPointVendorTurnActive)
	c1 := dialCrashClient(t, ctx1, dataDir, done1)
	defer c1.Close()
	if _, err := c1.UnlockVault(ctx1, []byte(cheapvault.MasterPassword)); err != nil {
		t.Fatalf("UnlockVault: %v", err)
	}
	ws := createWorkspaceAndAddRoot(t, ctx1, c1, "crash-vta", "repo", repoDir)
	sess, err := c1.StartSession(ctx1, "cmd-vta-start", client.VendorClaude, ws.ID, "repo", client.SessionProfile{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sendErr := c1.Send(ctx1, "cmd-vta-send", sess.ID, "hello crash"); sendErr == nil {
		t.Log("Send completed before crash exit")
	}
	awaitCrashExit(t, ctx1, done1)

	ctx2, cancel2 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel2()
	done2 := startCrashSubprocess(t, ctx2, dataDir, fugueHome, "")
	c2 := dialCrashClient(t, ctx2, dataDir, done2)
	listed, err := c2.ListSessions(ctx2)
	if err != nil {
		t.Fatalf("ListSessions after restart: %v", err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].ID != sess.ID || listed.Sessions[0].State != string(vendorstore.StateDetached) {
		t.Fatalf("restarted sessions = %+v, want %s in state detached", listed.Sessions, sess.ID)
	}
	if _, uErr := c2.UnlockVault(ctx2, []byte(cheapvault.MasterPassword)); uErr != nil {
		t.Fatalf("UnlockVault after restart: %v", uErr)
	}
	page, err := c2.TranscriptPage(ctx2, sess.ID, 0, 20)
	if err != nil {
		t.Fatalf("TranscriptPage after restart: %v", err)
	}
	if len(page) == 0 {
		t.Fatal("expected durable first transcript item after restart")
	}
	awaitCleanExit(t, ctx2, c2, done2)
	assertTurnOutcomeUnknown(t, dataDir, sess.ID)
}

func assertTurnOutcomeUnknown(t *testing.T, dataDir, sessionID string) {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(time.Unix(0, 0))
	gen := ids.NewUUIDv7(clk, ids.CryptoRandom{})
	st, err := storage.OpenStore(ctx, config.Config{DataDir: dataDir}, clk, gen)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() {
		if cErr := st.Close(); cErr != nil {
			t.Errorf("close store: %v", cErr)
		}
	}()
	vs, err := vendorstore.New(ctx, st.DB(), nil, clk, ids.CryptoRandom{})
	if err != nil {
		t.Fatalf("vendorstore.New: %v", err)
	}
	turns, err := vs.SessionTurns(ctx, sessionID)
	if err != nil {
		t.Fatalf("SessionTurns: %v", err)
	}
	if len(turns) != 1 || turns[0].Outcome != vendorstore.OutcomeUnknown {
		t.Fatalf("turns = %+v, want 1 turn with outcome unknown", turns)
	}
}

func TestCrashPointInteractionCardPendingRecovery(t *testing.T) {
	t.Parallel()
	dataDir, fugueHome := crashTestDirs(t)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	ctx1, cancel1 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel1()
	done1 := startCrashSubprocess(t, ctx1, dataDir, fugueHome, config.CrashPointInteractionCardPending)
	c1 := dialCrashClient(t, ctx1, dataDir, done1)
	defer c1.Close()
	if _, err := c1.UnlockVault(ctx1, []byte(cheapvault.MasterPassword)); err != nil {
		t.Fatalf("UnlockVault: %v", err)
	}
	ws := setupCrashTeamWorkspace(t, ctx1, c1, repoDir)
	sess, err := c1.StartSession(ctx1, "cmd-icp-start", client.VendorClaude, ws.ID, "repo", client.SessionProfile{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sendErr := c1.Send(ctx1, "cmd-icp-send", sess.ID, "CRASH_TEST_PROPOSE_TEAM"); sendErr != nil {
		t.Logf("Send returned: %v", sendErr)
	}
	awaitCrashExit(t, ctx1, done1)

	ctx2, cancel2 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel2()
	done2 := startCrashSubprocess(t, ctx2, dataDir, fugueHome, "")
	c2 := dialCrashClient(t, ctx2, dataDir, done2)
	if _, uErr := c2.UnlockVault(ctx2, []byte(cheapvault.MasterPassword)); uErr != nil {
		t.Fatalf("UnlockVault after restart: %v", uErr)
	}
	_ = awaitPendingProposalCard(t, ctx2, c2)
	awaitCleanExit(t, ctx2, c2, done2)
}

func TestCrashPointTeamTaskRunningRecovery(t *testing.T) {
	t.Parallel()
	dataDir, fugueHome := crashTestDirs(t)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	ctx1, cancel1 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel1()
	done1 := startCrashSubprocess(t, ctx1, dataDir, fugueHome, config.CrashPointTeamTaskRunning)
	c1 := dialCrashClient(t, ctx1, dataDir, done1)
	defer c1.Close()
	if _, err := c1.UnlockVault(ctx1, []byte(cheapvault.MasterPassword)); err != nil {
		t.Fatalf("UnlockVault: %v", err)
	}
	ws := setupCrashTeamWorkspace(t, ctx1, c1, repoDir)
	sess, err := c1.StartSession(ctx1, "cmd-ttr-start", client.VendorClaude, ws.ID, "repo", client.SessionProfile{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sendErr := c1.Send(ctx1, "cmd-ttr-send", sess.ID, "CRASH_TEST_PROPOSE_TEAM"); sendErr != nil {
		t.Fatalf("Send: %v", sendErr)
	}
	req := awaitPendingProposalCard(t, ctx1, c1)
	if _, aErr := c1.AnswerInteraction(ctx1, &client.AnswerInteractionInput{
		CommandID:   "cmd-ttr-approve",
		RequestID:   req.RequestID,
		Incarnation: req.Incarnation,
		Outcome:     "answered",
		Answers:     []client.InteractionQuestionAnswer{{QuestionID: "decision", OptionIDs: []string{"approve"}}},
	}); aErr != nil {
		t.Logf("AnswerInteraction returned: %v", aErr)
	}
	awaitCrashExit(t, ctx1, done1)

	ctx2, cancel2 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel2()
	done2 := startCrashSubprocess(t, ctx2, dataDir, fugueHome, "")
	c2 := dialCrashClient(t, ctx2, dataDir, done2)
	if _, uErr := c2.UnlockVault(ctx2, []byte(cheapvault.MasterPassword)); uErr != nil {
		t.Fatalf("UnlockVault after restart 1: %v", uErr)
	}
	awaitMemberTaskUnknown(t, ctx2, c2)
	awaitCleanExit(t, ctx2, c2, done2)
	assertSingleUnknownDelegatorNotice(t, dataDir)

	ctx3, cancel3 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel3()
	done3 := startCrashSubprocess(t, ctx3, dataDir, fugueHome, "")
	c3 := dialCrashClient(t, ctx3, dataDir, done3)
	if _, uErr := c3.UnlockVault(ctx3, []byte(cheapvault.MasterPassword)); uErr != nil {
		t.Fatalf("UnlockVault after restart 2: %v", uErr)
	}
	awaitCleanExit(t, ctx3, c3, done3)
	assertSingleUnknownDelegatorNotice(t, dataDir)
}

func TestCrashPointTeamTaskCommittedRecovery(t *testing.T) {
	t.Parallel()
	dataDir, fugueHome := crashTestDirs(t)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	ctx1, cancel1 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel1()
	done1 := startCrashSubprocess(t, ctx1, dataDir, fugueHome, config.CrashPointTeamTaskCommitted)
	c1 := dialCrashClient(t, ctx1, dataDir, done1)
	defer c1.Close()
	if _, err := c1.UnlockVault(ctx1, []byte(cheapvault.MasterPassword)); err != nil {
		t.Fatalf("UnlockVault: %v", err)
	}
	ws := setupCrashTeamWorkspace(t, ctx1, c1, repoDir)
	sess, err := c1.StartSession(ctx1, "cmd-ttc-start", client.VendorClaude, ws.ID, "repo", client.SessionProfile{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sendErr := c1.Send(ctx1, "cmd-ttc-send", sess.ID, "CRASH_TEST_PROPOSE_TEAM"); sendErr != nil {
		t.Fatalf("Send: %v", sendErr)
	}
	req := awaitPendingProposalCard(t, ctx1, c1)
	if _, aErr := c1.AnswerInteraction(ctx1, &client.AnswerInteractionInput{
		CommandID:   "cmd-ttc-approve",
		RequestID:   req.RequestID,
		Incarnation: req.Incarnation,
		Outcome:     "answered",
		Answers:     []client.InteractionQuestionAnswer{{QuestionID: "decision", OptionIDs: []string{"approve"}}},
	}); aErr != nil {
		t.Logf("AnswerInteraction returned: %v", aErr)
	}
	awaitCrashExit(t, ctx1, done1)

	// The host died after the task-branch commit and before the report event,
	// so the commit exists in git while the store never learned of it.
	wantSHA := assertSingleTaskBranchCommit(t, repoDir)

	restartAwaitUnknownKeepsCommit(t, dataDir, fugueHome, repoDir, wantSHA)
	restartAwaitUnknownKeepsCommit(t, dataDir, fugueHome, repoDir, wantSHA)
}

// restartAwaitUnknownKeepsCommit boots an unarmed host, waits for the member
// task to reconcile to unknown and checks the restart neither duplicated the
// commit nor sent a second delegator notice.
func restartAwaitUnknownKeepsCommit(t *testing.T, dataDir, fugueHome, repoDir, wantSHA string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := startCrashSubprocess(t, ctx, dataDir, fugueHome, "")
	c := dialCrashClient(t, ctx, dataDir, done)
	if _, err := c.UnlockVault(ctx, []byte(cheapvault.MasterPassword)); err != nil {
		t.Fatalf("UnlockVault after restart: %v", err)
	}
	awaitMemberTaskUnknown(t, ctx, c)
	awaitCleanExit(t, ctx, c, done)
	assertSingleUnknownDelegatorNotice(t, dataDir)
	if got := assertSingleTaskBranchCommit(t, repoDir); got != wantSHA {
		t.Fatalf("task branch head after restart = %s, want unchanged %s", got, wantSHA)
	}
}

// assertSingleTaskBranchCommit proves exactly one task branch carries exactly
// one host commit over main (no duplicate effect) and returns its SHA.
func assertSingleTaskBranchCommit(t *testing.T, repoDir string) string {
	t.Helper()
	refs := strings.Fields(runGit(t, repoDir, "for-each-ref", "--format=%(refname)", "refs/heads/"+gitops.TaskBranchPrefix))
	var shas []string
	for _, ref := range refs {
		if n := runGit(t, repoDir, "rev-list", "--count", "main.."+ref); n != "0" {
			if n != "1" {
				t.Fatalf("%s has %s commits over main, want exactly 1", ref, n)
			}
			shas = append(shas, runGit(t, repoDir, "rev-parse", ref))
		}
	}
	if len(shas) != 1 {
		t.Fatalf("task branches with a commit = %v, want exactly 1", shas)
	}
	return shas[0]
}

func awaitPendingProposalCard(t *testing.T, ctx context.Context, c *client.Client) client.PendingInteraction {
	t.Helper()
	var found client.PendingInteraction
	testutil.Spin(t, "pending team_proposal card", func() bool {
		pending, err := c.ListPendingInteractions(ctx)
		if err != nil {
			return false
		}
		for i := range pending.Requests {
			if pending.Requests[i].Kind == "question" && pending.Requests[i].Tool == "fugue_team_propose" {
				found = pending.Requests[i]
				return true
			}
		}
		return false
	})
	return found
}

func awaitMemberTaskUnknown(t *testing.T, ctx context.Context, c *client.Client) {
	t.Helper()
	testutil.Spin(t, "member task reconciled to unknown", func() bool {
		teamsList, err := c.ListTeams(ctx, &client.ListTeamsInput{})
		if err != nil || len(teamsList) == 0 {
			return false
		}
		tv, err := c.GetTeam(ctx, &client.GetTeamInput{TeamID: teamsList[0].TeamID})
		if err != nil {
			return false
		}
		for i := range tv.Tasks {
			if tv.Tasks[i].HierarchyRole == string(vendorstore.RoleMember) {
				return tv.Tasks[i].State == string(vendorstore.TaskStateUnknown)
			}
		}
		return false
	})
}

func assertSingleUnknownDelegatorNotice(t *testing.T, dataDir string) {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(time.Unix(0, 0))
	gen := ids.NewUUIDv7(clk, ids.CryptoRandom{})
	st, err := storage.OpenStore(ctx, config.Config{DataDir: dataDir}, clk, gen)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() {
		if cErr := st.Close(); cErr != nil {
			t.Errorf("close store: %v", cErr)
		}
	}()
	vs, err := vendorstore.New(ctx, st.DB(), nil, clk, ids.CryptoRandom{})
	if err != nil {
		t.Fatalf("vendorstore.New: %v", err)
	}
	mb, err := vs.PendingMailbox(ctx)
	if err != nil {
		t.Fatalf("PendingMailbox: %v", err)
	}
	if len(mb) > 1 {
		t.Fatalf("PendingMailbox count = %d, want at most 1 idempotent notice: %+v", len(mb), mb)
	}
}

func TestCrashPointTeamMergeAfterFFRecovery(t *testing.T) {
	t.Parallel()
	dataDir, fugueHome := crashTestDirs(t)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	ctx0, cancel0 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel0()
	done0 := startCrashSubprocess(t, ctx0, dataDir, fugueHome, "")
	c0 := dialCrashClient(t, ctx0, dataDir, done0)
	if _, err := c0.UnlockVault(ctx0, []byte(cheapvault.MasterPassword)); err != nil {
		t.Fatalf("UnlockVault: %v", err)
	}
	ws := createWorkspaceAndAddRoot(t, ctx0, c0, "crash-ff-ws", "repo", repoDir)
	awaitCleanExit(t, ctx0, c0, done0)

	teamID := "team-crash-ff"
	taskID := "task-crash-ff"
	headSHA := seedIntegratingTeamForCrash(t, dataDir, repoDir, ws.ID, teamID, taskID)

	ctx1, cancel1 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel1()
	done1 := startCrashSubprocess(t, ctx1, dataDir, fugueHome, config.CrashPointTeamMergeAfterFF)
	c1 := dialCrashClient(t, ctx1, dataDir, done1)
	defer c1.Close()
	if _, err := c1.UnlockVault(ctx1, []byte(cheapvault.MasterPassword)); err != nil {
		t.Fatalf("UnlockVault: %v", err)
	}
	awaitCrashExit(t, ctx1, done1)

	teamBr, err := gitops.TeamBranchName(teamID)
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA := runGit(t, repoDir, "rev-parse", teamBr); gotSHA != headSHA {
		t.Fatalf("%s SHA = %s, want fast-forwarded %s", teamBr, gotSHA, headSHA)
	}

	ctx2, cancel2 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel2()
	done2 := startCrashSubprocess(t, ctx2, dataDir, fugueHome, "")
	c2 := dialCrashClient(t, ctx2, dataDir, done2)
	if _, uErr := c2.UnlockVault(ctx2, []byte(cheapvault.MasterPassword)); uErr != nil {
		t.Fatalf("UnlockVault after restart: %v", uErr)
	}
	testutil.Spin(t, "integrating task reconciled to done", func() bool {
		tv, gErr := c2.GetTeam(ctx2, &client.GetTeamInput{TeamID: teamID})
		if gErr != nil {
			return false
		}
		for i := range tv.Tasks {
			if tv.Tasks[i].TaskID == taskID {
				return tv.Tasks[i].State == string(vendorstore.TaskStateDone)
			}
		}
		return false
	})
	awaitCleanExit(t, ctx2, c2, done2)
}

func mustMarshalCrashJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

func prepareCrashTaskWorktree(t *testing.T, dataDir, repoDir, teamID, taskID string) string {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(time.Unix(0, 0))
	teamBr, err := gitops.TeamBranchName(teamID)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "branch", teamBr, "main")
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath(git): %v", err)
	}
	gm, err := gitops.NewManager(gitBin, repoDir, clk)
	if err != nil {
		t.Fatalf("gitops.NewManager: %v", err)
	}
	canonicalRepo := repoDir
	if c, evalErr := filepath.EvalSymlinks(repoDir); evalErr == nil {
		canonicalRepo = c
	}
	repoSum := sha256.Sum256([]byte(canonicalRepo))
	repoID := "r" + hex.EncodeToString(repoSum[:8])
	wtPath, err := gm.CreateTaskWorktree(ctx, dataDir, canonicalRepo, repoID, taskID, teamBr)
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	if wErr := os.WriteFile(filepath.Join(wtPath, "change.txt"), []byte("merged\n"), 0o600); wErr != nil {
		t.Fatal(wErr)
	}
	runGit(t, wtPath, "add", "change.txt")
	runGit(t, wtPath, "commit", "-m", "task commit")
	return runGit(t, wtPath, "rev-parse", "HEAD")
}

func seedIntegratingTeamForCrash(t *testing.T, dataDir, repoDir, wsID, teamID, taskID string) string {
	t.Helper()
	headSHA := prepareCrashTaskWorktree(t, dataDir, repoDir, teamID, taskID)
	ctx := t.Context()
	clk := clock.NewFake(time.Unix(0, 0))
	gen := ids.NewUUIDv7(clk, ids.CryptoRandom{})
	st, err := storage.OpenStore(ctx, config.Config{DataDir: dataDir}, clk, gen)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() {
		if cErr := st.Close(); cErr != nil {
			t.Errorf("close store: %v", cErr)
		}
	}()
	v, err := vault.NewService(ctx, vault.NewSQLKeyringStore(st.DB()), gen)
	if err != nil {
		t.Fatalf("vault.NewService: %v", err)
	}
	cheapvault.Unlock(t, v)
	vs, err := vendorstore.New(ctx, st.DB(), v, clk, ids.CryptoRandom{})
	if err != nil {
		t.Fatalf("vendorstore.New: %v", err)
	}

	leadID := "lead-crash-ff"
	specBytes := mustMarshalCrashJSON(t, teams.ProtectedTaskContent{Goal: "implement", WriteScope: []string{"change.txt"}})
	rec, _, err := vs.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: wsID, SessionID: "sess-prop",
		ProposedEventID: "ev-prop", ProposedPayload: []byte(`{"title":"ff","brief":"ff"}`),
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: leadID, HierarchyRole: vendorstore.RoleLead, CatalogRole: crashLeadRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c-lead", ProtectedSpec: specBytes},
			{TaskID: taskID, ParentTaskID: leadID, HierarchyRole: vendorstore.RoleMember, CatalogRole: crashWorkerRole, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "c-mem", ProtectedSpec: specBytes},
		},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	rolesJSON := mustMarshalCrashJSON(t, map[string]teams.Role{
		crashLeadRole:   {Name: crashLeadRole, Vendor: "claude", Permission: "write", IsLeadEligible: true},
		crashWorkerRole: {Name: crashWorkerRole, Vendor: "claude", Permission: "write"},
	})
	evs := []*vendorstore.AppendTeamEventInput{
		{EventID: "ev-start", EventType: vendorstore.EventTeamStarted, ProtectedPayload: rolesJSON},
		{EventID: "ev-la", TaskID: leadID, EventType: vendorstore.EventTaskAssigned, SessionID: "sess-lead"},
		{EventID: "ev-ls", TaskID: leadID, EventType: vendorstore.EventTaskStarted},
		{EventID: "ev-ma", TaskID: taskID, EventType: vendorstore.EventTaskAssigned, SessionID: "sess-mem"},
		{EventID: "ev-ms", TaskID: taskID, EventType: vendorstore.EventTaskStarted},
		{EventID: "ev-mr", TaskID: taskID, EventType: vendorstore.EventTaskReported, ProtectedPayload: []byte(`{"summary":"done"}`)},
		{EventID: "ev-mc", TaskID: taskID, EventType: vendorstore.EventTaskCheckCompleted, Outcome: "passed"},
		{EventID: "ev-mv", TaskID: taskID, EventType: vendorstore.EventTaskReviewed, Outcome: "approved"},
	}
	if _, err := vs.AppendTeamEvents(ctx, teamID, rec.LastSeq, evs); err != nil {
		t.Fatalf("AppendTeamEvents: %v", err)
	}
	return headSHA
}
