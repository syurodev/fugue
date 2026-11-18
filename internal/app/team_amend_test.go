package app

// F11.2b amend evidence: direct amendments apply at once, a new role or more
// agents waits for the user, and an approved amend really creates its queued
// tasks without starting anything.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// Shared argument-key literals of the amend tests.
const (
	amendArgTeamID     = "team_id"
	amendArgRoles      = "roles"
	amendArgRoleName   = "name"
	amendArgVendor     = "vendor"
	amendArgPermission = "permission"
	amendArgModel      = "model"
	amendArgAddRoles   = "add_roles"
	amendArgAddTasks   = "add_tasks"
	amendArgTaskID     = "id"
	amendArgParent     = "parent_task_id"
	amendArgTeamRole   = "team_role"
	amendArgGoal       = "goal"
	amendArgScope      = "write_scope"
	amendArgDeliver    = "deliverable"
)

// amendCardArgs builds the tool arguments of a card amend: a new role and
// one added member task.
func amendCardArgs(teamID string) map[string]any {
	return map[string]any{
		amendArgTeamID: teamID,
		amendArgAddRoles: []map[string]any{
			{amendArgRoleName: teamTestWriterRole, amendArgVendor: string(teams.VendorCodex), amendArgPermission: string(teams.PermissionWrite)},
		},
		amendArgAddTasks: []map[string]any{
			{
				amendArgTaskID: teamTestTaskC, amendArgParent: teamTestLeadTask, amendArgTeamRole: string(teams.RoleMember), "role": teamTestWriterRole,
				amendArgGoal: "Write the summary", amendArgScope: []string{"docs/summary.md"}, amendArgDeliver: string(teams.DeliverableReport),
			},
		},
	}
}

// amendThroughTool drives the real fugue_team_amend tool call path.
func (f *teamProposalFixture) amendThroughTool(t *testing.T, args map[string]any) string {
	t.Helper()
	raw, marshalErr := json.Marshal(args)
	if marshalErr != nil {
		t.Fatalf("marshal the amend args: %v", marshalErr)
	}
	result, callErr := f.tools.Call(t.Context(), f.binding, teamAmendToolName, raw)
	if callErr != nil {
		t.Fatalf("fugue_team_amend: %v", callErr)
	}
	if result.IsError {
		t.Fatalf("fugue_team_amend returned an error result: %s", result.Text)
	}
	return result.Text
}

// TestAmendNeedsTheUserForANewRoleOrMoreAgents pins OR-E16's amend half: an
// amendment within the decided roles and agent count applies at once through
// TeamAmended, while a new role or more agents creates a card and leaves the
// team running unchanged.
func TestAmendNeedsTheUserForANewRoleOrMoreAgents(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	teamID := proposeApprovedTeam(t, f, "amend-direct-approve")
	amendDirectProfile(t, f, teamID)
	requestCardAmend(t, f, teamID)
}

// amendDirectProfile applies a profile change inside the decided roles and
// asserts it landed at once: the team is still active with three tasks and
// the sealed spec of a member task carries the amended model.
func amendDirectProfile(t *testing.T, f *teamProposalFixture, teamID string) {
	t.Helper()
	text := f.amendThroughTool(t, map[string]any{
		amendArgTeamID: teamID,
		amendArgRoles:  []map[string]any{{amendArgRoleName: teamTestRole, amendArgModel: teamTestAmendModel}},
	})
	if text == "" {
		t.Fatal("the direct amend returned no result text")
	}
	assertTeamTasks(t, f, teamID, vendorstore.TeamStateActive, 3)
	raw, specErr := f.hp.vendorStore.OpenTaskSpec(t.Context(), teamID, durableTaskID(teamID, teamTestTaskA))
	if specErr != nil {
		t.Fatalf("OpenTaskSpec: %v", specErr)
	}
	var content teams.ProtectedTaskContent
	if unmarshalErr := json.Unmarshal(raw, &content); unmarshalErr != nil {
		t.Fatalf("unmarshal the sealed spec: %v", unmarshalErr)
	}
	if content.Profile == nil || content.Profile.Model != teamTestAmendModel {
		t.Fatalf("amended profile = %+v, want model %s", content.Profile, teamTestAmendModel)
	}
	events, err := f.hp.vendorStore.TeamEventsSince(t.Context(), teamID, 0, 100)
	if err != nil {
		t.Fatalf("TeamEventsSince: %v", err)
	}
	if !slicesContainsEvent(events, vendorstore.EventTeamAmended) {
		t.Fatalf("events = %+v, want a TeamAmended event", events)
	}
}

// requestCardAmend submits a new role and an added task and asserts they
// wait for the user with the team running unchanged.
func requestCardAmend(t *testing.T, f *teamProposalFixture, teamID string) {
	t.Helper()
	card := f.amendThroughTool(t, amendCardArgs(teamID))
	if !containsWaitText(card) {
		t.Fatalf("amend card result = %q, want the waiting-for-the-user notice", card)
	}
	if cards := f.pendingCardsTool(t, teamAmendToolName); len(cards) != 1 {
		t.Fatalf("pending amend cards = %d, want 1", len(cards))
	}
	assertTeamTasks(t, f, teamID, vendorstore.TeamStateActive, 3)
}

// assertTeamTasks asserts the team's state and task count.
func assertTeamTasks(t *testing.T, f *teamProposalFixture, teamID string, state vendorstore.TeamState, wantTasks int) {
	t.Helper()
	rec, tasks := f.teamSnapshot(t, teamID)
	if rec.State != state || len(tasks) != wantTasks {
		t.Fatalf("team = %s with %d tasks, want %s with %d", rec.State, len(tasks), state, wantTasks)
	}
}

// TestApprovedAmendCreatesQueuedTasksAndStartsNothing pins the insertion
// rule: an approved amend creates the added tasks as queued rows in the same
// transaction as its TeamAmended event, and nothing starts them (Q-98).
func TestApprovedAmendCreatesQueuedTasksAndStartsNothing(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	teamID := proposeApprovedTeam(t, f, "amend-team-approve")
	f.amendThroughTool(t, amendCardArgs(teamID))
	if answerErr := f.answerCard(t, "amend-approve", teamAmendToolName, teamAmendQuestionID, decisionApprove, ""); answerErr != nil {
		t.Fatalf("approve the amend: %v", answerErr)
	}
	rec, tasks := f.teamSnapshot(t, teamID)
	if rec.State != vendorstore.TeamStateActive {
		t.Fatalf("team state = %s, want active", rec.State)
	}
	if rec.TaskCount != 4 || len(tasks) != 4 {
		t.Fatalf("task count after the amend = %d rows / %d record, want 4", len(tasks), rec.TaskCount)
	}
	for i := range tasks {
		if tasks[i].State != vendorstore.TaskStateQueued {
			t.Fatalf("task %s state = %s, want queued", tasks[i].TaskID, tasks[i].State)
		}
	}
	events, err := f.hp.vendorStore.TeamEventsSince(t.Context(), teamID, 0, 100)
	if err != nil {
		t.Fatalf("TeamEventsSince: %v", err)
	}
	for i := range events {
		if events[i].EventType == vendorstore.EventTaskAssigned {
			t.Fatalf("task %s was assigned by the amend; nothing starts before F11.3", events[i].TaskID)
		}
	}
	assertSessionsCount(t, f, 1)
	if cards := f.pendingCardsTool(t, teamAmendToolName); len(cards) != 0 {
		t.Fatalf("pending amend cards after the decision = %d, want 0", len(cards))
	}
}

// containsWaitText reports whether the amend result tells the lead the
// amendment waits for the user — the tool result is the lead's only notice
// (no transcript item, no wake, Q-99).
func containsWaitText(text string) bool {
	return strings.Contains(text, "waits for the user")
}

// slicesContainsEvent reports whether the events include one of the type.
func slicesContainsEvent(events []vendorstore.RecordedTeamEvent, eventType vendorstore.TeamEventType) bool {
	return slices.ContainsFunc(events, func(ev vendorstore.RecordedTeamEvent) bool { return ev.EventType == eventType })
}
