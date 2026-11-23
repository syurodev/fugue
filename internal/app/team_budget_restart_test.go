package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// TestTeamBudgetWaitSurvivesAHostRestart is the budget half of CL-E03: a team
// stopped by its deadline is still waiting for the user after the host
// restarts, queued work stays queued, the decision card comes back once the
// vault unlocks, and answering it resumes the team.
func TestTeamBudgetWaitSurvivesAHostRestart(t *testing.T) {
	t.Parallel()
	f := newBudgetsTestFixture(t, "")
	args := validTeamProposalArgs()
	args.Budget = &teams.TeamBudget{MaxAgents: 4, MaxDurationSeconds: 300}
	args.Tasks = []teams.ProposedTask{
		{ID: teamTestLeadTask, TeamRole: teams.RoleLead, Role: teamTestRole, Goal: testBudgetLeadGoal, WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "w1", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "First worker", WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "w2", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Second worker", WriteScope: []string{}, Deliverable: teams.DeliverableReport, DependsOn: []string{"w1"}},
	}
	teamID := f.approve(t, args, decisionApprove, "")
	w1ID, w2ID := durableTaskID(teamID, "w1"), durableTaskID(teamID, "w2")
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	f.clk.Advance(301 * time.Second)
	f.budgets.Step(t.Context())
	completeAndAcceptTask(t, f, w1ID)
	if rec, _ := f.teamSnapshot(t, teamID); rec.WaitingReason != vendorstore.TeamWaitingDeadline {
		t.Fatalf("waiting_reason before restart = %q, want %q", rec.WaitingReason, vendorstore.TeamWaitingDeadline)
	}

	dataDir := f.hp.store.DataDir()
	closeHostAdvanceClock(t, f.hp, f.clk)
	hp2 := startRecoveryHost(t, dataDir, filepath.Join(t.TempDir(), "Fugue"), f.clk)
	defer closeHostAdvanceClock(t, hp2, f.clk)

	rec, tasks, err := hp2.vendorStore.TeamSnapshot(t.Context(), teamID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != vendorstore.TeamStateActive || rec.WaitingReason != vendorstore.TeamWaitingDeadline {
		t.Fatalf("team after restart = (%s, waiting=%q), want (active, %q)", rec.State, rec.WaitingReason, vendorstore.TeamWaitingDeadline)
	}
	if w2, ok := findTask(tasks, w2ID); !ok || w2.State != vendorstore.TaskStateQueued {
		t.Fatalf("w2 after restart = %+v (found=%v), want queued", w2, ok)
	}

	unlockCheapVault(t, hp2)
	answerRebuiltDeadlineCard(t, hp2, teamID, w2ID)
}

// answerRebuiltDeadlineCard waits for the deadline card the unlock rebuilds,
// checks the waiting team still refuses a start, and extends the deadline.
func answerRebuiltDeadlineCard(t *testing.T, hp2 *HostProcess, teamID, w2ID string) {
	t.Helper()
	var card PendingInteractionView
	testutil.Spin(t, "deadline card rebuilt after restart", func() bool {
		views, _, listErr := hp2.rt.ListPendingInteractions(t.Context(), testAppPrincipal)
		for i := range views {
			if listErr == nil && views[i].Tool == teamDeadlineToolName {
				card = views[i]
				return true
			}
		}
		return false
	})
	if startErr := hp2.rt.teamDispatch.StartTask(t.Context(), teamID, w2ID); errcode.CodeOf(startErr) != errcode.FailedPrecondition {
		t.Fatalf("StartTask while the restarted team waits = %v, want FailedPrecondition", startErr)
	}
	if _, answerErr := hp2.rt.AnswerInteraction(t.Context(), &AnswerInteractionCommand{
		CommandID: "dl-restart-extend", Principal: testAppPrincipal,
		Incarnation: hp2.rt.interactions.Incarnation(), RequestID: card.RequestID, Outcome: OutcomeAnswered,
		Answers: []InteractionAnswer{{QuestionID: teamDeadlineQuestionID, OptionIDs: []string{teamDeadlineOptionExtend}, Text: "180"}},
	}); answerErr != nil {
		t.Fatalf("answer the rebuilt deadline card: %v", answerErr)
	}
	if after, _, snapErr := hp2.vendorStore.TeamSnapshot(t.Context(), teamID); snapErr != nil || after.WaitingReason != "" {
		t.Fatalf("waiting_reason after extend = (%q, %v), want empty", after.WaitingReason, snapErr)
	}
}
