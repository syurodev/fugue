package main

import (
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/pkg/client"
)

// TestFailureUXResultGateRefusalSurfacesOverARealHost is the "result gate
// refuses" half of the activation §3 "unknown task or result gate refusal"
// row (A-E05): a member that writes outside its write_scope is sent back once
// with the file list, and on the second report fails with failure code scope
// while the worktree diff stays readable for the delegator's decision.
func TestFailureUXResultGateRefusalSurfacesOverARealHost(t *testing.T) {
	t.Parallel()
	h := startE2EHostWith(t, &e2eHostOptions{bridge: true})
	ctx := failureUXCtx(t)
	_, _ = startFailureUXLead(t, ctx, h.client, failureUXRepo(t), "FAILUX_PROPOSE;members=1;scope=1;")
	teamID := approveFailureUXProposal(t, ctx, h.client)

	var member client.TaskNode
	testutil.Spin(t, "member failed by the result gate", func() bool {
		_, members := failureUXMembers(t, ctx, h.client, teamID)
		if len(members) != 1 || members[0].State != "failed" {
			return false
		}
		member = members[0]
		return true
	})
	if member.FailureCode != "scope" {
		t.Fatalf("member failure code = %q, want scope", member.FailureCode)
	}

	// The first refusal reached the member as the file list (the reason).
	bounce := awaitFailureUXText(t, ctx, h.client, member.SessionID, failureUXUser, failureUXBounceText)
	if !strings.Contains(bounce, failureUXOutside) {
		t.Fatalf("scope bounce text = %q, want it to name %s", bounce, failureUXOutside)
	}

	// The worktree diff is what the delegator reviews to continue, keep or cancel.
	tv, err := h.client.GetTeam(ctx, &client.GetTeamInput{TeamID: teamID, DiffTaskID: member.TaskID})
	if err != nil {
		t.Fatalf("GetTeam with the task diff: %v", err)
	}
	if !strings.Contains(tv.DiffText, failureUXOutside) {
		t.Fatalf("task diff = %q, want it to contain %s", tv.DiffText, failureUXOutside)
	}
}
