package app

// The direct apply path of fugue_team_amend: amendments within the decided
// roles and agent count (§5.4). Role profile changes and budget edits are
// validated against the decided plan (OR-E13) and land in one TeamAmended
// transaction with the re-sealed specs of the tasks they change.

import (
	"context"
	"encoding/json"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// applyDirectAmend applies amendments within the decided roles and agent
// count: role profile changes re-seal the affected task specs and everything
// lands in one TeamAmended transaction. A role edit that names a role the
// decided plan does not have is refused: a new role needs the user.
func (s *TeamProposalService) applyDirectAmend(ctx context.Context, rec *vendorstore.TeamRecord, args *AmendArgs, decided []decidedRole) (string, error) {
	resolved := make(map[string]teams.Role, len(decided))
	for i := range decided {
		r := &decided[i]
		resolved[r.Name] = teams.Role{Name: r.Name, Vendor: teams.Vendor(r.Vendor), Model: r.Model, Effort: teams.Effort(r.Effort), Permission: teams.Permission(r.Permission)}
	}
	overrides, newRoles, editErr := teams.ValidateRoleEdits(args.Roles, resolved)
	if editErr != nil {
		return "", errcode.Wrap(errcode.CodeOf(editErr), "the amendment does not apply", editErr)
	}
	if len(newRoles) > 0 {
		return "", errcode.New(errcode.InvalidArgument, "a new role waits for the user's approval")
	}
	if budgetErr := s.validateBudgetEdits(ctx, args.TaskBudgets, rec); budgetErr != nil {
		return "", budgetErr
	}
	reseals, err := s.amendedSpecs(ctx, rec, overrides, args.TaskBudgets)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(teamAmendPayload{RoleEdits: amendedRolePayloads(args.Roles, resolved)})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "marshal the amendment payload", err)
	}
	eventID, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate the amend event ID", err)
	}
	if _, err := s.store.AmendTeam(ctx, rec.TeamID, &vendorstore.TeamAmendInput{
		ExpectedSeq: rec.LastSeq, AmendEventID: eventID, AmendPayload: payload, ResealedSpecs: reseals,
	}); err != nil {
		return "", boundaryError("apply the amendment", err)
	}
	return "the amendment is applied; the team keeps running with the changed profiles", nil
}

// amendedRolePayloads records the amended profile of every edited role in the
// sealed payload, so the decided plan stays readable (OR-E13).
func amendedRolePayloads(edits []teams.RoleEdit, resolved map[string]teams.Role) []decidedRole {
	out := make([]decidedRole, 0, len(edits))
	for i := range edits {
		role, ok := resolved[edits[i].Name]
		if !ok {
			continue
		}
		vendor, model, effort := role.Vendor, role.Model, role.Effort
		if edits[i].Vendor != "" {
			vendor = edits[i].Vendor
		}
		if edits[i].Model != "" {
			model = edits[i].Model
		}
		if edits[i].Effort != "" {
			effort = edits[i].Effort
		}
		out = append(out, decidedRole{
			Name: role.Name, Vendor: string(vendor), Model: model, Effort: string(effort), Permission: string(role.Permission),
		})
	}
	return out
}

// validateBudgetEdits checks the budget edits name team tasks and carry
// non-negative values.
func (s *TeamProposalService) validateBudgetEdits(ctx context.Context, edits []teams.TaskBudgetEdit, rec *vendorstore.TeamRecord) error {
	tasks := make(map[string]struct{}, rec.TaskCount)
	for i := range edits {
		e := &edits[i]
		if e.TaskID == "" {
			return errcode.New(errcode.InvalidArgument, "the budget edit names a task")
		}
		tasks[e.TaskID] = struct{}{}
		if err := teams.ValidateBudget(&e.Budget); err != nil {
			return errcode.Wrap(errcode.CodeOf(err), "the budget edit does not apply", err)
		}
	}
	if len(tasks) == 0 {
		return nil
	}
	teamTasks, err := s.teamTasks(ctx, rec.TeamID)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(teamTasks))
	for i := range teamTasks {
		known[teamTasks[i].TaskID] = struct{}{}
	}
	for id := range tasks {
		if _, ok := known[id]; !ok {
			return errcode.New(errcode.NotFound, "the budget edit names a task the team does not have")
		}
	}
	return nil
}

// amendedSpecs reads the sealed spec of every task an amendment changes and
// returns its re-sealed content: the edited role profile and budget are part
// of the sealed spec (OR-E13).
func (s *TeamProposalService) amendedSpecs(ctx context.Context, rec *vendorstore.TeamRecord,
	overrides map[string]teams.ProfileOverride, budgets []teams.TaskBudgetEdit,
) ([]vendorstore.ResealedTaskSpec, error) {
	if len(overrides) == 0 && len(budgets) == 0 {
		return nil, nil
	}
	teamTasks, err := s.teamTasks(ctx, rec.TeamID)
	if err != nil {
		return nil, err
	}
	budgetByTask := make(map[string]teams.TaskBudget, len(budgets))
	for i := range budgets {
		budgetByTask[budgets[i].TaskID] = budgets[i].Budget
	}
	out := make([]vendorstore.ResealedTaskSpec, 0, len(teamTasks))
	for i := range teamTasks {
		t := &teamTasks[i]
		override, roleEdited := overrides[t.CatalogRole]
		budget, budgetEdited := budgetByTask[t.TaskID]
		if !roleEdited && !budgetEdited {
			continue
		}
		specJSON, specErr := s.amendedTaskSpec(ctx, rec.TeamID, t.TaskID, override, roleEdited, budget, budgetEdited)
		if specErr != nil {
			return nil, specErr
		}
		out = append(out, vendorstore.ResealedTaskSpec{TaskID: t.TaskID, ProtectedSpec: specJSON})
	}
	return out, nil
}

// amendedTaskSpec re-seals one changed task's spec with its amended profile
// and budget (OR-E13: the decided model is part of the sealed spec).
func (s *TeamProposalService) amendedTaskSpec(ctx context.Context, teamID, taskID string,
	override teams.ProfileOverride, roleEdited bool, budget teams.TaskBudget, budgetEdited bool,
) ([]byte, error) {
	spec, err := s.openTaskContent(ctx, teamID, taskID)
	if err != nil {
		return nil, err
	}
	if roleEdited {
		p := override
		spec.Profile = &p
	}
	if budgetEdited {
		b := budget
		spec.Budget = &b
	}
	specJSON, marshalErr := json.Marshal(spec)
	if marshalErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal the amended task spec", marshalErr)
	}
	return specJSON, nil
}

// openTaskContent decrypts one task's sealed spec content.
func (s *TeamProposalService) openTaskContent(ctx context.Context, teamID, taskID string) (*teams.ProtectedTaskContent, error) {
	raw, err := s.store.OpenTaskSpec(ctx, teamID, taskID)
	if err != nil {
		return nil, boundaryError("open the sealed task spec", err)
	}
	var content teams.ProtectedTaskContent
	if unmarshalErr := json.Unmarshal(raw, &content); unmarshalErr != nil {
		return nil, errcode.New(errcode.FailedPrecondition, "the sealed task spec is not readable")
	}
	return &content, nil
}

// teamTasks reads a team's task rows.
func (s *TeamProposalService) teamTasks(ctx context.Context, teamID string) ([]vendorstore.TaskRecord, error) {
	_, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return nil, boundaryError("read the team's tasks", err)
	}
	return tasks, nil
}

// decidedRoles reads the decided plan's role profiles from the sealed
// TeamStarted payload (OR-E13), so an amend validates against exactly what
// the user approved.
func (s *TeamProposalService) decidedRoles(ctx context.Context, teamID string) ([]decidedRole, error) {
	events, err := s.store.TeamEventsSince(ctx, teamID, 0, 200)
	if err != nil {
		return nil, boundaryError("read the team's events", err)
	}
	started := int64(-1)
	for i := range events {
		if events[i].EventType == vendorstore.EventTeamStarted {
			started = events[i].Sequence
			break
		}
	}
	if started < 0 {
		return nil, errcode.New(errcode.FailedPrecondition, "the decided plan is not readable")
	}
	payload, err := s.store.OpenTeamEventPayload(ctx, teamID, started)
	if err != nil {
		return nil, boundaryError("open the decided plan", err)
	}
	var decision teamDecisionPayload
	if unmarshalErr := json.Unmarshal(payload, &decision); unmarshalErr != nil {
		return nil, errcode.New(errcode.FailedPrecondition, "the decided plan is not readable")
	}
	return decision.Roles, nil
}
