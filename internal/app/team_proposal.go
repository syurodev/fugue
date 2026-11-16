package app

import (
	"context"
	"encoding/json"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

// teamProposeToolName is the MCP tool name the proposing session calls.
const teamProposeToolName = "fugue_team_propose"

// teamPendingDecision and decisionRejectedItem are the team_proposal item
// decisions F11.2a records: pending on propose, rejected when supersede or
// the session end cancels the proposal (Q-103, Q-104). F11.2b owns the other
// decision values.
const (
	teamPendingDecision  = "pending"
	decisionRejectedItem = "rejected"
)

// TeamProposalService receives team proposals from sessions (F11.2a). It
// validates against the workspace role catalog, stores the team in the
// proposed state, records the team_proposal transcript item and creates the
// inbox card. It never decides: a pending proposal stays pending until F11.2b
// decides it, and expiry or closing is never an approval.
//
// Proposal titles, briefs and goals are untrusted data (OR-13, §13): they go
// into sealed payloads and card content only, never into logs, errors or
// audit metadata.
type TeamProposalService struct {
	store        *vendorstore.Store
	recorder     approvalRecorder
	workspaces   *workspace.Service
	interactions *InteractionService
	usage        usageHistory
	ids          ids.Generator
}

func newTeamProposalService(store *vendorstore.Store, recorder approvalRecorder,
	workspaces *workspace.Service, interactions *InteractionService, usage usageHistory, gen ids.Generator,
) *TeamProposalService {
	return &TeamProposalService{
		store: store, recorder: recorder, workspaces: workspaces,
		interactions: interactions, usage: usage, ids: gen,
	}
}

// resolveProposingSession reads the session snapshot and its workspace's
// effective configuration, failing typed when the session is gone or the
// workspace has teams disabled (Q-105).
func (s *TeamProposalService) resolveProposingSession(ctx context.Context, sessionID string) (workspace.EffectiveConfig, vendorstore.Session, error) {
	snap, err := s.store.Snapshot(ctx, sessionID)
	if err != nil {
		return workspace.EffectiveConfig{}, vendorstore.Session{}, boundaryError("read the proposing session", err)
	}
	if snap.State == vendorstore.StateClosed {
		return workspace.EffectiveConfig{}, vendorstore.Session{}, errSessionEnded()
	}
	eff, _, err := s.workspaces.GetEffectiveConfig(ctx, snap.WorkspaceID, nil)
	if err != nil {
		return workspace.EffectiveConfig{}, vendorstore.Session{}, boundaryError("read the workspace configuration", err)
	}
	if !eff.Session.Teams {
		return workspace.EffectiveConfig{}, vendorstore.Session{}, errcode.New(errcode.FailedPrecondition, "teams are not enabled for this workspace")
	}
	return eff, snap, nil
}

// validateProposal runs the domain validation and maps a rejection to its
// boundary code; the wrap keeps the typed reason for errors.Is.
func (s *TeamProposalService) validateProposal(input *teams.ProposalInput, eff *workspace.EffectiveConfig) ([]teams.Warning, error) {
	warnings, err := teams.ValidateProposal(input, catalogRoles(eff))
	if err != nil {
		// Domain rejections carry stable codes only across the boundary.
		return nil, errcode.Wrap(errcode.InvalidArgument, "the proposal does not satisfy the team rules", err)
	}
	return warnings, nil
}

// boundaryError wraps another package's typed failure for this boundary,
// preserving its public code (errcode.Wrap is the repository's wrapping
// path). A nil error stays nil.
func boundaryError(what string, err error) error {
	if err == nil {
		return nil
	}
	return errcode.Wrap(errcode.CodeOf(err), what, err)
}

// teamsEnabled reads the workspace's effective [session].teams flag (Q-105).
// The flag is a policy key (Q-64), so an unconfirmed out-of-band change keeps
// the accepted value here.
func (s *TeamProposalService) teamsEnabled(ctx context.Context, workspaceID string) (bool, error) {
	eff, _, err := s.workspaces.GetEffectiveConfig(ctx, workspaceID, nil)
	if err != nil {
		return false, boundaryError("read the workspace configuration", err)
	}
	return eff.Session.Teams, nil
}

// Propose validates and stores one proposal for sessionID and returns its ID
// at once (OR-03). A pending proposal of the same session is superseded first
// (Q-103).
func (s *TeamProposalService) Propose(ctx context.Context, sessionID string, args *ProposeArgs) (string, error) {
	if args == nil {
		return "", errcode.New(errcode.InvalidArgument, "proposal arguments are required")
	}
	eff, snap, err := s.resolveProposingSession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	input := &teams.ProposalInput{Title: args.Title, Brief: args.Brief, Roles: args.Roles, Tasks: args.Tasks}
	warnings, warnErr := s.validateProposal(input, &eff)
	if warnErr != nil {
		return "", warnErr
	}
	if supErr := s.supersedePending(ctx, sessionID); supErr != nil {
		return "", supErr
	}
	teamID, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate the proposal ID", err)
	}
	teamInput, err := newTeamInput(teamID, sessionID, snap.WorkspaceID, input, s.ids)
	if err != nil {
		return "", err
	}
	if _, _, createErr := s.store.CreateTeam(ctx, teamInput); createErr != nil {
		return "", boundaryError("store the proposal", createErr)
	}
	if itemErr := s.recordProposalItem(ctx, sessionID, teamID, input); itemErr != nil {
		return "", itemErr
	}
	if err := s.ensureCard(ctx, &snap, teamID, input, warnings, &eff); err != nil {
		return "", err
	}
	return teamID, nil
}

// supersedePending rejects the session's pending proposal before a new one is
// created (Q-103).
func (s *TeamProposalService) supersedePending(ctx context.Context, sessionID string) error {
	pending, ok, err := s.store.PendingTeamForSession(ctx, sessionID)
	if err != nil {
		return boundaryError("read the pending proposal", err)
	}
	if !ok {
		return nil
	}
	return s.cancelPendingProposal(ctx, &pending, "superseded")
}

// CancelForSession cancels the session's pending proposal because its session
// ended (Q-104). It is idempotent: a proposal that is already decided or
// cancelled is left alone, and only non-terminal tasks are cancelled. The
// item flip goes through the actor while it still serves commands; a miss
// leaves the item pending, because the durable team state stays
// authoritative and expiry or closing is never an approval.
func (s *TeamProposalService) CancelForSession(ctx context.Context, sessionID string) error {
	pending, ok, err := s.store.PendingTeamForSession(ctx, sessionID)
	if err != nil {
		return boundaryError("read the pending proposal", err)
	}
	if !ok {
		return nil
	}
	itemErr := s.markItemRejected(ctx, pending.SessionID, pending.TeamID)
	if teamErr := s.cancelPendingTeam(ctx, &pending, "session ended"); teamErr != nil {
		return teamErr
	}
	return itemErr
}

// cancelPendingProposal settles tasks first, then appends TeamEnded (Q-84).
// Supersede updates the transcript item synchronously before any store
// append, so a failure there leaves the durable state untouched; the steps
// after it are each idempotent, so a retried cancel finishes a partially
// cancelled proposal.
func (s *TeamProposalService) cancelPendingProposal(ctx context.Context, pending *vendorstore.TeamRecord, note string) error {
	if pending.State != vendorstore.TeamStateProposed {
		return nil
	}
	if markErr := s.markItemRejected(ctx, pending.SessionID, pending.TeamID); markErr != nil {
		return markErr
	}
	return s.cancelPendingTeam(ctx, pending, note)
}

// cancelPendingTeam invalidates the card and settles the durable team: the
// card first, so supersede's own card finds the session's single pending
// slot free (Q-103), then each non-terminal task, then TeamEnded.
func (s *TeamProposalService) cancelPendingTeam(ctx context.Context, pending *vendorstore.TeamRecord, note string) error {
	// The card goes first: a cancelled proposal must not leave a pending
	// card behind.
	operation, err := interaction.NativeOperationKey(pending.TeamID, pending.TeamID, 1)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "key the proposal card", err)
	}
	if _, invalErr := s.interactions.owner.InvalidateOperation(operation); invalErr != nil {
		return boundaryError("invalidate the proposal card", invalErr)
	}
	_, tasks, err := s.store.TeamSnapshot(ctx, pending.TeamID)
	if err != nil {
		return boundaryError("read the proposal's tasks", err)
	}
	next := pending.LastSeq
	for i := range tasks {
		if taskStateTerminal(tasks[i].State) {
			continue
		}
		eventID, idErr := s.ids.NewID()
		if idErr != nil {
			return errcode.Wrap(errcode.Internal, "generate the task cancel event ID", idErr)
		}
		if next, err = s.store.AppendTeamEvent(ctx, pending.TeamID, next, &vendorstore.AppendTeamEventInput{
			EventID: eventID, TaskID: tasks[i].TaskID, EventType: vendorstore.EventTaskEnded,
			Outcome: string(vendorstore.TeamStateCancelled),
		}); err != nil {
			return boundaryError("cancel a proposal task", err)
		}
	}
	endID, err := s.ids.NewID()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "generate the team end event ID", err)
	}
	payload, err := json.Marshal(cancelDecisionPayload{Decision: decisionRejectedItem, Note: note})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal the cancel decision", err)
	}
	_, err = s.store.AppendTeamEvent(ctx, pending.TeamID, next, &vendorstore.AppendTeamEventInput{
		EventID: endID, EventType: vendorstore.EventTeamEnded,
		Outcome: string(vendorstore.TeamStateCancelled), ProtectedPayload: payload,
	})
	return boundaryError("end the cancelled proposal", err)
}

// cancelDecisionPayload is the sealed payload of a TeamEnded append that
// cancels a still-proposed team (F11.2a). F11.2b extends the vocabulary with
// the user decisions.
type cancelDecisionPayload struct {
	Decision string `json:"decision"`
	Note     string `json:"note,omitempty"`
}

// taskStateTerminal reports the terminal task states (§3.1); unknown is not
// terminal (Q-84).
func taskStateTerminal(state vendorstore.TaskState) bool {
	switch state {
	case vendorstore.TaskStateDone, vendorstore.TaskStateFailed,
		vendorstore.TaskStateStopped, vendorstore.TaskStateCancelled:
		return true
	case vendorstore.TaskStateQueued, vendorstore.TaskStateStarting,
		vendorstore.TaskStateRunning, vendorstore.TaskStateAsking,
		vendorstore.TaskStateChecking, vendorstore.TaskStateReview,
		vendorstore.TaskStateIntegrating, vendorstore.TaskStateUnknown:
		return false
	}
	return false
}

// markItemRejected flips the session's team_proposal item to rejected
// through the actor's single-writer journal. A missing item is skipped: the
// durable team state stays authoritative.
func (s *TeamProposalService) markItemRejected(ctx context.Context, sessionID, teamID string) error {
	itemID, itemErr := s.findProposalItem(ctx, sessionID, teamID)
	if itemErr != nil || itemID == "" {
		return itemErr
	}
	stored, content, readErr := s.store.ReadItem(ctx, sessionID, itemID)
	if readErr != nil {
		return boundaryError("read the proposal item", readErr)
	}
	metadata := stored.Metadata
	metadata.Decision = decisionRejectedItem
	recordErr := s.recorder.RecordEvent(ctx, sessionID, &vendorstore.Event{
		Type: vendorstore.TranscriptItemUpdated, TurnID: stored.TurnID,
		Item: &vendorstore.Item{
			ID: stored.ID, TurnID: stored.TurnID, Kind: stored.Kind,
			Revision: stored.Revision, Metadata: metadata, Content: content,
		},
	})
	return boundaryError("record the rejected proposal item", recordErr)
}

// findProposalItem scans the newest transcript pages for the proposal item of
// teamID; the item of a pending proposal is always among the most recent.
func (s *TeamProposalService) findProposalItem(ctx context.Context, sessionID, teamID string) (string, error) {
	before := int64(0)
	for range 2 {
		items, err := s.store.Transcript(ctx, sessionID, before, teamItemScanLimit)
		if err != nil {
			return "", boundaryError("read the session transcript", err)
		}
		if len(items) == 0 {
			return "", nil
		}
		for i := len(items) - 1; i >= 0; i-- {
			if items[i].Kind == vendorstore.KindTeamProposal && items[i].Metadata.ProposalID == teamID {
				return items[i].ID, nil
			}
		}
		before = items[0].Sequence
	}
	return "", nil
}

// catalogRoles converts the workspace catalog to the teams domain.
func catalogRoles(eff *workspace.EffectiveConfig) []teams.Role {
	out := make([]teams.Role, 0, len(eff.Roles))
	for i := range eff.Roles {
		r := &eff.Roles[i]
		out = append(out, teams.Role{
			Name: r.Name, Description: r.Description, Vendor: teams.Vendor(r.Vendor),
			Model: r.Model, Effort: teams.Effort(r.Effort), Permission: teams.Permission(r.Permission),
			IsLeadEligible: r.IsLeadEligible, Checks: r.Checks,
		})
	}
	return out
}

// newTeamInput maps the validated proposal onto the store's create input.
// The store keys tasks globally (team_tasks.task_id is unique), so the
// proposal's untrusted task IDs are namespaced under the proposal ID; the
// mapping is deterministic, and every in-team reference (parent and
// depends_on) uses the namespaced key. Goals, contexts, scopes and checks
// travel only in the sealed spec (INV-09).
func newTeamInput(teamID, sessionID, workspaceID string, input *teams.ProposalInput, gen ids.Generator) (*vendorstore.NewTeamInput, error) {
	proposedID, err := gen.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate the propose event ID", err)
	}
	out := &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: workspaceID, SessionID: sessionID, ProposedEventID: proposedID,
		Tasks: make([]vendorstore.NewTaskInput, 0, len(input.Tasks)),
	}
	for i := range input.Tasks {
		t := &input.Tasks[i]
		eventID, idErr := gen.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate a task event ID", idErr)
		}
		spec := t.TaskSpec()
		specJSON, marshalErr := json.Marshal(spec.ProtectedContent())
		if marshalErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "marshal the protected task spec", marshalErr)
		}
		dependsOn := make([]string, 0, len(t.DependsOn))
		for _, dep := range t.DependsOn {
			dependsOn = append(dependsOn, durableTaskID(teamID, dep))
		}
		out.Tasks = append(out.Tasks, vendorstore.NewTaskInput{
			TaskID:       durableTaskID(teamID, t.ID),
			ParentTaskID: durableTaskID(teamID, t.ParentTaskID), HierarchyRole: vendorstore.TeamRole(t.TeamRole),
			CatalogRole: t.Role, Deliverable: vendorstore.TeamDeliverable(t.Deliverable),
			DependsOn: dependsOn, CreatedEventID: eventID, ProtectedSpec: specJSON,
		})
	}
	return out, nil
}

// durableTaskID namespaces a proposal task ID under its team.
func durableTaskID(teamID, taskID string) string {
	if taskID == "" {
		return ""
	}
	return teamID + "." + taskID
}

// ProposeArgs is the fugue_team_propose input after decoding.
type ProposeArgs struct {
	Title string               `json:"title"`
	Brief string               `json:"brief"`
	Roles []teams.ProposedRole `json:"roles,omitempty"`
	Tasks []teams.ProposedTask `json:"tasks"`
}
