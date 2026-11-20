package app

// F11.2b decide path: the four §5.3 decisions of a team proposal card. The
// decision comes from the F11.2a inbox card through the interaction answer
// path; only a LOCAL principal can reach it (the Host API handler and the
// interaction service both refuse session principals), and the proposing
// session is never woken — delivery is F11.3 (Q-99).
//
// Proposal text, briefs, edits and notes are untrusted data (OR-13, §13):
// they travel in sealed payloads and card content only, never in logs,
// errors or audit metadata.

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

// The §5.3 card options and the decision values they produce.
const (
	decisionApprove       = "approve"
	decisionApproveEdited = "approve_edited"
	decisionSolo          = "solo"
	decisionReject        = "reject"

	// The team_proposal item decisions (transcript §3).
	itemDecisionApproved       = "approved"
	itemDecisionApprovedEdited = "approved_edited"
	itemDecisionSolo           = "solo"
)

// Who decided: the user on a card, or the workspace auto-approve setting.
const (
	decidedByUser = "user"
	decidedByAuto = "auto"
)

const (
	// teamDecisionQuestionID is the question ID of the proposal card.
	teamDecisionQuestionID = "decision"
	// teamProposedSequence is the fixed sequence of the TeamProposed event,
	// whose sealed payload carries the plan the edits re-validate against.
	teamProposedSequence = 1
	// autoApproveAgentLimit is the §5.3 agent cap of auto-approval (Q-102).
	autoApproveAgentLimit = 3
	// decisionApplyAttempts bounds the retries of the post-resolve apply on
	// a transient unavailable.
	decisionApplyAttempts = 3
)

// teamDecisionPayload is the sealed payload of the TeamStarted append. Roles
// carries the decided profile of every role, so F11.3 starts tasks with
// exactly what the user decided (OR-E13).
type teamDecisionPayload struct {
	Decision  string            `json:"decision"`
	DecidedBy string            `json:"decided_by"`
	Roles     []decidedRole     `json:"roles,omitempty"`
	Budget    *teams.TeamBudget `json:"budget,omitempty"`
}

// decidedRole is one role profile as decided on the card.
type decidedRole struct {
	Name       string `json:"name"`
	Vendor     string `json:"vendor"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
	Permission string `json:"permission"`
}

// validateTeamCardAnswer implements teamCardResolver. It runs before the card
// resolves, so an invalid decision fails typed and leaves the card
// answerable (F11.2b).
func (s *TeamProposalService) validateTeamCardAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	if ticket.Binding.ToolName == teamAmendToolName {
		return s.validateAmendAnswer(ctx, ticket, decision)
	}
	return s.validateProposalAnswer(ctx, ticket, decision)
}

// applyTeamCardAnswer implements teamCardResolver. It runs after the card
// resolved: a retried answer fails at the owner (already answered) and flips
// nothing, so deciding is idempotent under retry.
func (s *TeamProposalService) applyTeamCardAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	if ticket.Binding.ToolName == teamAmendToolName {
		return s.applyAmendAnswer(ctx, ticket, decision)
	}
	return s.applyProposalAnswer(ctx, ticket, decision)
}

// decisionAnswerOf extracts the chosen option and its text from the card's
// answer to questionID. A card resolved without an answer (cancelled,
// expired, unavailable) carries no decision and nothing is applied.
func decisionAnswerOf(questionID string, decision *interaction.Decision) (string, string, error) {
	option, text := "", ""
	for i := range decision.Answers {
		a := &decision.Answers[i]
		if a.QuestionID != questionID {
			continue
		}
		switch len(a.OptionIDs) {
		case 0:
			if a.Text != "" {
				return "", "", errcode.New(errcode.InvalidArgument, "the decision answer takes one option")
			}
		case 1:
			option, text = a.OptionIDs[0], a.Text
		default:
			return "", "", errcode.New(errcode.InvalidArgument, "the decision answer takes one option")
		}
	}
	return option, text, nil
}

// validateProposalAnswer pre-validates one proposal card answer. Only the
// edit document needs work before the card resolves: an invalid edit is
// refused with a typed error and the proposal stays pending with its card
// still answerable.
func (s *TeamProposalService) validateProposalAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	option, editsText, err := decisionAnswerOf(teamDecisionQuestionID, decision)
	if err != nil {
		return err
	}
	if option == "" {
		return nil
	}
	if !slices.Contains(teamDecisionOptions(), option) {
		return errcode.New(errcode.InvalidArgument, "the decision option was not offered")
	}
	if option != decisionApproveEdited {
		return nil
	}
	input, err := s.readProposalInput(ctx, ticket.Binding.InvocationID)
	if err != nil {
		return err
	}
	eff, _, err := s.workspaces.GetEffectiveConfig(ctx, ticket.Binding.WorkspaceID, nil)
	if err != nil {
		return boundaryError("read the workspace configuration", err)
	}
	_, err = s.applyEditSet(editsText, input, &eff)
	return err
}

// applyProposalAnswer applies one proposal card answer. The answer won the
// owner's once-only resolution, but supersede and the session-end cancel may
// still have settled the team in between: the apply re-checks under the
// session's keyed lock and fails closed without flipping state (F11.2b).
func (s *TeamProposalService) applyProposalAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	option, text, err := decisionAnswerOf(teamDecisionQuestionID, decision)
	if err != nil {
		return err
	}
	if option == "" {
		return nil
	}
	sessionID, teamID := ticket.Binding.SessionID, ticket.Binding.InvocationID
	unlock := s.locks.lock(sessionID)
	defer unlock()
	var applyErr error
	for range decisionApplyAttempts {
		eff, effErr := s.workspaceConfig(ctx, ticket.Binding.WorkspaceID)
		if effErr == nil {
			applyErr = s.applyDecisionLocked(ctx, sessionID, teamID, option, text, decidedByUser, &eff)
		} else {
			applyErr = effErr
		}
		if applyErr == nil || errcode.CodeOf(applyErr) != errcode.Unavailable {
			break
		}
	}
	return applyErr
}

// applyDecisionLocked applies one decided option with the session's keyed
// lock held. The team must still be proposed and owned by the session, so an
// answer that lost the race against supersede or the session-end cancel
// fails typed and flips nothing.
func (s *TeamProposalService) applyDecisionLocked(ctx context.Context, sessionID, teamID, option, text, decidedBy string, eff *workspace.EffectiveConfig) error {
	rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return boundaryError("read the proposed team", err)
	}
	if rec.SessionID != sessionID {
		return errcode.New(errcode.PermissionDenied, "the proposal belongs to another session")
	}
	if rec.State != vendorstore.TeamStateProposed {
		return errcode.New(errcode.FailedPrecondition, "the proposal is no longer pending")
	}
	input, err := s.readProposalInput(ctx, teamID)
	if err != nil {
		return err
	}
	switch option {
	case decisionApprove, decisionApproveEdited:
		var edited *teams.ProposalInput
		if option == decisionApproveEdited {
			if edited, err = s.applyEditSet(text, input, eff); err != nil {
				return err
			}
		}
		return s.approvePlan(ctx, &rec, leadTaskOf(tasks), input, edited, decidedBy, eff)
	case decisionSolo, decisionReject:
		return s.decideSoloOrReject(ctx, sessionID, teamID, leadTaskOf(tasks), option, text, decidedBy)
	default:
		return errcode.New(errcode.InvalidArgument, "the decision option was not offered")
	}
}

// approvePlan activates the team (Q-98): TeamStarted with the sealed decision
// payload, the edited specs re-sealed, removed tasks cancelled and added
// tasks inserted as queued tasks. The decision itself starts nothing: the
// onTeamActive hook hands the team to the F11.3a dispatcher, and the
// scheduler is F11.6. The item flip is best-effort afterwards: the
// durable decision is authoritative, and a failed flip is warned with its
// error code only (OR-13).
// decideSoloOrReject ends the still-proposed team with the user's decision
// (Q-84): the item flip comes first, like every cancel — a failed flip
// leaves the proposal fully pending instead of a settled team behind a
// pending-looking item — then every non-terminal task cancels, TeamEnded
// appends with the decision in the sealed payload, and the user's note is
// enqueued to the proposing session in the same transaction (OR-E13, Q-122).
func (s *TeamProposalService) decideSoloOrReject(ctx context.Context, sessionID, teamID, leadTaskID, option, text, decidedBy string) error {
	decisionValue := itemDecisionSolo
	payloadDecision := decisionSolo
	if option == decisionReject {
		decisionValue, payloadDecision = decisionRejectedItem, decisionRejectedItem
	}
	if err := s.flipItemDecision(ctx, sessionID, teamID, decisionValue); err != nil {
		return err
	}
	payload, marshalErr := json.Marshal(cancelDecisionPayload{Decision: payloadDecision, DecidedBy: decidedBy, Note: text})
	if marshalErr != nil {
		return errcode.Wrap(errcode.Internal, "marshal the decision payload", marshalErr)
	}
	rec, _, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return boundaryError("read the proposed team", err)
	}
	endID, endErr := s.ids.NewID()
	if endErr != nil {
		return errcode.Wrap(errcode.Internal, "generate the team end event ID", endErr)
	}
	if _, err := s.store.SettleTeam(ctx, teamID, &vendorstore.TeamSettleInput{
		ExpectedSeq: rec.LastSeq, EndEventID: endID, EndPayload: payload,
		Mailbox: soloDecisionMailbox(leadTaskID, payloadDecision, decidedBy, text),
	}); err != nil {
		return boundaryError("end the decided proposal", err)
	}
	s.notifyMailbox()
	return nil
}

func (s *TeamProposalService) approvePlan(ctx context.Context, rec *vendorstore.TeamRecord, leadTaskID string, input, edited *teams.ProposalInput, decidedBy string, eff *workspace.EffectiveConfig) error {
	plan := input
	itemDecision := itemDecisionApproved
	payloadDecision := decisionApprove
	if edited != nil {
		plan, itemDecision, payloadDecision = edited, itemDecisionApprovedEdited, decisionApproveEdited
	}
	payload, err := json.Marshal(teamDecisionPayload{
		Decision: payloadDecision, DecidedBy: decidedBy, Roles: decidedRolesOf(plan, eff), Budget: plan.Budget,
	})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal the decision payload", err)
	}
	mutations, err := s.decisionMutations(rec.TeamID, input, edited)
	if err != nil {
		return err
	}
	eventID, err := s.ids.NewID()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "generate the decision event ID", err)
	}
	deadlineSec := effectiveProposalTeamDeadlineSeconds(eff, plan.Budget)
	var armTimers []vendorstore.TeamTimerArmInput
	if deadlineSec > 0 {
		armTimers = []vendorstore.TeamTimerArmInput{{
			TeamID:     rec.TeamID,
			Kind:       vendorstore.TimerKindTeamDeadline,
			DeadlineMs: s.now().Add(time.Duration(deadlineSec) * time.Second).UnixMilli(),
		}}
	}
	if _, err := s.store.DecideTeam(ctx, rec.TeamID, &vendorstore.TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: eventID, StartedPayload: payload,
		RemovedTaskIDs: mutations.removed, ResealedSpecs: mutations.reseals, AddedTasks: mutations.added,
		Mailbox:   decisionMailboxRows(leadTaskID, payloadDecision, decidedBy, "", input, edited, mutations),
		ArmTimers: armTimers,
	}); err != nil {
		return boundaryError("apply the decision", err)
	}
	if s.onTimerArmed != nil && len(armTimers) > 0 {
		s.onTimerArmed()
	}
	if flipErr := s.flipItemDecision(ctx, rec.SessionID, rec.TeamID, itemDecision); flipErr != nil {
		slog.WarnContext(ctx, "recording the decided proposal item failed",
			"session_id", rec.SessionID, "error_code", string(errcode.CodeOf(flipErr)))
	}
	s.notifyMailbox()
	if s.onTeamActive != nil {
		s.onTeamActive(rec.TeamID)
	}
	return nil
}

// applyEditSet decodes the answer's edit document, applies it to the stored
// plan and re-validates the edited plan against the role catalog and the
// F11.1 rules. The document is untrusted data: its text never reaches an
// error or a log (OR-13).
func (s *TeamProposalService) applyEditSet(editsText string, input *teams.ProposalInput, eff *workspace.EffectiveConfig) (*teams.ProposalInput, error) {
	edit, err := teams.DecodeEditSet([]byte(editsText))
	if err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "the edits do not apply", err)
	}
	edited, err := teams.ApplyEdits(edit, input, catalogRoles(eff))
	if err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "the edits do not apply", err)
	}
	if _, err := teams.ValidateProposal(edited, catalogRoles(eff)); err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "the edited plan does not satisfy the team rules", err)
	}
	if teams.AgentCount(edited) > effectiveProposalAgentCap(eff, edited.Budget) {
		return nil, errcode.New(errcode.InvalidArgument, "the edited plan exceeds the team max_agents budget")
	}
	if err := refuseTaskIdentityChanges(input, edited); err != nil {
		return nil, err
	}
	return edited, nil
}

// flipItemDecision flips the proposal item through the actor's single-writer
// journal, retrying a transient unavailable; the flip is idempotent.
func (s *TeamProposalService) flipItemDecision(ctx context.Context, sessionID, teamID, decision string) error {
	var flipErr error
	for range decisionApplyAttempts {
		if flipErr = s.markItemDecision(ctx, sessionID, teamID, decision); flipErr == nil || errcode.CodeOf(flipErr) != errcode.Unavailable {
			return flipErr
		}
	}
	return flipErr
}

// readProposalInput reads the sealed plan of the TeamProposed event, so the
// edits re-validate against exactly what was proposed (F11.2b).
func (s *TeamProposalService) readProposalInput(ctx context.Context, teamID string) (*teams.ProposalInput, error) {
	payload, err := s.store.OpenTeamEventPayload(ctx, teamID, teamProposedSequence)
	if err != nil {
		return nil, boundaryError("open the sealed proposal plan", err)
	}
	var input teams.ProposalInput
	if unmarshalErr := json.Unmarshal(payload, &input); unmarshalErr != nil {
		return nil, errcode.New(errcode.FailedPrecondition, "the proposal plan is not readable for decisions")
	}
	return &input, nil
}

// teamDecisionOptions lists the §5.3 decision options in card order.
func teamDecisionOptions() []string {
	return []string{decisionApprove, decisionApproveEdited, decisionSolo, decisionReject}
}

// autoApprovable reports the §5.3 auto-approve conditions (Q-102, Q-111):
// the workspace setting is on, the proposal needs at most three agents, and
// every task role is a catalog role with read-only permission. Anything
// else waits for the user.
func (s *TeamProposalService) autoApprovable(eff *workspace.EffectiveConfig, input *teams.ProposalInput) bool {
	if !eff.Session.TeamsAutoApprove {
		return false
	}
	if teams.AgentCount(input) > autoApproveAgentLimit {
		return false
	}
	if input.Budget != nil && (input.Budget.MaxAgents > workspaceMaxTeamAgents(eff) || int(input.Budget.MaxDurationSeconds) > workspaceTeamDeadlineSeconds(eff)) {
		return false
	}
	catalog := make(map[string]teams.Role, len(eff.Roles))
	for i := range eff.Roles {
		catalog[eff.Roles[i].Name] = teams.Role{Permission: teams.Permission(eff.Roles[i].Permission)}
	}
	for i := range input.Tasks {
		role, ok := catalog[input.Tasks[i].Role]
		if !ok || role.Permission != teams.PermissionRead {
			return false
		}
	}
	return true
}

// workspaceConfig reads the workspace's effective configuration; it is a
// helper so both the propose path and the answer path share one read.
func (s *TeamProposalService) workspaceConfig(ctx context.Context, workspaceID string) (workspace.EffectiveConfig, error) {
	eff, _, err := s.workspaces.GetEffectiveConfig(ctx, workspaceID, nil)
	if err != nil {
		return workspace.EffectiveConfig{}, boundaryError("read the workspace configuration", err)
	}
	return eff, nil
}

// teamCardResolver applies the user's decision for a team proposal or amend
// card. Validation runs before the card resolves, so an invalid decision
// fails typed and leaves the card answerable; apply runs after the card
// resolved and must be idempotent under retry (F11.2b).
type teamCardResolver interface {
	validateTeamCardAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error
	applyTeamCardAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error
}

// SetTeamCardResolver wires the team card consumer. It is called once during
// startup, before any client can answer a card.
func (s *InteractionService) SetTeamCardResolver(r teamCardResolver) {
	s.teamCards = r
}

// isTeamCard reports whether the ticket is a team proposal or amend card.
func isTeamCard(ticket *interaction.Ticket) bool {
	return ticket.Binding.ToolName == teamProposeToolName || ticket.Binding.ToolName == teamAmendToolName
}
