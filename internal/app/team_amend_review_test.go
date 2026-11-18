package app

// F11.2b review-round evidence: the amend baseline accumulates, a mixed card
// amendment applies everything in one transaction, an ended session never
// applies an approved amendment, and an edit cannot leave a kept task's row
// stale.

import (
	"encoding/json"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const amendArgTaskBudgets = "task_budgets"

// roleByName finds a decided role by name.
func roleByName(t *testing.T, roles []decidedRole, name string) decidedRole {
	t.Helper()
	r := findDecidedRole(roles, name)
	if r == nil {
		t.Fatalf("decided roles %+v lack %s", roles, name)
	}
	return *r
}

// sealedSpec opens one task's sealed spec.
func sealedSpec(t *testing.T, f *teamProposalFixture, teamID, taskID string) teams.ProtectedTaskContent {
	t.Helper()
	raw, err := f.hp.vendorStore.OpenTaskSpec(t.Context(), teamID, durableTaskID(teamID, taskID))
	if err != nil {
		t.Fatalf("OpenTaskSpec: %v", err)
	}
	var content teams.ProtectedTaskContent
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatalf("unmarshal the sealed spec: %v", err)
	}
	return content
}

// TestSecondAmendKeepsTheFirstAmendsVendor pins the accumulating baseline:
// amend 1 changes the vendor, amend 2 changes only the model, and the vendor
// stays changed in both the decided roles and the re-sealed spec.
func TestSecondAmendKeepsTheFirstAmendsVendor(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	teamID := proposeApprovedTeam(t, f, "amend-twice")
	f.amendThroughTool(t, map[string]any{
		amendArgTeamID: teamID,
		amendArgRoles:  []map[string]any{{amendArgRoleName: teamTestRole, amendArgVendor: string(teams.VendorCodex)}},
	})
	f.amendThroughTool(t, map[string]any{
		amendArgTeamID: teamID,
		amendArgRoles:  []map[string]any{{amendArgRoleName: teamTestRole, amendArgModel: teamTestAmendModel}},
	})
	roles, err := f.rt.teamProposals.decidedRoles(t.Context(), teamID)
	if err != nil {
		t.Fatalf("decidedRoles: %v", err)
	}
	role := roleByName(t, roles, teamTestRole)
	if role.Vendor != string(teams.VendorCodex) || role.Model != teamTestAmendModel {
		t.Fatalf("role after two amends = %+v, want vendor codex and model %s", role, teamTestAmendModel)
	}
	spec := sealedSpec(t, f, teamID, teamTestTaskA)
	if spec.Profile == nil || spec.Profile.Vendor != teams.VendorCodex || spec.Profile.Model != teamTestAmendModel {
		t.Fatalf("sealed profile after two amends = %+v, want vendor codex and model %s", spec.Profile, teamTestAmendModel)
	}
}

// TestMixedCardAmendAppliesEveryPartAndRecordsTheAddedRole pins the card
// path: an approved amendment with role edits, budget edits, an added role and
// an added task lands them all, and the added role is part of the baseline.
func TestMixedCardAmendAppliesEveryPartAndRecordsTheAddedRole(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	teamID := proposeApprovedTeam(t, f, "amend-mixed")
	args := amendCardArgs(teamID)
	args[amendArgRoles] = []map[string]any{{amendArgRoleName: teamTestRole, amendArgModel: teamTestAmendModel}}
	args[amendArgTaskBudgets] = []map[string]any{{"task_id": durableTaskID(teamID, teamTestTaskA), "budget": map[string]any{"max_turns": 7}}}
	f.amendThroughTool(t, args)
	if answerErr := f.answerCard(t, "amend-mixed-approve", teamAmendToolName, teamAmendQuestionID, decisionApprove, ""); answerErr != nil {
		t.Fatalf("approve the mixed amend: %v", answerErr)
	}
	assertActiveTeamTasks(t, f, teamID, 4)
	roles, err := f.rt.teamProposals.decidedRoles(t.Context(), teamID)
	if err != nil {
		t.Fatalf("decidedRoles: %v", err)
	}
	if got := roleByName(t, roles, teamTestRole); got.Model != teamTestAmendModel {
		t.Fatalf("edited role = %+v, want model %s", got, teamTestAmendModel)
	}
	if got := roleByName(t, roles, teamTestWriterRole); got.Vendor != string(teams.VendorCodex) || got.Permission != string(teams.PermissionWrite) {
		t.Fatalf("added role = %+v, want codex write", got)
	}
	spec := sealedSpec(t, f, teamID, teamTestTaskA)
	if spec.Profile == nil || spec.Profile.Model != teamTestAmendModel || spec.Budget == nil || spec.Budget.MaxTurns != 7 {
		t.Fatalf("re-sealed spec = profile %+v budget %+v, want the amended model and budget", spec.Profile, spec.Budget)
	}
}

// TestApprovedAmendAfterSessionEndFailsClosed pins the ended-session rule of
// the card path: an approval that reaches the apply after its session ended
// changes nothing.
func TestApprovedAmendAfterSessionEndFailsClosed(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	teamID := proposeApprovedTeam(t, f, "amend-ended")
	if _, closeErr := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "amend-close", SessionID: f.session.ID}); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	var args AmendArgs
	raw, marshalErr := json.Marshal(amendCardArgs(teamID))
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := f.rt.teamProposals.approveAmendment(t.Context(), f.session.ID, teamID, &args); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("approve after session end = %v, want permission denied", err)
	}
	assertActiveTeamTasks(t, f, teamID, 3)
}

// TestEditCannotChangeAKeptTasksRow pins the identity rule: removing a task
// and adding one with the same ID but other dependencies is refused before
// the card resolves, and the proposal stays pending.
func TestEditCannotChangeAKeptTasksRow(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	readded := validTeamProposalArgs().Tasks[1]
	readded.DependsOn = []string{teamTestTaskB}
	edit, marshalErr := json.Marshal(teams.EditSet{
		Version: teams.EditSetVersion, RemoveTasks: []string{teamTestTaskA}, AddTasks: []teams.ProposedTask{readded},
	})
	if marshalErr != nil {
		t.Fatalf("marshal the edit: %v", marshalErr)
	}
	if answerErr := f.answerCard(t, "decide-readd", teamProposeToolName, teamDecisionQuestionID, decisionApproveEdited, string(edit)); errcode.CodeOf(answerErr) != errcode.InvalidArgument {
		t.Fatalf("re-added task error = %v, want invalid argument", answerErr)
	}
	if rec, _ := f.teamSnapshot(t, teamID); rec.State != vendorstore.TeamStateProposed {
		t.Fatalf("team state after the refused edit = %s, want proposed", rec.State)
	}
}
