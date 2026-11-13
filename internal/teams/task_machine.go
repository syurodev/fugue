package teams

// TaskState represents the lifecycle state of a team task (orchestration §3.1).
type TaskState string

// Defined orchestration §3.1 task lifecycle states.
const (
	TaskStateQueued      TaskState = "queued"
	TaskStateStarting    TaskState = "starting"
	TaskStateRunning     TaskState = "running"
	TaskStateAsking      TaskState = "asking"
	TaskStateChecking    TaskState = "checking"
	TaskStateReview      TaskState = "review"
	TaskStateIntegrating TaskState = "integrating"
	TaskStateDone        TaskState = "done"
	TaskStateFailed      TaskState = "failed"
	TaskStateStopped     TaskState = "stopped"
	TaskStateCancelled   TaskState = "cancelled"
	TaskStateUnknown     TaskState = "unknown"
)

// AllTaskStates returns all 12 task states in canonical order.
func AllTaskStates() []TaskState {
	return []TaskState{
		TaskStateQueued,
		TaskStateStarting,
		TaskStateRunning,
		TaskStateAsking,
		TaskStateChecking,
		TaskStateReview,
		TaskStateIntegrating,
		TaskStateDone,
		TaskStateFailed,
		TaskStateStopped,
		TaskStateCancelled,
		TaskStateUnknown,
	}
}

// IsValidTaskState reports whether s is one of the 12 defined task states.
func IsValidTaskState(s TaskState) bool {
	switch s {
	case TaskStateQueued, TaskStateStarting, TaskStateRunning, TaskStateAsking,
		TaskStateChecking, TaskStateReview, TaskStateIntegrating,
		TaskStateDone, TaskStateFailed, TaskStateStopped, TaskStateCancelled,
		TaskStateUnknown:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether s is a terminal task state.
// Note that unknown is non-terminal: only an explicit decision leaves unknown.
func (s TaskState) IsTerminal() bool {
	return s == TaskStateDone || s == TaskStateFailed || s == TaskStateStopped || s == TaskStateCancelled
}

// NeedsUnknownOnRestart reports whether OR-15 requires moving s to unknown on host restart.
func (s TaskState) NeedsUnknownOnRestart() bool {
	return s == TaskStateRunning || s == TaskStateStarting || s == TaskStateAsking || s == TaskStateChecking
}

// TaskEvent is a domain trigger applied to an existing task's state machine.
type TaskEvent string

// Supported task state machine events (§3.1, OR-15).
const (
	TaskEventAssign         TaskEvent = "assign"
	TaskEventStart          TaskEvent = "start"
	TaskEventAsk            TaskEvent = "ask"
	TaskEventAnswer         TaskEvent = "answer"
	TaskEventReport         TaskEvent = "report"
	TaskEventChecksPassed   TaskEvent = "checks_passed"
	TaskEventChecksFailed   TaskEvent = "checks_failed"
	TaskEventApprove        TaskEvent = "approve"
	TaskEventRequestChanges TaskEvent = "request_changes"
	TaskEventCompleteNoDiff TaskEvent = "complete_no_diff"
	TaskEventIntegrate      TaskEvent = "integrate"
	TaskEventStop           TaskEvent = "stop"
	TaskEventFail           TaskEvent = "fail"
	TaskEventCancel         TaskEvent = "cancel"
	TaskEventHostRestart    TaskEvent = "host_restart"
	TaskEventDecisionResume TaskEvent = "decision_resume"
	TaskEventDecisionKeep   TaskEvent = "decision_keep_diff"
	TaskEventDecisionCancel TaskEvent = "decision_cancel"
)

// AllTaskEvents returns every task transition event.
func AllTaskEvents() []TaskEvent {
	return []TaskEvent{
		TaskEventAssign,
		TaskEventStart,
		TaskEventAsk,
		TaskEventAnswer,
		TaskEventReport,
		TaskEventChecksPassed,
		TaskEventChecksFailed,
		TaskEventApprove,
		TaskEventRequestChanges,
		TaskEventCompleteNoDiff,
		TaskEventIntegrate,
		TaskEventStop,
		TaskEventFail,
		TaskEventCancel,
		TaskEventHostRestart,
		TaskEventDecisionResume,
		TaskEventDecisionKeep,
		TaskEventDecisionCancel,
	}
}

// TaskTransitionInput supplies the state, event, and structural guards for TransitionTask.
type TaskTransitionInput struct {
	From             TaskState
	Event            TaskEvent
	Role             TeamRole
	ChildrenTerminal bool
	FailureCode      string
}

// TaskTransitionKey indexes the task state transition table.
type TaskTransitionKey struct {
	From  TaskState
	Event TaskEvent
}

// TaskTransitionTable returns the explicit (state, event) -> nextState table.
func TaskTransitionTable() map[TaskTransitionKey]TaskState {
	return map[TaskTransitionKey]TaskState{
		{TaskStateQueued, TaskEventAssign}:          TaskStateStarting,
		{TaskStateQueued, TaskEventFail}:            TaskStateFailed,
		{TaskStateQueued, TaskEventCancel}:          TaskStateCancelled,
		{TaskStateStarting, TaskEventStart}:         TaskStateRunning,
		{TaskStateStarting, TaskEventStop}:          TaskStateStopped,
		{TaskStateStarting, TaskEventFail}:          TaskStateFailed,
		{TaskStateStarting, TaskEventCancel}:        TaskStateCancelled,
		{TaskStateStarting, TaskEventHostRestart}:   TaskStateUnknown,
		{TaskStateRunning, TaskEventAsk}:            TaskStateAsking,
		{TaskStateRunning, TaskEventReport}:         TaskStateChecking,
		{TaskStateRunning, TaskEventStop}:           TaskStateStopped,
		{TaskStateRunning, TaskEventFail}:           TaskStateFailed,
		{TaskStateRunning, TaskEventCancel}:         TaskStateCancelled,
		{TaskStateRunning, TaskEventHostRestart}:    TaskStateUnknown,
		{TaskStateAsking, TaskEventAnswer}:          TaskStateRunning,
		{TaskStateAsking, TaskEventStop}:            TaskStateStopped,
		{TaskStateAsking, TaskEventFail}:            TaskStateFailed,
		{TaskStateAsking, TaskEventCancel}:          TaskStateCancelled,
		{TaskStateAsking, TaskEventHostRestart}:     TaskStateUnknown,
		{TaskStateChecking, TaskEventChecksPassed}:  TaskStateReview,
		{TaskStateChecking, TaskEventChecksFailed}:  TaskStateFailed,
		{TaskStateChecking, TaskEventStop}:          TaskStateStopped,
		{TaskStateChecking, TaskEventFail}:          TaskStateFailed,
		{TaskStateChecking, TaskEventCancel}:        TaskStateCancelled,
		{TaskStateChecking, TaskEventHostRestart}:   TaskStateUnknown,
		{TaskStateReview, TaskEventApprove}:         TaskStateIntegrating,
		{TaskStateReview, TaskEventRequestChanges}:  TaskStateRunning,
		{TaskStateReview, TaskEventCompleteNoDiff}:  TaskStateDone,
		{TaskStateReview, TaskEventFail}:            TaskStateFailed,
		{TaskStateReview, TaskEventCancel}:          TaskStateCancelled,
		{TaskStateIntegrating, TaskEventIntegrate}:  TaskStateDone,
		{TaskStateIntegrating, TaskEventFail}:       TaskStateFailed,
		{TaskStateIntegrating, TaskEventCancel}:     TaskStateCancelled,
		{TaskStateUnknown, TaskEventDecisionResume}: TaskStateStarting,
		{TaskStateUnknown, TaskEventDecisionKeep}:   TaskStateReview,
		{TaskStateUnknown, TaskEventDecisionCancel}: TaskStateCancelled,
	}
}

// TransitionTask validates and computes the next task state from the explicit transition table
// and enforces domain guards (such as team_manager children termination before checking and
// non-empty failure codes for failed).
func TransitionTask(in TaskTransitionInput) (TaskState, error) {
	table := TaskTransitionTable()
	next, ok := table[TaskTransitionKey{From: in.From, Event: in.Event}]
	if !ok {
		return "", &TransitionError{
			Entity: "task",
			From:   string(in.From),
			Event:  string(in.Event),
			Reason: ErrInvalidTransition,
		}
	}
	if next == TaskStateChecking && in.Role == RoleTeamManager && !in.ChildrenTerminal {
		return "", &TransitionError{
			Entity: "task",
			From:   string(in.From),
			Event:  string(in.Event),
			Reason: ErrChildrenNotTerminal,
		}
	}
	if next == TaskStateFailed && in.FailureCode == "" {
		return "", &ValidationError{
			Field:  "failure_code",
			Detail: "failure code is required when entering failed state",
			Reason: ErrInvalidSpec,
		}
	}
	return next, nil
}
