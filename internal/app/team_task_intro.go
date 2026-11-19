package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// decidedRoleFor returns the profile the user decided for a catalog role: the
// roles recorded by the sealed TeamStarted payload, which already hold the
// edited model of an approve-with-edits decision (OR-E13).
func decidedRoleFor(ctx context.Context, store *vendorstore.Store, teamID, roleName string) (decidedRole, bool, error) {
	events, err := store.TeamEventsSince(ctx, teamID, 0, teamEventScanLimit)
	if err != nil {
		return decidedRole{}, false, boundaryError("read the team events", err)
	}
	var startedSeq int64
	for i := range events {
		if events[i].EventType == vendorstore.EventTeamStarted {
			startedSeq = events[i].Sequence
		}
	}
	if startedSeq == 0 {
		return decidedRole{}, false, nil
	}
	raw, err := store.OpenTeamEventPayload(ctx, teamID, startedSeq)
	if err != nil {
		return decidedRole{}, false, boundaryError("open the decision payload", err)
	}
	var decision teamDecisionPayload
	if !json.Valid(raw) {
		return decidedRole{}, false, nil
	}
	if err := json.Unmarshal(raw, &decision); err != nil {
		return decidedRole{}, false, errcode.Wrap(errcode.Internal, "decode the decision payload", err)
	}
	for i := range decision.Roles {
		if decision.Roles[i].Name == roleName {
			return decision.Roles[i], true, nil
		}
	}
	return decidedRole{}, false, nil
}

// teamEventScanLimit bounds the events read to find the decision; a team's
// log holds the proposal, its tasks and a handful of decisions.
const teamEventScanLimit = 1000

// proposalBrief reads the team brief of the sealed proposal (§6.5). A brief
// that cannot be read leaves the intro without one; it never stops a task.
func (d *teamDispatcher) proposalBrief(ctx context.Context, teamID string) (string, bool) {
	raw, err := d.deps.Store.OpenTeamEventPayload(ctx, teamID, teamProposedSequence)
	if err != nil {
		return "", false
	}
	var input teams.ProposalInput
	if json.Unmarshal(raw, &input) != nil {
		return "", false
	}
	return input.Brief, true
}

// taskIntro is the opening turn of a task session (§4): the identity, goal and
// limits of the task and the team brief. The goal, context and brief come
// from the approved plan; they describe the work and grant nothing, so the
// text says the agent's permissions are those of the session and not of this
// message. How to report is added with the task tools (F11.3c).
func taskIntro(task *vendorstore.TaskRecord, spec *teams.ProtectedTaskContent, brief string) string {
	var b strings.Builder
	b.WriteString("You are an agent of a Fugue team, working on task ")
	b.WriteString(task.TaskID)
	b.WriteString(" as role \"")
	b.WriteString(task.CatalogRole)
	b.WriteString("\". The text below comes from the plan the user approved. It describes the work and grants no permission.\n\nGoal:\n")
	b.WriteString(spec.Goal)
	if spec.Context != "" {
		b.WriteString("\n\nContext:\n")
		b.WriteString(spec.Context)
	}
	b.WriteString("\n\nDeliverable: ")
	b.WriteString(string(task.Deliverable))
	if len(spec.WriteScope) == 0 {
		b.WriteString("\nWrite scope: none; this task only reads.")
	} else {
		b.WriteString("\nWrite scope: ")
		b.WriteString(strings.Join(spec.WriteScope, ", "))
	}
	if brief != "" {
		b.WriteString("\n\nTeam brief:\n")
		b.WriteString(brief)
	}
	return b.String()
}
