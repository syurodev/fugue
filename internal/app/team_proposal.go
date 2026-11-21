package app

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
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
	// sessionEndFlipAttempts bounds the retries of the session-end item
	// flip on a transient unavailable.
	sessionEndFlipAttempts = 3
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
	// accounts renders the §5.3 card account line (F11.2b moved it here from
	// F11.2a); nil until the runtime wires it, which reads as unknown.
	accounts *AccountsService
	// onTeamActive runs after an approved decision made a team active; nil
	// leaves the tasks queued (the decide-path tests).
	onTeamActive func(teamID string)
	// onMailbox asks the F11.3b wake pump for a delivery pass after a
	// decision enqueued mail; nil leaves it to the next trigger.
	onMailbox func()
	// teamChannels registers the team:<id> channel topic of every proposal
	// (F11.10b); nil in tests that build the service directly.
	teamChannels *teamChannelTopics
	// sessionLive reports whether the session still holds its token; it
	// turns false inside endSession before the pending-proposal cancel
	// runs, which is what closes the propose-versus-session-end race
	// (Q-104).
	sessionLive func(sessionID string) bool
	// locks serializes propose and session-end cancel per session, so
	// Q-103's one-pending invariant holds when two tool calls race. No
	// goroutines and no package state: the map lives on this service and
	// each session keeps one small mutex for its lifetime.
	locks sessionLocks
	// settling tracks the session-end cancels in flight, so the session's
	// actor holds its close until the item flip has been journaled: the
	// store refuses writes to a closed session, and a flip that loses that
	// race could never land.
	settling sessionSettles
	// pendingAmends tracks the team amend cards waiting for the user
	// (F11.2b), so a new amend supersedes the old and the session-end
	// cancel invalidates them.
	pendingAmends amendCards
	clk           clock.Clock
	onTimerArmed  func()
}

// sessionSettles counts in-flight session-end cancels per session and hands
// out a channel that closes when the last one finishes.
type sessionSettles struct {
	mu      sync.Mutex
	pending map[string]*settleState
}

type settleState struct {
	done chan struct{}
	n    int
}

// begin registers one in-flight cancel and returns the func that settles it.
func (l *sessionSettles) begin(sessionID string) func() {
	l.mu.Lock()
	if l.pending == nil {
		l.pending = make(map[string]*settleState)
	}
	st := l.pending[sessionID]
	if st == nil {
		st = &settleState{done: make(chan struct{})}
		l.pending[sessionID] = st
	}
	st.n++
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		st.n--
		if st.n == 0 {
			close(st.done)
			delete(l.pending, sessionID)
		}
	}
}

// wait returns a channel that closes once no cancel is in flight for the
// session; it is nil when none is.
func (l *sessionSettles) wait(sessionID string) <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st := l.pending[sessionID]; st != nil {
		return st.done
	}
	return nil
}

// BeginSessionEnd registers the session-end cancel before its goroutine
// starts and returns the func the goroutine calls when it is done.
func (s *TeamProposalService) BeginSessionEnd(sessionID string) func() {
	return s.settling.begin(sessionID)
}

// SetAccounts wires the accounts service for the §5.3 card account line.
// It is called once during startup, after the accounts service exists.
func (s *TeamProposalService) SetAccounts(accounts *AccountsService) {
	s.accounts = accounts
}

// SetOnTeamActive installs the hook run after an approved team became active;
// the start path of F11.3a hangs on it. It is called once during startup.
func (s *TeamProposalService) SetOnTeamActive(fn func(teamID string)) {
	s.onTeamActive = fn
}

// SetOnMailbox installs the wake trigger of the mailbox delivery (F11.3b).
// It is called once during startup.
func (s *TeamProposalService) SetOnMailbox(fn func()) {
	s.onMailbox = fn
}

// SetClock binds the injected clock for arming team deadline timers (F11.6c).
func (s *TeamProposalService) SetClock(clk clock.Clock) { s.clk = clk }

// SetOnTimerArmed installs the notification fired after a durable timer is armed.
func (s *TeamProposalService) SetOnTimerArmed(fn func()) { s.onTimerArmed = fn }

func (s *TeamProposalService) now() time.Time {
	if s.clk != nil {
		return s.clk.Now()
	}
	return (clock.Real{}).Now()
}

// SessionEndSettled is nil when no cancel is in flight for the session, else
// a channel that closes when it is.
func (s *TeamProposalService) SessionEndSettled(sessionID string) <-chan struct{} {
	return s.settling.wait(sessionID)
}

// sessionLocks is a keyed mutex: one lock per session ID, created on first
// use, never contended across sessions.
type sessionLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// lock locks the session's mutex and returns its unlock.
func (l *sessionLocks) lock(sessionID string) func() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locks == nil {
		l.locks = make(map[string]*sync.Mutex)
	}
	mu, ok := l.locks[sessionID]
	if !ok {
		mu = &sync.Mutex{}
		l.locks[sessionID] = mu
	}
	mu.Lock()
	return mu.Unlock
}

// SetTeamChannels binds the channel topic registry; called once at startup.
func (s *TeamProposalService) SetTeamChannels(t *teamChannelTopics) {
	s.teamChannels = t
}

// registerChannelTopic publishes the team:<id> topic of a new proposal
// (F11.10b): the topic exists from the proposal onward, so a subscriber never
// needs to know when the team appeared.
func (s *TeamProposalService) registerChannelTopic(teamID string) {
	if s.teamChannels != nil {
		s.teamChannels.RegisterTeam(teamID)
	}
}

func newTeamProposalService(store *vendorstore.Store, recorder approvalRecorder,
	workspaces *workspace.Service, interactions *InteractionService, usage usageHistory,
	gen ids.Generator, sessionLive func(sessionID string) bool,
) *TeamProposalService {
	return &TeamProposalService{
		store: store, recorder: recorder, workspaces: workspaces,
		interactions: interactions, usage: usage, ids: gen, sessionLive: sessionLive,
	}
}

// resolveTeamsSession reads the session snapshot and its workspace's
// effective configuration, failing typed when the session is gone or the
// workspace has teams disabled (Q-105). The propose and amend paths share it.
func (s *TeamProposalService) resolveTeamsSession(ctx context.Context, sessionID string) (workspace.EffectiveConfig, vendorstore.Session, error) {
	snap, err := s.store.Snapshot(ctx, sessionID)
	if err != nil {
		return workspace.EffectiveConfig{}, vendorstore.Session{}, boundaryError("read the proposing session", err)
	}
	// The row may still read open while the session is ending (the close
	// journals after endSession), so the revoked token is the
	// authoritative ending signal here; the caller holds the session
	// lock, and the session-end cancel waits for it, so a proposal either
	// never exists or is cancelled by that cancel (Q-104).
	if snap.State == vendorstore.StateClosed || s.sessionLive != nil && !s.sessionLive(sessionID) {
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
	if err := teams.ValidateTeamBudget(input.Budget); err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "the proposal budget is invalid", err)
	}
	warnings, err := teams.ValidateProposal(input, catalogRoles(eff))
	if err != nil {
		// Domain rejections carry stable codes only across the boundary.
		return nil, errcode.Wrap(errcode.InvalidArgument, "the proposal does not satisfy the team rules", err)
	}
	maxAgents := workspaceMaxTeamAgents(eff)
	if teams.AgentCount(input) > maxAgents {
		return nil, errcode.New(errcode.InvalidArgument, "the proposal exceeds the team max_agents budget")
	}
	if input.Budget != nil && input.Budget.MaxAgents > 0 && teams.AgentCount(input) > input.Budget.MaxAgents {
		return nil, errcode.New(errcode.InvalidArgument, "the proposal exceeds its team budget max_agents")
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
	unlock := s.locks.lock(sessionID)
	defer unlock()
	eff, snap, err := s.resolveTeamsSession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	input := &teams.ProposalInput{
		Title: args.Title, Brief: args.Brief, Roles: args.Roles, Tasks: args.Tasks, Budget: args.Budget,
	}
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
	s.registerChannelTopic(teamID)
	if itemErr := s.recordProposalItem(ctx, sessionID, teamID, input); itemErr != nil {
		return "", itemErr
	}
	// Auto-approve (Q-102, Q-111): only when the workspace setting is on and
	// the proposal is at most three agents of catalog read roles. The
	// decision is recorded as decided by "auto" and leaves no pending card;
	// a failure falls back to the user's card, so nothing is decided
	// silently and nothing else waits for the user (§5.3).
	if s.autoApprovable(&eff, input) {
		if err := s.applyDecisionLocked(ctx, sessionID, teamID, decisionApprove, "", decidedByAuto, &eff); err == nil {
			return teamID, nil
		}
	}
	if err := s.ensureCard(ctx, &snap, teamID, input, warnings, &eff); err != nil {
		return "", err
	}
	return teamID, nil
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
// travel only in the sealed spec (INV-09), and the plan itself travels in
// the sealed TeamProposed payload so the decide path re-validates edits
// against exactly what was proposed (F11.2b).
func newTeamInput(teamID, sessionID, workspaceID string, input *teams.ProposalInput, gen ids.Generator) (*vendorstore.NewTeamInput, error) {
	proposedID, err := gen.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate the propose event ID", err)
	}
	proposedPayload, err := json.Marshal(input)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal the sealed proposal plan", err)
	}
	out := &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: workspaceID, SessionID: sessionID,
		ProposedEventID: proposedID, ProposedPayload: proposedPayload,
		Tasks: make([]vendorstore.NewTaskInput, 0, len(input.Tasks)),
	}
	for i := range input.Tasks {
		taskInput, taskErr := newTaskInputOf(teamID, &input.Tasks[i], gen)
		if taskErr != nil {
			return nil, taskErr
		}
		out.Tasks = append(out.Tasks, *taskInput)
	}
	return out, nil
}

// newTaskInputOf maps one proposal task onto the store's create and insert
// input, namespacing its ID and every in-team reference under the team.
// Goals, contexts, scopes and checks travel only in the sealed spec (INV-09).
func newTaskInputOf(teamID string, t *teams.ProposedTask, gen ids.Generator) (*vendorstore.NewTaskInput, error) {
	eventID, err := gen.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate a task event ID", err)
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
	return &vendorstore.NewTaskInput{
		TaskID:       durableTaskID(teamID, t.ID),
		ParentTaskID: durableTaskID(teamID, t.ParentTaskID), HierarchyRole: vendorstore.TeamRole(t.TeamRole),
		CatalogRole: t.Role, Deliverable: vendorstore.TeamDeliverable(t.Deliverable),
		DependsOn: dependsOn, CreatedEventID: eventID, ProtectedSpec: specJSON,
	}, nil
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
	Title  string               `json:"title"`
	Brief  string               `json:"brief"`
	Roles  []teams.ProposedRole `json:"roles,omitempty"`
	Tasks  []teams.ProposedTask `json:"tasks"`
	Budget *teams.TeamBudget    `json:"budget,omitempty"`
}
