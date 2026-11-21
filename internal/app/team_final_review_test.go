package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/ocr"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func runSingleDiffTaskToTeamBranch(t *testing.T, teamID, file, content string) *integrationFixture {
	t.Helper()
	fx := newLeadMembersFixture(t, teamID, "sess-"+teamID+"-lead", []string{"t-work"}, [][]string{{"*"}})
	wt := fx.createWorktree(t, "t-work", fx.teamBr)
	fx.writeAndCommit(t, wt, file, content)
	fx.doneAndAccept(t, "t-work")
	fx.waitMergeIdle(t)
	return fx
}

func findPendingFinalReviewCard(t *testing.T, fx *integrationFixture) PendingInteractionView {
	t.Helper()
	list, _, err := fx.rt.interactions.ListPending(t.Context(), testAppPrincipal)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	for i := range list {
		if list[i].Tool == teamFinalReviewToolName {
			return list[i]
		}
	}
	t.Fatalf("no pending final review card in %+v", list)
	return PendingInteractionView{}
}

func answerFinalReviewCard(t *testing.T, fx *integrationFixture, card *PendingInteractionView, optionID, text string) {
	t.Helper()
	if _, err := fx.rt.interactions.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-fr-" + card.RequestID,
		Incarnation: card.Incarnation,
		RequestID:   card.RequestID,
		Principal:   testAppPrincipal,
		Outcome:     string(interaction.OutcomeAnswered),
		Answers: []InteractionAnswer{{
			QuestionID: teamDecisionQuestionID,
			OptionIDs:  []string{optionID},
			Text:       text,
		}},
	}); err != nil {
		t.Fatalf("Answer(%s): %v", optionID, err)
	}
}

func commitFileOnMain(t *testing.T, fx *integrationFixture, rel, content, msg string) string {
	t.Helper()
	if wErr := os.WriteFile(filepath.Join(fx.repoDir, rel), []byte(content), 0o600); wErr != nil {
		t.Fatal(wErr)
	}
	if _, addErr := fx.rt.teamTasks.git.CommitWorktree(t.Context(), fx.repoDir, msg); addErr != nil {
		t.Fatal(addErr)
	}
	head, err := fx.rt.teamTasks.git.BranchHead(t.Context(), fx.repoDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func TestFinalReviewMergesTeamBranchOnlyAfterUserApproval(t *testing.T) {
	t.Parallel()
	t.Run("approve_rebases_and_fast_forwards", func(t *testing.T) {
		t.Parallel()
		testFinalReviewApproveRebasesAndFastForwards(t)
	})
	t.Run("conflict_goes_to_lead", func(t *testing.T) {
		t.Parallel()
		testFinalReviewConflictGoesToLead(t)
	})
}

func testFinalReviewApproveRebasesAndFastForwards(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	fx := runSingleDiffTaskToTeamBranch(t, "team-fr-ok", "feature.txt", "from team\n")

	headBefore, err := fx.rt.teamTasks.git.BranchHead(ctx, fx.repoDir, "main")
	if err != nil || headBefore != fx.baseHead {
		t.Fatalf("main moved before approval: (%s != %s, %v)", headBefore, fx.baseHead, err)
	}
	rec, _, err := fx.hp.vendorStore.TeamSnapshot(ctx, fx.teamID)
	if err != nil || rec.State != vendorstore.TeamStateFinalReview {
		t.Fatalf("team state = (%s, %v), want final_review", rec.State, err)
	}
	card := findPendingFinalReviewCard(t, fx)
	if !strings.Contains(card.Body, "feature.txt") || !strings.Contains(card.Body, "t-work") {
		t.Fatalf("unexpected final review card body:\n%s", card.Body)
	}

	outsideHead := commitFileOnMain(t, fx, "outside.txt", "outside\n", "outside commit")
	answerFinalReviewCard(t, fx, &card, decisionApprove, "")
	fx.waitMergeIdle(t)
	assertMainMergedOutsideAndTeamDone(t, fx, outsideHead)
}

func assertMainMergedOutsideAndTeamDone(t *testing.T, fx *integrationFixture, outsideHead string) {
	t.Helper()
	ctx := t.Context()
	rec, _, err := fx.hp.vendorStore.TeamSnapshot(ctx, fx.teamID)
	if err != nil || rec.State != vendorstore.TeamStateDone {
		t.Fatalf("team state after approve = (%s, %v), want done", rec.State, err)
	}
	newHead, err := fx.rt.teamTasks.git.BranchHead(ctx, fx.repoDir, "main")
	if err != nil || newHead == outsideHead {
		t.Fatalf("expected main to advance with rebased team branch: %s (%v)", newHead, err)
	}
	hasOutside, err := fx.rt.teamTasks.git.BranchContainsCommit(ctx, fx.repoDir, "main", outsideHead)
	if err != nil || !hasOutside {
		t.Fatalf("main lost outside commit: (%t, %v)", hasOutside, err)
	}
}

func testFinalReviewConflictGoesToLead(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	fx := runSingleDiffTaskToTeamBranch(t, "team-fr-conflict", "shared.txt", "team version\n")

	mainBefore := commitFileOnMain(t, fx, "shared.txt", "main version\n", "conflicting main commit")
	card := findPendingFinalReviewCard(t, fx)
	answerFinalReviewCard(t, fx, &card, decisionApprove, "")
	fx.waitMergeIdle(t)

	mainAfter, err := fx.rt.teamTasks.git.BranchHead(ctx, fx.repoDir, "main")
	if err != nil || mainAfter != mainBefore {
		t.Fatalf("main changed on conflict: %s != %s", mainAfter, mainBefore)
	}
	rec, _, err := fx.hp.vendorStore.TeamSnapshot(ctx, fx.teamID)
	if err != nil || rec.State != vendorstore.TeamStateActive {
		t.Fatalf("team state on conflict = (%s, %v), want active", rec.State, err)
	}
	assertConflictRecordedForLead(t, fx, "shared.txt")
	testutil.Spin(t, "lead mailbox has conflict notice", func() bool {
		mail, mErr := fx.hp.vendorStore.PendingMailboxRows(ctx, fx.teamID, intLeadTaskID)
		return mErr == nil && hasMailboxSummary(ctx, fx, mail, "shared.txt")
	})
}

func assertConflictRecordedForLead(t *testing.T, fx *integrationFixture, file string) {
	t.Helper()
	ctx := t.Context()
	events, err := fx.hp.vendorStore.TeamEventsSince(ctx, fx.teamID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		ev := &events[i]
		if ev.EventType != vendorstore.EventTeamDecided || ev.Outcome != "revision" {
			continue
		}
		payload, openErr := fx.hp.vendorStore.OpenTeamEventPayload(ctx, fx.teamID, ev.Sequence)
		if openErr != nil {
			t.Fatal(openErr)
		}
		var body struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(payload, &body) == nil && strings.Contains(body.Reason, file) {
			return
		}
	}
	t.Fatalf("expected EventTeamDecided(revision) with conflict in %s", file)
}

func hasMailboxSummary(ctx context.Context, fx *integrationFixture, mail []vendorstore.MailboxMessage, substr string) bool {
	for i := range mail {
		payload, openErr := fx.hp.vendorStore.OpenMailboxPayload(ctx, fx.teamID, mail[i].MessageID)
		if openErr != nil {
			continue
		}
		var item mailboxDigestItem
		if json.Unmarshal(payload, &item) == nil && strings.Contains(item.Summary, substr) {
			return true
		}
	}
	return false
}

func TestFinalReviewRejectWakesTheLead(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fx := runSingleDiffTaskToTeamBranch(t, "team-fr-reject", "rej.txt", "draft\n")

	card := findPendingFinalReviewCard(t, fx)
	answerFinalReviewCard(t, fx, &card, decisionReject, "please add unit tests")

	headAfter, err := fx.rt.teamTasks.git.BranchHead(ctx, fx.repoDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if headAfter != fx.baseHead {
		t.Fatalf("main changed on reject: %s != %s", headAfter, fx.baseHead)
	}
	rec, _, err := fx.hp.vendorStore.TeamSnapshot(ctx, fx.teamID)
	if err != nil || rec.State != vendorstore.TeamStateActive {
		t.Fatalf("team state after reject = (%s, %v), want active", rec.State, err)
	}
	testutil.Spin(t, "lead mailbox has rejection reason", func() bool {
		mail, mErr := fx.hp.vendorStore.PendingMailboxRows(ctx, fx.teamID, intLeadTaskID)
		return mErr == nil && hasMailboxSummary(ctx, fx, mail, "please add unit tests")
	})
}

func TestFinalReviewCardRebuildsOnUnlockAndInFlightReconciles(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	fx := runSingleDiffTaskToTeamBranch(t, "team-fr-rec", "rec.txt", "rebuild\n")
	card := findPendingFinalReviewCard(t, fx)
	opID, err := interaction.NativeOperationKey(fx.teamID, fx.teamID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, invErr := fx.rt.interactions.owner.InvalidateOperation(opID); invErr != nil {
		t.Fatal(invErr)
	}
	if recErr := fx.rt.teamTasks.RecoverOnUnlock(ctx); recErr != nil {
		t.Fatalf("RecoverOnUnlock(rebuild): %v", recErr)
	}
	rebuilt := findPendingFinalReviewCard(t, fx)
	if rebuilt.Summary != card.Summary {
		t.Fatalf("rebuilt summary = %q, want %q", rebuilt.Summary, card.Summary)
	}

	if _, ffErr := fx.rt.teamTasks.git.FastForwardBranch(ctx, fx.repoDir, "main", fx.teamBr); ffErr != nil {
		t.Fatalf("FastForwardBranch: %v", ffErr)
	}
	if recErr := fx.rt.teamTasks.RecoverOnUnlock(ctx); recErr != nil {
		t.Fatalf("RecoverOnUnlock(reconcile): %v", recErr)
	}
	rec, _, err := fx.hp.vendorStore.TeamSnapshot(ctx, fx.teamID)
	if err != nil || rec.State != vendorstore.TeamStateDone {
		t.Fatalf("team state after reconcile = (%s, %v), want done", rec.State, err)
	}
}

func TestFinalReviewAnswerValidationAndEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	fx := newLeadMembersFixture(t, "team-fr-val", "sess-fr-val-lead", []string{teamTestTaskA}, [][]string{{"*"}})
	svc := fx.rt.teamTasks
	ticket := &interaction.Ticket{Binding: interaction.Binding{InvocationID: fx.teamID}}

	if err := svc.validateTeamFinalReviewAnswer(ctx, ticket, &interaction.Decision{Outcome: interaction.OutcomeCancelled}); err != nil {
		t.Fatalf("validate non-answered: %v", err)
	}
	if err := svc.applyTeamFinalReviewAnswer(ctx, ticket, &interaction.Decision{Outcome: interaction.OutcomeCancelled}); err != nil {
		t.Fatalf("apply non-answered: %v", err)
	}
	badOpt := &interaction.Decision{
		Outcome: interaction.OutcomeAnswered,
		Answers: []interaction.Answer{{QuestionID: teamDecisionQuestionID, OptionIDs: []string{decisionSolo}}},
	}
	if err := svc.validateTeamFinalReviewAnswer(ctx, ticket, badOpt); err == nil {
		t.Fatal("validate invalid option succeeded, want error")
	}
	if err := svc.applyTeamFinalReviewAnswer(ctx, ticket, badOpt); err == nil {
		t.Fatal("apply invalid option succeeded, want error")
	}
	approveDec := &interaction.Decision{
		Outcome: interaction.OutcomeAnswered,
		Answers: []interaction.Answer{{QuestionID: teamDecisionQuestionID, OptionIDs: []string{decisionApprove}}},
	}
	if err := svc.validateTeamFinalReviewAnswer(ctx, ticket, approveDec); err == nil {
		t.Fatal("validate approve in active state succeeded, want error")
	}
	if err := (&teamTaskService{}).applyTeamFinalReviewAnswer(ctx, ticket, approveDec); err == nil {
		t.Fatal("apply approve without mergeQueue succeeded, want error")
	}

	wt := fx.createWorktree(t, teamTestTaskA, fx.teamBr)
	fx.writeAndCommit(t, wt, "f.txt", "hi\n")
	fx.doneAndAccept(t, teamTestTaskA)
	fx.waitMergeIdle(t)
	_ = findPendingFinalReviewCard(t, fx)
	rejectEmpty := &interaction.Decision{
		Outcome: interaction.OutcomeAnswered,
		Answers: []interaction.Answer{{QuestionID: teamDecisionQuestionID, OptionIDs: []string{decisionReject}, Text: "\t "}},
	}
	if err := svc.applyTeamFinalReviewAnswer(ctx, ticket, rejectEmpty); err != nil {
		t.Fatalf("apply reject empty reason: %v", err)
	}
	testutil.Spin(t, "lead mailbox has default reject reason", func() bool {
		mail, mErr := fx.hp.vendorStore.PendingMailboxRows(ctx, fx.teamID, intLeadTaskID)
		return mErr == nil && hasMailboxSummary(ctx, fx, mail, "rejected by user")
	})
}

func TestCrossReviewCheckReadAndAggregate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	fx := newLeadMembersFixture(t, "team-cr-read", "sess-cr-read-lead", []string{teamTestTaskA, "t-b"}, [][]string{{"*"}, {"*"}})
	svc := fx.rt.teamTasks
	if _, err := svc.readTaskChecks(ctx, fx.teamID, teamTestTaskA); err == nil {
		t.Fatal("readTaskChecks without worktree succeeded, want error")
	}

	wt := fx.createWorktree(t, teamTestTaskA, fx.teamBr)
	fx.writeAndCommit(t, wt, "f.txt", "hi\n")

	cr := CrossReviewReport{
		Coverage: CrossReviewCoverage{
			ReviewedFiles:        2,
			TotalFiles:           3,
			ModeLabel:            "no OCR",
			SkipReasons:          []string{"bundle-1: empty"},
			DroppedLowCount:      1,
			DroppedPositionCount: 1,
		},
		Findings: []ocr.Finding{{
			File: "a.go", LineStart: 1, LineEnd: 2, Severity: "high", Rule: "sec", Description: "issue",
		}},
	}
	payload, err := json.Marshal(cr)
	if err != nil {
		t.Fatal(err)
	}
	if repErr := svc.appendTaskEventWithRetry(ctx, fx.teamID, func(_ *vendorstore.TeamRecord, _ []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		return &vendorstore.AppendTeamEventInput{
			EventID: "ev-sub-ta", EventType: vendorstore.EventTaskReported, TaskID: teamTestTaskA,
		}, nil
	}); repErr != nil {
		t.Fatal(repErr)
	}
	if chkErr := svc.appendTaskEventWithRetry(ctx, fx.teamID, func(_ *vendorstore.TeamRecord, _ []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		return &vendorstore.AppendTeamEventInput{
			EventID: "ev-chk-ta", EventType: vendorstore.EventTaskCheckCompleted, TaskID: teamTestTaskA, Outcome: "passed", ProtectedPayload: payload,
		}, nil
	}); chkErr != nil {
		t.Fatal(chkErr)
	}

	checks, err := svc.readTaskChecks(ctx, fx.teamID, teamTestTaskA)
	if err != nil || !strings.Contains(checks, "passed=true") || !strings.Contains(checks, "[high] a.go:1-2 (sec): issue") {
		t.Fatalf("readTaskChecks(with report) = (%q, %v)", checks, err)
	}
	_, tasks, err := fx.hp.vendorStore.TeamSnapshot(ctx, fx.teamID)
	if err != nil {
		t.Fatal(err)
	}
	agg := svc.aggregateCrossReviewSummary(ctx, fx.teamID, tasks)
	if !strings.Contains(agg, "2/3 files reviewed") || !strings.Contains(agg, "1 finding(s)") {
		t.Fatalf("aggregateCrossReviewSummary = %q", agg)
	}
}
