package teams

// TeamState represents the lifecycle state of an orchestrated team (orchestration §3.2).
type TeamState string

// Defined orchestration §3.2 team lifecycle states.
const (
	TeamStateProposed    TeamState = "proposed"
	TeamStateActive      TeamState = "active"
	TeamStateFinalReview TeamState = "final_review"
	TeamStateDone        TeamState = "done"
	TeamStateCancelled   TeamState = "cancelled"
	TeamStateFailed      TeamState = "failed"
)

// AllTeamStates returns all 6 team states in canonical order.
func AllTeamStates() []TeamState {
	return []TeamState{
		TeamStateProposed,
		TeamStateActive,
		TeamStateFinalReview,
		TeamStateDone,
		TeamStateCancelled,
		TeamStateFailed,
	}
}

// IsValidTeamState reports whether s is one of the 6 defined team states.
func IsValidTeamState(s TeamState) bool {
	switch s {
	case TeamStateProposed, TeamStateActive, TeamStateFinalReview,
		TeamStateDone, TeamStateCancelled, TeamStateFailed:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether the team state is terminal.
func (s TeamState) IsTerminal() bool {
	return s == TeamStateDone || s == TeamStateCancelled || s == TeamStateFailed
}

// TeamEvent is a domain trigger applied to an existing team's state machine.
type TeamEvent string

// Supported team state machine events (§3.2).
const (
	TeamEventStart              TeamEvent = "start"
	TeamEventRequestFinalReview TeamEvent = "request_final_review"
	TeamEventRequestRevision    TeamEvent = "request_revision"
	TeamEventComplete           TeamEvent = "complete"
	TeamEventCancel             TeamEvent = "cancel"
	TeamEventFail               TeamEvent = "fail"
)

// AllTeamEvents returns every team transition event.
func AllTeamEvents() []TeamEvent {
	return []TeamEvent{
		TeamEventStart,
		TeamEventRequestFinalReview,
		TeamEventRequestRevision,
		TeamEventComplete,
		TeamEventCancel,
		TeamEventFail,
	}
}

// TeamTransitionKey indexes the team state transition table.
type TeamTransitionKey struct {
	From  TeamState
	Event TeamEvent
}

// TeamTransitionTable returns the explicit (state, event) -> nextState table.
func TeamTransitionTable() map[TeamTransitionKey]TeamState {
	return map[TeamTransitionKey]TeamState{
		{TeamStateProposed, TeamEventStart}:              TeamStateActive,
		{TeamStateProposed, TeamEventCancel}:             TeamStateCancelled,
		{TeamStateActive, TeamEventRequestFinalReview}:   TeamStateFinalReview,
		{TeamStateActive, TeamEventCancel}:               TeamStateCancelled,
		{TeamStateActive, TeamEventFail}:                 TeamStateFailed,
		{TeamStateFinalReview, TeamEventRequestRevision}: TeamStateActive,
		{TeamStateFinalReview, TeamEventComplete}:        TeamStateDone,
		{TeamStateFinalReview, TeamEventCancel}:          TeamStateCancelled,
		{TeamStateFinalReview, TeamEventFail}:            TeamStateFailed,
	}
}

// TransitionTeam validates and computes the next team state from the explicit transition table.
func TransitionTeam(from TeamState, event TeamEvent) (TeamState, error) {
	table := TeamTransitionTable()
	next, ok := table[TeamTransitionKey{From: from, Event: event}]
	if !ok {
		return "", &TransitionError{
			Entity: "team",
			From:   string(from),
			Event:  string(event),
			Reason: ErrInvalidTransition,
		}
	}
	return next, nil
}
