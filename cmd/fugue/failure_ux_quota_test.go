package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/pkg/client"
)

const (
	failureUXAccountPaused = "paused"
	failureUXQuotaCode     = "quota"
	failureUXFailedOutcome = "failed"
	failureUXSessionReady  = "ready"
)

// failureUXQuotaHost starts a host on a fake clock at a fixed morning, with
// the clock pump running so vendor processes make progress.
func failureUXQuotaHost(t *testing.T, bridge bool) (*e2eHost, *clock.Fake, *clockPump, time.Time) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC))
	pump := startClockPump(t, clk)
	h := startE2EHostWith(t, &e2eHostOptions{
		bridge: bridge, clock: clk,
		hostConfig: "[host]\nprompt_pacing_min_seconds = 0\nprompt_pacing_max_seconds = 0\n",
	})
	return h, clk, pump, time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
}

func failureUXClaudeAccount(t *testing.T, ctx context.Context, c *client.Client) client.Account {
	t.Helper()
	accounts, err := c.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	for i := range accounts {
		if accounts[i].Vendor == client.VendorClaude {
			return accounts[i]
		}
	}
	t.Fatalf("accounts = %+v, want a claude account", accounts)
	return client.Account{}
}

func assertFailureUXPaused(t *testing.T, acc *client.Account, resetsAt time.Time) {
	t.Helper()
	want := resetsAt.UTC().Format(time.RFC3339)
	if acc.State != failureUXAccountPaused || acc.PausedUntil != want {
		t.Fatalf("claude account = %+v, want state paused until %s", *acc, want)
	}
	if len(acc.Windows) != 1 || acc.Windows[0].Name != "five_hour" || acc.Windows[0].Status != "rejected" || acc.Windows[0].ResetsAt != want {
		t.Fatalf("claude windows = %+v, want one rejected five_hour window resetting at %s", acc.Windows, want)
	}
}

// TestFailureUXPausedAccountHoldsAStartUntilItResetsOverARealHost is the
// "paused vendor account" row of activation §3 (A-E05): after the vendor
// reports a full window the account is paused with the reset time in its
// read model, a team task waits in queued (never failed, no session starts),
// and the task starts when the clock reaches resets_at.
func TestFailureUXPausedAccountHoldsAStartUntilItResetsOverARealHost(t *testing.T) {
	t.Parallel()
	h, clk, pump, resetsAt := failureUXQuotaHost(t, true)
	ctx := failureUXCtx(t)
	_, _ = startFailureUXLead(t, ctx, h.client, failureUXRepo(t),
		"FAILUX_PROPOSE;members=1;quota=1;resets="+strconv.FormatInt(resetsAt.Unix(), 10)+";")
	teamID := approveFailureUXProposal(t, ctx, h.client)
	awaitFailureUXLeadRunning(t, ctx, h.client, teamID)
	tv := awaitFailureUXMembers(t, ctx, h.client, teamID, failureUXTaskQueued)

	if tv.Summary.FailedCount != 0 {
		t.Fatalf("failed tasks = %d, want 0: a paused account queues, it does not fail", tv.Summary.FailedCount)
	}
	if got := failureUXSessionCount(t, ctx, h.client); got != 1 {
		t.Fatalf("sessions = %d, want 1 (the lead only)", got)
	}
	acc := failureUXClaudeAccount(t, ctx, h.client)
	assertFailureUXPaused(t, &acc, resetsAt)

	// The pump stops so the test owns the clock: one second short of the reset
	// the account is still paused and the task still waits.
	pump.paused.Store(true)
	t.Cleanup(func() { pump.paused.Store(false) })
	clk.Advance(resetsAt.Add(-time.Second).Sub(clk.Now()))
	awaitFailureUXMembers(t, ctx, h.client, teamID, failureUXTaskQueued)
	if acc = failureUXClaudeAccount(t, ctx, h.client); acc.State != failureUXAccountPaused {
		t.Fatalf("claude account one second before resets_at = %+v, want paused", acc)
	}

	// The reopen timer is armed by the host; each step is one clock second, so
	// the loop ends as soon as the host has seen the reset.
	testutil.Spin(t, "queued task starts at resets_at", func() bool {
		clk.Advance(time.Second)
		_, members := failureUXMembers(t, ctx, h.client, teamID)
		return len(members) == 1 && members[0].State == failureUXTaskRunning
	})
	if acc = failureUXClaudeAccount(t, ctx, h.client); acc.State == failureUXAccountPaused {
		t.Fatalf("claude account after resets_at = %+v, want reopened", acc)
	}
}

// TestFailureUXQuotaExhaustedMidTurnRecordsTheFailureOverARealHost is the
// "vendor quota exhausted" row of activation §3 (A-E05): the CLI reports a
// rejected window and a 429 partway through a turn. The host records the turn
// as failed with failure code quota on the transcript topic, shows the account
// paused until the reset, and the session stays usable: it is ready again and
// answers the next prompt.
func TestFailureUXQuotaExhaustedMidTurnRecordsTheFailureOverARealHost(t *testing.T) {
	t.Parallel()
	h, _, _, resetsAt := failureUXQuotaHost(t, false)
	ctx := failureUXCtx(t)
	ws := setupCrashTeamWorkspace(t, ctx, h.client, failureUXRepo(t))
	sess, err := h.client.StartSession(ctx, "failux-quota-start", client.VendorClaude, ws.ID, failureUXRepoRoot, client.SessionProfile{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	stream, err := h.client.Subscribe(ctx, "transcript:"+sess.ID, nil)
	if err != nil {
		t.Fatalf("subscribe to the transcript: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := stream.Close(); closeErr != nil {
			t.Logf("close the transcript stream: %v", closeErr)
		}
	})
	if sendErr := h.client.Send(ctx, "failux-quota-send", sess.ID, "FAILUX_QUOTA_MIDTURN;resets="+strconv.FormatInt(resetsAt.Unix(), 10)+";"); sendErr != nil {
		t.Fatalf("Send: %v", sendErr)
	}

	state := awaitFailureUXFailedTurn(t, stream)
	if state.Outcome != failureUXFailedOutcome || state.FailureCode != failureUXQuotaCode {
		t.Fatalf("turn state = %+v, want outcome %s with failure code %s", state, failureUXFailedOutcome, failureUXQuotaCode)
	}
	acc := failureUXClaudeAccount(t, ctx, h.client)
	assertFailureUXPaused(t, &acc, resetsAt)

	// Not wedged: the session returns to ready and runs the next turn.
	testutil.Spin(t, "session ready after the failed turn", func() bool {
		list, listErr := h.client.ListSessions(ctx)
		if listErr != nil {
			return false
		}
		for i := range list.Sessions {
			if list.Sessions[i].ID == sess.ID {
				return list.Sessions[i].State == failureUXSessionReady
			}
		}
		return false
	})
	if sendErr := h.client.Send(ctx, "failux-quota-reply", sess.ID, failureUXReplyMarker); sendErr != nil {
		t.Fatalf("Send after the failed turn: %v", sendErr)
	}
	awaitFailureUXText(t, ctx, h.client, sess.ID, failureUXAssistant, failureUXRepliedText)
}

// awaitFailureUXFailedTurn reads the transcript topic until a turn ends with
// an outcome, and returns that state event.
func awaitFailureUXFailedTurn(t *testing.T, stream *client.Stream) client.TranscriptState {
	t.Helper()
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("receive a transcript message before any turn ended: %v", err)
		}
		if msg.Event == nil || msg.Event.Type != app.EventTranscriptState {
			continue
		}
		state, decErr := client.DecodeTranscriptState(msg.Event.Payload)
		if decErr != nil {
			t.Fatalf("decode the transcript state: %v", decErr)
		}
		if state.Outcome != "" {
			return state
		}
	}
}

// TestFailureUXHeadlessHostNeverAutoApprovesOverARealHost is the "headless
// host" row of activation §3 (A-E05): with no app or responder attached, an
// approval-needing request (the lead's team proposal card) is never resolved
// by the host. It stays pending however much host time passes, the team stays
// proposed, no member session starts, and the proposal transcript item stays
// pending with no decision delivered.
func TestFailureUXHeadlessHostNeverAutoApprovesOverARealHost(t *testing.T) {
	t.Parallel()
	h, clk, pump, _ := failureUXQuotaHost(t, true)
	ctx := failureUXCtx(t)
	_, lead := startFailureUXLead(t, ctx, h.client, failureUXRepo(t), "FAILUX_PROPOSE;members=1;")
	card := awaitPendingProposalCard(t, ctx, h.client)
	awaitFailureUXText(t, ctx, h.client, lead.ID, failureUXAssistant, "waits for the user's decision in the approval inbox")

	pump.paused.Store(true)
	t.Cleanup(func() { pump.paused.Store(false) })
	clk.Advance(24 * time.Hour)
	// A later request lists after the clock moved, so a resolve on the timer
	// would already have shown.
	testutil.Poll(t, "pending card survives a day of host time", func() bool {
		pending, err := h.client.ListPendingInteractions(ctx)
		return err == nil && len(pending.Requests) == 1 && pending.Requests[0].RequestID == card.RequestID
	})

	teamsList, err := h.client.ListTeams(ctx, &client.ListTeamsInput{})
	if err != nil || len(teamsList) != 1 || teamsList[0].State != "proposed" || teamsList[0].RunningCount != 0 {
		t.Fatalf("ListTeams with nobody answering = (%+v, %v), want one team still proposed with nothing running", teamsList, err)
	}
	if got := failureUXSessionCount(t, ctx, h.client); got != 1 {
		t.Fatalf("sessions = %d, want 1 (the lead only)", got)
	}
	assertLeadProposalUnanswered(t, ctx, h.client, lead.ID)
}

func assertLeadProposalUnanswered(t *testing.T, ctx context.Context, c *client.Client, leadID string) {
	t.Helper()
	page, err := c.TranscriptPage(ctx, leadID, 0, 100)
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	var proposalFound bool
	var userItems int
	for i := range page {
		switch page[i].Kind {
		case "team_proposal":
			proposalFound = true
			if page[i].Metadata.Decision != "pending" || page[i].Metadata.Decider != "" {
				t.Fatalf("lead proposal item = %+v, want pending with no decider", page[i])
			}
		case failureUXUser:
			userItems++
		case failureUXAssistant:
			if text := failureUXItemText(ctx, c, leadID, &page[i]); !strings.Contains(text, "waits for the user's decision in the approval inbox") {
				t.Fatalf("unexpected lead assistant item %q: the proposal must stay pending", text)
			}
		}
	}
	if !proposalFound || userItems != 1 {
		t.Fatalf("lead transcript proposalFound=%v userItems=%d, want true and 1", proposalFound, userItems)
	}
}
