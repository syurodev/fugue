package app

// The amend card path of fugue_team_amend: a new role or more agents than
// the approved plan creates a small inbox card and waits for the user
// (OR-E16), with the same fail-closed rules as the proposal card. An
// approved amend really creates its queued tasks; the team keeps running
// unchanged meanwhile.

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// amendCards tracks the pending amend cards per team, so a new amend
// supersedes the old one (Q-103) and the session-end cancel invalidates them
// (fail closed). The interaction owner lives for one host incarnation, so a
// restart loses cards and entries together and a stale answer fails closed
// at the incarnation check.
type amendCards struct {
	mu     sync.Mutex
	byTeam map[string]*pendingAmend
}

// pendingAmend is one amend card waiting for the user.
type pendingAmend struct {
	sessionID   string
	amendID     string
	operationID string
	args        *AmendArgs
}

func (a *amendCards) put(teamID string, pa *pendingAmend) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byTeam == nil {
		a.byTeam = make(map[string]*pendingAmend, 1)
	}
	a.byTeam[teamID] = pa
}

func (a *amendCards) get(teamID string) *pendingAmend {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byTeam[teamID]
}

func (a *amendCards) clear(teamID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.byTeam, teamID)
}

// cancelForSession invalidates every pending amend card of the session; the
// amendments never apply once the session that asked for them has ended.
func (a *amendCards) cancelForSession(sessionID string, owner *interaction.Owner) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for teamID, pa := range a.byTeam {
		if pa.sessionID != sessionID {
			continue
		}
		if _, err := owner.InvalidateOperation(pa.operationID); err == nil {
			delete(a.byTeam, teamID)
		}
	}
}

// requestAmendCard creates the amend card for additions that need the user
// (OR-E16): a new role or more agents than the approved plan. The team keeps
// running unchanged meanwhile, and a new amend for the same team supersedes
// the pending card first (Q-103).
func (s *TeamProposalService) requestAmendCard(ctx context.Context, sessionID string, rec *vendorstore.TeamRecord, args *AmendArgs) (string, error) {
	if err := s.validateAmendAdditions(ctx, rec, args); err != nil {
		return "", err
	}
	amendID, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate the amend ID", err)
	}
	operation, err := interaction.NativeOperationKey(rec.TeamID, amendID, 1)
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "key the amend card", err)
	}
	if prev := s.pendingAmends.get(rec.TeamID); prev != nil {
		if _, invalErr := s.interactions.owner.InvalidateOperation(prev.operationID); invalErr != nil {
			return "", boundaryError("invalidate the superseded amend card", invalErr)
		}
	}
	spec := &interaction.Spec{
		OperationID: operation, Kind: interaction.KindQuestion,
		Summary: truncateCardText("Team amendment", interaction.MaxSummaryBytes),
		Body:    truncateCardText(strings.Join(amendCardLines(args), "\n"), interaction.MaxPromptBytes),
		Questions: []interaction.Question{{
			ID: teamAmendQuestionID, Question: "Should the team take this amendment?", Header: "team amendment",
			Options: []interaction.Option{
				{ID: decisionApprove, Label: "Approve", Description: "The amendment applies; added tasks are queued and nothing starts"},
				{ID: decisionReject, Label: "Reject", Description: "The team keeps running unchanged"},
			},
		}},
		Binding: interaction.Binding{
			SessionID: sessionID, WorkspaceID: rec.WorkspaceID,
			InvocationID: rec.TeamID, AttemptID: amendID, CheckGeneration: 1,
			ToolName: teamAmendToolName,
		},
	}
	if _, err := s.interactions.Ensure(spec); err != nil {
		return "", err
	}
	s.pendingAmends.put(rec.TeamID, &pendingAmend{sessionID: sessionID, amendID: amendID, operationID: operation, args: args})
	return "the amendment waits for the user in the approval inbox; the team keeps running unchanged", nil
}

// amendCardLines renders the amend card body: identifiers and counts only.
// Goals, contexts and scopes stay in the sealed specs (§13).
func amendCardLines(args *AmendArgs) []string {
	lines := make([]string, 0, 1+len(args.AddRoles)+len(args.AddTasks)+1)
	lines = append(lines, "amendment")
	for i := range args.AddRoles {
		r := &args.AddRoles[i]
		line := "+ role " + r.Name + " (" + string(r.Vendor)
		if r.Model != "" {
			line += " " + r.Model
		}
		line += " " + string(r.Permission) + ")"
		lines = append(lines, line)
	}
	for i := range args.AddTasks {
		t := &args.AddTasks[i]
		line := "+ task " + t.ID + " (role " + t.Role
		if t.ParentTaskID != "" {
			line += ", under " + t.ParentTaskID
		}
		line += ")"
		lines = append(lines, line)
	}
	lines = append(lines, "agents: +"+strconv.Itoa(len(args.AddTasks)))
	return lines
}

// validateAmendAdditions validates the additions of an amend against the
// team's current tasks and the decided roles: the F11.1 tree rules over the
// combined plan, and every referenced role decided or added by this amend.
func (s *TeamProposalService) validateAmendAdditions(ctx context.Context, rec *vendorstore.TeamRecord, args *AmendArgs) error {
	decided, err := s.decidedRoles(ctx, rec.TeamID)
	if err != nil {
		return err
	}
	resolved := make(map[string]struct{}, len(decided)+len(args.AddRoles))
	for i := range decided {
		resolved[decided[i].Name] = struct{}{}
	}
	for i := range args.AddRoles {
		role := &args.AddRoles[i]
		if roleErr := teams.ValidateRole(&teams.Role{Name: role.Name, Vendor: role.Vendor, Model: role.Model, Effort: role.Effort, Permission: role.Permission}); roleErr != nil {
			return errcode.Wrap(errcode.CodeOf(roleErr), "the added role is invalid", roleErr)
		}
		resolved[role.Name] = struct{}{}
	}
	if len(args.AddTasks) == 0 {
		return nil
	}
	teamTasks, err := s.teamTasks(ctx, rec.TeamID)
	if err != nil {
		return err
	}
	nodes := make([]teams.TaskNode, 0, len(teamTasks)+len(args.AddTasks))
	for i := range teamTasks {
		nodes = append(nodes, teams.TaskNode{
			ID: proposalTaskID(rec.TeamID, teamTasks[i].TaskID), ParentTaskID: proposalTaskID(rec.TeamID, teamTasks[i].ParentTaskID),
			Role: teams.TeamRole(teamTasks[i].HierarchyRole),
		})
	}
	for i := range args.AddTasks {
		t := &args.AddTasks[i]
		if _, known := resolved[t.Role]; !known {
			return errcode.New(errcode.InvalidArgument, "the added task names a role the team does not have")
		}
		spec := t.TaskSpec()
		if err := teams.ValidateTaskSpec(&spec); err != nil {
			return errcode.Wrap(errcode.CodeOf(err), "the added task does not satisfy the team rules", err)
		}
		nodes = append(nodes, teams.TaskNode{ID: t.ID, ParentTaskID: t.ParentTaskID, Role: t.TeamRole})
	}
	if err := teams.ValidateTeamTree(nodes); err != nil {
		return errcode.Wrap(errcode.CodeOf(err), "the amended team does not satisfy the team rules", err)
	}
	return validateAmendDependencies(rec, teamTasks, args)
}

// validateAmendDependencies checks the added tasks' depends_on against the
// combined plan, using the proposal-level IDs of the existing tasks.
func validateAmendDependencies(rec *vendorstore.TeamRecord, teamTasks []vendorstore.TaskRecord, args *AmendArgs) error {
	known := make(map[string]struct{}, len(teamTasks)+len(args.AddTasks))
	for i := range teamTasks {
		known[proposalTaskID(rec.TeamID, teamTasks[i].TaskID)] = struct{}{}
	}
	for i := range args.AddTasks {
		known[args.AddTasks[i].ID] = struct{}{}
	}
	for i := range args.AddTasks {
		t := &args.AddTasks[i]
		for _, dep := range t.DependsOn {
			if _, ok := known[dep]; !ok {
				return errcode.New(errcode.InvalidArgument, "the added task depends on a task the team does not have")
			}
		}
	}
	return nil
}

// proposalTaskID strips the team namespace back off a durable task ID.
func proposalTaskID(teamID, taskID string) string {
	return strings.TrimPrefix(taskID, teamID+".")
}
