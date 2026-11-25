package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/testutil"

	"github.com/syurodev/fugue/pkg/client"
)

// TestFailureUXTeamBudgetRefusesAnOversizedProposalOverARealHost is the
// max_agents half of the activation §3 "run budget hit" row (A-E05): a lead
// that proposes more agents than the team budget allows gets the refusal text
// back from the tool, and the host lists no team and raises no card.
func TestFailureUXTeamBudgetRefusesAnOversizedProposalOverARealHost(t *testing.T) {
	t.Parallel()
	h := startE2EHostWith(t, &e2eHostOptions{bridge: true})
	ctx := failureUXCtx(t)

	_, lead := startFailureUXLead(t, ctx, h.client, failureUXRepo(t), "FAILUX_PROPOSE;members=3;max_agents=2;")

	text := awaitFailureUXText(t, ctx, h.client, lead.ID, failureUXAssistant, "exceeds its team budget max_agents")
	if !strings.HasPrefix(text, "fugue_team_propose: error: ") {
		t.Fatalf("tool answer = %q, want a refusal", text)
	}
	teamsList, err := h.client.ListTeams(ctx, &client.ListTeamsInput{})
	if err != nil || len(teamsList) != 0 {
		t.Fatalf("ListTeams after the refused proposal = (%+v, %v), want no team", teamsList, err)
	}
	pending, err := h.client.ListPendingInteractions(ctx)
	if err != nil || len(pending.Requests) != 0 {
		t.Fatalf("pending interactions after the refused proposal = (%+v, %v), want none", pending.Requests, err)
	}
}

// TestFailureUXTeamDeadlineBudgetWaitsForADecisionOverARealHost is the
// max_duration half of the §3 "run budget hit" row (A-E05), on the fake
// clock: an expired team deadline stops new task starts, shows the budget
// usage on a decision card, and the user either extends the deadline or ends
// the queued work; running tasks are never touched.
func TestFailureUXTeamDeadlineBudgetWaitsForADecisionOverARealHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ option, wantFifth, wantWaiting string }{
		{option: "extend", wantFifth: failureUXTaskQueued, wantWaiting: ""},
		// Ending the queued work leaves the team paused on its deadline: running
		// tasks finish, nothing new starts.
		{option: "cancel_queued", wantFifth: "cancelled", wantWaiting: "team_deadline"},
	} {
		t.Run(tc.option, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewFake(time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC))
			pump := startClockPump(t, clk)
			h := startE2EHostWith(t, &e2eHostOptions{
				bridge: true, clock: clk,
				hostConfig: "[host]\nprompt_pacing_min_seconds = 0\nprompt_pacing_max_seconds = 0\n",
			})
			ctx := failureUXCtx(t)
			_, _ = startFailureUXLead(t, ctx, h.client, failureUXRepo(t), "FAILUX_PROPOSE;members=5;max_duration=1800;")
			teamID := approveFailureUXProposal(t, ctx, h.client)
			// The cap of four running direct children leaves the fifth queued.
			awaitFailureUXMembers(t, ctx, h.client, teamID, "running", "running", "running", "running", failureUXTaskQueued)
			// Wait for all four paced intro turns to land before jumping the fake
			// clock past pacedSendTimeout (30s) and idleDetach (300s).
			awaitFailureUXMemberIntros(t, ctx, h.client, teamID)

			pump.paused.Store(true)
			t.Cleanup(func() { pump.paused.Store(false) })
			clk.Advance(1801 * time.Second)
			testutil.Spin(t, "team waiting on its deadline", func() bool {
				list, err := h.client.ListTeams(ctx, &client.ListTeamsInput{})
				return err == nil && len(list) == 1 && list[0].WaitingReason == "team_deadline"
			})
			card := findFailureUXQuestion(t, ctx, h.client, "fugue_team_deadline")
			for _, want := range []string{"Team deadline reached", "running tasks: 4", "queued tasks paused: 1"} {
				if !strings.Contains(card.Summary+"\n"+card.Body, want) {
					t.Fatalf("deadline card = %+v, want %q", card, want)
				}
			}
			awaitFailureUXMembers(t, ctx, h.client, teamID, "running", "running", "running", "running", failureUXTaskQueued)

			answerFailureUXCard(t, ctx, h.client, &card, tc.option)
			awaitFailureUXMembers(t, ctx, h.client, teamID, "running", "running", "running", "running", tc.wantFifth)
			testutil.Spin(t, "deadline wait settled", func() bool {
				list, err := h.client.ListTeams(ctx, &client.ListTeamsInput{})
				return err == nil && len(list) == 1 && list[0].WaitingReason == tc.wantWaiting
			})
		})
	}
}

func awaitFailureUXMemberIntros(t *testing.T, ctx context.Context, c *client.Client, teamID string) {
	t.Helper()
	testutil.Spin(t, "running member intro turns started", func() bool {
		_, members := failureUXMembers(t, ctx, c, teamID)
		for i := range members {
			if members[i].State != failureUXTaskRunning {
				continue
			}
			if members[i].SessionID == "" || !sessionHasItemKind(ctx, c, members[i].SessionID, failureUXUser) {
				return false
			}
		}
		return true
	})
}

func sessionHasItemKind(ctx context.Context, c *client.Client, sessionID, kind string) bool {
	page, err := c.TranscriptPage(ctx, sessionID, 0, 10)
	if err != nil {
		return false
	}
	for i := range page {
		if page[i].Kind == kind {
			return true
		}
	}
	return false
}
