package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/pkg/client"
)

const (
	failureUXRepoRoot    = "repo"
	failureUXMember      = "member"
	failureUXTaskQueued  = "queued"
	failureUXTaskRunning = "running"
	failureUXAssistant   = "assistant_message"
	failureUXUser        = "user_message"
)

// failureUXCtx bounds one scenario. It outlasts e2eCtx because a scenario
// starts vendor processes and git worktrees under the race detector.
func failureUXCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// failureUXRepo makes the git repository the scenario works in.
func failureUXRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	return repoDir
}

// startFailureUXLead creates a team-enabled workspace over the repository, starts a lead session in it and sends the scenario prompt.
func startFailureUXLead(t *testing.T, ctx context.Context, c *client.Client, repoDir, prompt string) (client.Workspace, client.Session) {
	t.Helper()
	ws := setupCrashTeamWorkspace(t, ctx, c, repoDir)
	sess, err := c.StartSession(ctx, "failux-start", client.VendorClaude, ws.ID, failureUXRepoRoot, client.SessionProfile{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sendErr := c.Send(ctx, "failux-send", sess.ID, prompt); sendErr != nil {
		t.Fatalf("Send %q: %v", prompt, sendErr)
	}
	return ws, sess
}

// approveFailureUXProposal approves the team proposal card the lead raised and
// returns the new team's ID once the host lists it.
func approveFailureUXProposal(t *testing.T, ctx context.Context, c *client.Client) string {
	t.Helper()
	proposal := awaitPendingProposalCard(t, ctx, c)
	answerFailureUXCard(t, ctx, c, &proposal, "approve")
	return awaitFailureUXTeam(t, ctx, c)
}

// answerFailureUXCard picks one option of the single "decision" question of a
// card and returns the host's answer error.
func answerFailureUXCard(t *testing.T, ctx context.Context, c *client.Client, req *client.PendingInteraction, option string) {
	t.Helper()
	if err := tryAnswerFailureUXCard(ctx, c, req, option); err != nil {
		t.Fatalf("answer %s with %s: %v", req.Tool, option, err)
	}
}

func tryAnswerFailureUXCard(ctx context.Context, c *client.Client, req *client.PendingInteraction, option string) error {
	_, err := c.AnswerInteraction(ctx, &client.AnswerInteractionInput{
		CommandID:   "failux-answer-" + option,
		RequestID:   req.RequestID,
		Incarnation: req.Incarnation,
		Outcome:     client.InteractionOutcomeAnswered,
		Answers:     []client.InteractionQuestionAnswer{{QuestionID: req.Questions[0].QuestionID, OptionIDs: []string{option}}},
	})
	if err != nil {
		return fmt.Errorf("answer the card: %w", err)
	}
	return nil
}

func awaitFailureUXTeam(t *testing.T, ctx context.Context, c *client.Client) string {
	t.Helper()
	var teamID string
	testutil.Spin(t, "team listed", func() bool {
		list, err := c.ListTeams(ctx, &client.ListTeamsInput{})
		if err != nil || len(list) == 0 {
			return false
		}
		teamID = list[0].TeamID
		return true
	})
	return teamID
}

// failureUXMembers returns the member tasks of the team in creation order.
func failureUXMembers(t *testing.T, ctx context.Context, c *client.Client, teamID string) (client.TeamView, []client.TaskNode) {
	t.Helper()
	tv, err := c.GetTeam(ctx, &client.GetTeamInput{TeamID: teamID})
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	var members []client.TaskNode
	for i := range tv.Tasks {
		if tv.Tasks[i].HierarchyRole == failureUXMember {
			members = append(members, tv.Tasks[i])
		}
	}
	return tv, members
}

// awaitFailureUXMembers waits until the member task states, in creation
// order, equal want.
func awaitFailureUXMembers(t *testing.T, ctx context.Context, c *client.Client, teamID string, want ...string) client.TeamView {
	t.Helper()
	var view client.TeamView
	testutil.Spin(t, "member states "+strings.Join(want, ","), func() bool {
		tv, err := c.GetTeam(ctx, &client.GetTeamInput{TeamID: teamID})
		if err != nil {
			return false
		}
		var got []string
		for i := range tv.Tasks {
			if tv.Tasks[i].HierarchyRole == failureUXMember {
				got = append(got, tv.Tasks[i].State)
			}
		}
		view = tv
		return strings.Join(got, ",") == strings.Join(want, ",")
	})
	return view
}

// awaitFailureUXText waits for a transcript item of the given kind whose text
// contains needle and returns that text.
func awaitFailureUXText(t *testing.T, ctx context.Context, c *client.Client, sessionID, kind, needle string) string {
	t.Helper()
	var found string
	testutil.Spin(t, kind+" containing "+needle, func() bool {
		page, err := c.TranscriptPage(ctx, sessionID, 0, 100)
		if err != nil {
			return false
		}
		for i := range page {
			if page[i].Kind != kind {
				continue
			}
			if text := failureUXItemText(ctx, c, sessionID, &page[i]); strings.Contains(text, needle) {
				found = text
				return true
			}
		}
		return false
	})
	return found
}

// failureUXItemText is the display text of one item, read in full when the
// vault is open.
func failureUXItemText(ctx context.Context, c *client.Client, sessionID string, item *client.TranscriptItem) string {
	detail, err := c.TranscriptItem(ctx, sessionID, item.ItemID)
	if err != nil {
		return item.View.Text
	}
	var msg struct {
		Text string `json:"Text"`
	}
	if json.Unmarshal(detail.Content, &msg) != nil || msg.Text == "" {
		return item.View.Text + string(detail.Content)
	}
	return msg.Text
}

func findFailureUXQuestion(t *testing.T, ctx context.Context, c *client.Client, tool string) client.PendingInteraction {
	t.Helper()
	var found client.PendingInteraction
	testutil.Spin(t, "pending "+tool+" card", func() bool {
		pending, err := c.ListPendingInteractions(ctx)
		if err != nil {
			return false
		}
		for i := range pending.Requests {
			if pending.Requests[i].Tool == tool {
				found = pending.Requests[i]
				return true
			}
		}
		return false
	})
	return found
}

// clockPump moves a fake clock forward in small steps so the host paths that
// wait on timers (process containment polls, the stop sequence) make progress
// while a scenario starts and stops vendor processes. A scenario pauses the
// pump to take the clock over, and resumes it before teardown.
type clockPump struct {
	clk    *clock.Fake
	paused atomic.Bool
}

const (
	clockPumpStep = 10 * time.Millisecond
	clockPumpTick = 2 * time.Millisecond
)

func startClockPump(t *testing.T, clk *clock.Fake) *clockPump {
	t.Helper()
	p := &clockPump{clk: clk}
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	task := supervise.Go(ctx, "test.clock-pump", func(ctx context.Context) error {
		for ctx.Err() == nil {
			if err := clock.Sleep(ctx, testutil.OSClock(), clockPumpTick); err == nil && !p.paused.Load() {
				clk.Advance(clockPumpStep)
			}
		}
		return nil
	})
	t.Cleanup(func() {
		cancel()
		if err := task.Wait(); err != nil {
			t.Errorf("clock pump: %v", err)
		}
	})
	return p
}
