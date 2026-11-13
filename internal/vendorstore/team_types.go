package vendorstore

import "github.com/syurodev/fugue/internal/platform/errcode"

var (
	// ErrTeamNotFound indicates the requested team does not exist.
	ErrTeamNotFound = errcode.New(errcode.NotFound, "team not found")
	// ErrTeamExists indicates a team with that ID already exists.
	ErrTeamExists = errcode.New(errcode.AlreadyExists, "team already exists")
	// ErrTaskNotFound indicates the requested task does not exist in the team.
	ErrTaskNotFound = errcode.New(errcode.NotFound, "team task not found")
	// ErrTeamTransition indicates an invalid team or task state transition.
	ErrTeamTransition = errcode.New(errcode.FailedPrecondition, "invalid team or task state transition")
	// ErrTeamChildrenNotTerminal indicates a team_manager reported before all children reached terminal states.
	ErrTeamChildrenNotTerminal = errcode.New(errcode.FailedPrecondition, "team manager children are not terminal")
	// ErrTeamInvalidInput indicates malformed team creation or event input.
	ErrTeamInvalidInput = errcode.New(errcode.InvalidArgument, "invalid team or task input")
)

// TeamState represents the durable lifecycle state of a team (§3.2).
type TeamState string

// Valid team lifecycle states persisted in team_records (§3.2).
const (
	TeamStateProposed    TeamState = "proposed"
	TeamStateActive      TeamState = "active"
	TeamStateFinalReview TeamState = "final_review"
	TeamStateDone        TeamState = "done"
	TeamStateCancelled   TeamState = "cancelled"
	TeamStateFailed      TeamState = "failed"
)

// TaskState represents the durable lifecycle state of a team task (§3.1).
type TaskState string

// Valid task lifecycle states persisted in team_tasks (§3.1).
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

// TeamRole identifies a task's OR-01 structural role in the team tree.
type TeamRole string

// Valid OR-01 structural team hierarchy roles.
const (
	RoleSolo        TeamRole = "solo"
	RoleLead        TeamRole = "lead"
	RoleTeamManager TeamRole = "team_manager"
	RoleMember      TeamRole = "member"
)

// TeamDeliverable defines the deliverable artifact enum (§4).
type TeamDeliverable string

// Allowed task deliverable types (§4).
const (
	DeliverableDiff   TeamDeliverable = "diff"
	DeliverableReport TeamDeliverable = "report"
	DeliverableBoth   TeamDeliverable = "both"
)

// TeamEventType enumerates the event names persisted in team_task_events (§3.1, §3.2).
type TeamEventType string

// Supported team and task event types persisted in team_task_events.
const (
	EventTeamProposed    TeamEventType = "TeamProposed"
	EventTeamStarted     TeamEventType = "TeamStarted"
	EventTeamFinalReview TeamEventType = "TeamFinalReview"
	EventTeamDecided     TeamEventType = "TeamDecided"
	EventTeamEnded       TeamEventType = "TeamEnded"

	EventTaskCreated          TeamEventType = "TaskCreated"
	EventTaskAssigned         TeamEventType = "TaskAssigned"
	EventTaskStarted          TeamEventType = "TaskStarted"
	EventTaskQuestionAsked    TeamEventType = "TaskQuestionAsked"
	EventTaskQuestionAnswered TeamEventType = "TaskQuestionAnswered"
	EventTaskReported         TeamEventType = "TaskReported"
	EventTaskCheckCompleted   TeamEventType = "TaskCheckCompleted"
	EventTaskReviewed         TeamEventType = "TaskReviewed"
	EventTaskIntegrated       TeamEventType = "TaskIntegrated"
	EventTaskMarkedUnknown    TeamEventType = "TaskMarkedUnknown"
	EventTaskRecoveryDecided  TeamEventType = "TaskRecoveryDecided"
	EventTaskEnded            TeamEventType = "TaskEnded"
)

// TeamRecord is the metadata-only projection of a team_records row (INV-09).
type TeamRecord struct {
	TeamID      string
	WorkspaceID string
	SessionID   string
	State       TeamState
	LastSeq     int64
	TaskCount   int
	FailureCode string
	CreatedAt   int64
	UpdatedAt   int64
	EndedAt     int64
}

// TaskRecord is the metadata-only projection of a team_tasks row (INV-09).
// Protected content (goal, context, write_scope, checks, output_schema) is stored sealed.
type TaskRecord struct {
	TaskID        string
	TeamID        string
	ParentTaskID  string
	HierarchyRole TeamRole
	CatalogRole   string
	Deliverable   TeamDeliverable
	DependsOn     []string
	Depth         int
	State         TaskState
	FailureCode   string
	LastEventSeq  int64
	CreatedAt     int64
	UpdatedAt     int64
	EndedAt       int64
}

// NewTeamInput specifies a new team and its initial tasks created atomically.
type NewTeamInput struct {
	TeamID          string
	WorkspaceID     string
	SessionID       string
	ProposedEventID string
	Tasks           []NewTaskInput
}

// NewTaskInput defines one initial task within NewTeamInput.
// ProtectedSpec must contain only the serialized protected TaskSpec payload
// and is sealed before storage; metadata fields are validated against INV-09.
type NewTaskInput struct {
	TaskID         string
	ParentTaskID   string
	HierarchyRole  TeamRole
	CatalogRole    string
	Deliverable    TeamDeliverable
	DependsOn      []string
	CreatedEventID string
	ProtectedSpec  []byte
}

// AppendTeamEventInput describes a single event to append to a team's sequence.
type AppendTeamEventInput struct {
	EventID          string
	TaskID           string
	EventType        TeamEventType
	Outcome          string
	FailureCode      string
	ProtectedPayload []byte
}

// RecordedTeamEvent is a metadata view of one team_task_events row (INV-09).
type RecordedTeamEvent struct {
	TeamID      string
	Sequence    int64
	EventID     string
	TaskID      string
	EventType   TeamEventType
	Outcome     string
	FromState   string
	ToState     string
	FailureCode string
	HasPayload  bool
	CreatedAt   int64
}

// RecoveredTask reports one task moved to unknown by RecoverTeams (OR-15).
type RecoveredTask struct {
	TeamID        string
	TaskID        string
	PreviousState TaskState
	Sequence      int64
}
