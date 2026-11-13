package vendorstore

import "fmt"

const (
	outcomeDone      = string(TeamStateDone)
	outcomeFailed    = string(TeamStateFailed)
	outcomeStopped   = string(TaskStateStopped)
	outcomeCancelled = string(TeamStateCancelled)
)

type teamTransKey struct {
	From    TeamState
	Event   TeamEventType
	Outcome string
}

type taskTransKey struct {
	From    TaskState
	Event   TeamEventType
	Outcome string
}

func teamTransitions() map[teamTransKey]TeamState {
	return map[teamTransKey]TeamState{
		{TeamStateProposed, EventTeamStarted, ""}:                TeamStateActive,
		{TeamStateProposed, EventTeamEnded, outcomeCancelled}:    TeamStateCancelled,
		{TeamStateActive, EventTeamFinalReview, ""}:              TeamStateFinalReview,
		{TeamStateActive, EventTeamEnded, outcomeCancelled}:      TeamStateCancelled,
		{TeamStateActive, EventTeamEnded, outcomeFailed}:         TeamStateFailed,
		{TeamStateFinalReview, EventTeamDecided, "revision"}:     TeamStateActive,
		{TeamStateFinalReview, EventTeamDecided, "complete"}:     TeamStateDone,
		{TeamStateFinalReview, EventTeamEnded, outcomeDone}:      TeamStateDone,
		{TeamStateFinalReview, EventTeamEnded, outcomeCancelled}: TeamStateCancelled,
		{TeamStateFinalReview, EventTeamEnded, outcomeFailed}:    TeamStateFailed,
	}
}

func taskTransitions() map[taskTransKey]TaskState {
	return map[taskTransKey]TaskState{
		{TaskStateQueued, EventTaskAssigned, ""}:                    TaskStateStarting,
		{TaskStateQueued, EventTaskEnded, outcomeFailed}:            TaskStateFailed,
		{TaskStateQueued, EventTaskEnded, outcomeCancelled}:         TaskStateCancelled,
		{TaskStateStarting, EventTaskStarted, ""}:                   TaskStateRunning,
		{TaskStateStarting, EventTaskEnded, outcomeStopped}:         TaskStateStopped,
		{TaskStateStarting, EventTaskEnded, outcomeFailed}:          TaskStateFailed,
		{TaskStateStarting, EventTaskEnded, outcomeCancelled}:       TaskStateCancelled,
		{TaskStateStarting, EventTaskMarkedUnknown, ""}:             TaskStateUnknown,
		{TaskStateRunning, EventTaskQuestionAsked, ""}:              TaskStateAsking,
		{TaskStateRunning, EventTaskReported, ""}:                   TaskStateChecking,
		{TaskStateRunning, EventTaskEnded, outcomeStopped}:          TaskStateStopped,
		{TaskStateRunning, EventTaskEnded, outcomeFailed}:           TaskStateFailed,
		{TaskStateRunning, EventTaskEnded, outcomeCancelled}:        TaskStateCancelled,
		{TaskStateRunning, EventTaskMarkedUnknown, ""}:              TaskStateUnknown,
		{TaskStateAsking, EventTaskQuestionAnswered, ""}:            TaskStateRunning,
		{TaskStateAsking, EventTaskEnded, outcomeStopped}:           TaskStateStopped,
		{TaskStateAsking, EventTaskEnded, outcomeFailed}:            TaskStateFailed,
		{TaskStateAsking, EventTaskEnded, outcomeCancelled}:         TaskStateCancelled,
		{TaskStateAsking, EventTaskMarkedUnknown, ""}:               TaskStateUnknown,
		{TaskStateChecking, EventTaskCheckCompleted, "passed"}:      TaskStateReview,
		{TaskStateChecking, EventTaskCheckCompleted, outcomeFailed}: TaskStateFailed,
		{TaskStateChecking, EventTaskEnded, outcomeStopped}:         TaskStateStopped,
		{TaskStateChecking, EventTaskEnded, outcomeFailed}:          TaskStateFailed,
		{TaskStateChecking, EventTaskEnded, outcomeCancelled}:       TaskStateCancelled,
		{TaskStateChecking, EventTaskMarkedUnknown, ""}:             TaskStateUnknown,
		{TaskStateReview, EventTaskReviewed, "approved"}:            TaskStateIntegrating,
		{TaskStateReview, EventTaskReviewed, "request_changes"}:     TaskStateRunning,
		{TaskStateReview, EventTaskReviewed, "complete_no_diff"}:    TaskStateDone,
		{TaskStateReview, EventTaskEnded, outcomeDone}:              TaskStateDone,
		{TaskStateReview, EventTaskEnded, outcomeFailed}:            TaskStateFailed,
		{TaskStateReview, EventTaskEnded, outcomeCancelled}:         TaskStateCancelled,
		{TaskStateIntegrating, EventTaskIntegrated, ""}:             TaskStateDone,
		{TaskStateIntegrating, EventTaskEnded, outcomeDone}:         TaskStateDone,
		{TaskStateIntegrating, EventTaskEnded, outcomeFailed}:       TaskStateFailed,
		{TaskStateIntegrating, EventTaskEnded, outcomeCancelled}:    TaskStateCancelled,
		{TaskStateUnknown, EventTaskRecoveryDecided, "resume"}:      TaskStateStarting,
		{TaskStateUnknown, EventTaskRecoveryDecided, "keep_diff"}:   TaskStateReview,
		{TaskStateUnknown, EventTaskRecoveryDecided, "cancel"}:      TaskStateCancelled,
	}
}

func nextTeamState(from TeamState, ev TeamEventType, outcome, failureCode string) (TeamState, error) {
	next, ok := teamTransitions()[teamTransKey{From: from, Event: ev, Outcome: outcome}]
	if !ok {
		return "", fmt.Errorf("%w: team cannot apply %s(%s) from %s", ErrTeamTransition, ev, outcome, from)
	}
	if next == TeamStateFailed && failureCode == "" {
		return "", fmt.Errorf("%w: team failed state requires failure_code", ErrTeamInvalidInput)
	}
	return next, nil
}

func nextTaskState(from TaskState, ev TeamEventType, outcome, failureCode string) (TaskState, error) {
	next, ok := taskTransitions()[taskTransKey{From: from, Event: ev, Outcome: outcome}]
	if !ok {
		return "", fmt.Errorf("%w: task cannot apply %s(%s) from %s", ErrTeamTransition, ev, outcome, from)
	}
	if next == TaskStateFailed && failureCode == "" {
		return "", fmt.Errorf("%w: task failed state requires failure_code", ErrTeamInvalidInput)
	}
	return next, nil
}

func isTeamEventType(ev TeamEventType) bool {
	return ev == EventTeamProposed ||
		ev == EventTeamStarted ||
		ev == EventTeamFinalReview ||
		ev == EventTeamDecided ||
		ev == EventTeamEnded
}

func isTeamTerminal(s TeamState) bool {
	return s == TeamStateDone || s == TeamStateCancelled || s == TeamStateFailed
}

func isTaskTerminal(s TaskState) bool {
	return s == TaskStateDone || s == TaskStateFailed || s == TaskStateStopped || s == TaskStateCancelled
}

func validateNewTeamTasks(tasks []NewTaskInput) (map[string]int, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%w: team requires at least one task", ErrTeamInvalidInput)
	}
	byID := make(map[string]NewTaskInput, len(tasks))
	hasChildren := make(map[string]bool, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		if err := validateTaskInputFields(t, byID); err != nil {
			return nil, err
		}
		byID[t.TaskID] = *t
		if t.ParentTaskID != "" {
			hasChildren[t.ParentTaskID] = true
		}
	}
	depths, err := validateTaskHierarchy(tasks, byID, hasChildren)
	if err != nil {
		return nil, err
	}
	if err := validateTaskDependencies(tasks, byID); err != nil {
		return nil, err
	}
	return depths, nil
}

func validateTaskInputFields(t *NewTaskInput, byID map[string]NewTaskInput) error {
	if t.TaskID == "" || t.CreatedEventID == "" || t.CatalogRole == "" || len(t.ProtectedSpec) == 0 {
		return fmt.Errorf("%w: missing required task field", ErrTeamInvalidInput)
	}
	if _, dup := byID[t.TaskID]; dup {
		return fmt.Errorf("%w: duplicate task ID %s", ErrTeamInvalidInput, t.TaskID)
	}
	switch t.HierarchyRole {
	case RoleSolo, RoleLead, RoleTeamManager, RoleMember:
	default:
		return fmt.Errorf("%w: invalid hierarchy role %s", ErrTeamInvalidInput, t.HierarchyRole)
	}
	switch t.Deliverable {
	case DeliverableDiff, DeliverableReport, DeliverableBoth:
	default:
		return fmt.Errorf("%w: invalid deliverable %s", ErrTeamInvalidInput, t.Deliverable)
	}
	return nil
}

func validateTaskHierarchy(tasks []NewTaskInput, byID map[string]NewTaskInput, hasChildren map[string]bool) (map[string]int, error) {
	depths := make(map[string]int, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		if t.HierarchyRole == RoleSolo && len(tasks) > 1 {
			return nil, fmt.Errorf("%w: solo cannot share a multi-task team", ErrTeamInvalidInput)
		}
		if (t.HierarchyRole == RoleMember || t.HierarchyRole == RoleSolo) && hasChildren[t.TaskID] {
			return nil, fmt.Errorf("%w: %s cannot have children", ErrTeamInvalidInput, t.HierarchyRole)
		}
		d, err := computeTaskDepth(t, byID)
		if err != nil {
			return nil, err
		}
		if d > 2 {
			return nil, fmt.Errorf("%w: hierarchy depth exceeds 2", ErrTeamInvalidInput)
		}
		if t.ParentTaskID != "" && !validParentRolePair(byID[t.ParentTaskID].HierarchyRole, t.HierarchyRole, d) {
			return nil, fmt.Errorf("%w: invalid parent/child role pairing", ErrTeamInvalidInput)
		}
		depths[t.TaskID] = d
	}
	return depths, nil
}

func validParentRolePair(parent, child TeamRole, depth int) bool {
	if parent == RoleLead {
		return depth == 1 && (child == RoleTeamManager || child == RoleMember)
	}
	if parent == RoleTeamManager {
		return depth == 2 && child == RoleMember
	}
	return false
}

func computeTaskDepth(t *NewTaskInput, byID map[string]NewTaskInput) (int, error) {
	seen := make(map[string]struct{}, 4)
	cur := t
	depth := 0
	for cur.ParentTaskID != "" {
		if _, ok := seen[cur.TaskID]; ok {
			return 0, fmt.Errorf("%w: parent cycle", ErrTeamInvalidInput)
		}
		seen[cur.TaskID] = struct{}{}
		parent, ok := byID[cur.ParentTaskID]
		if !ok {
			return 0, fmt.Errorf("%w: missing parent %s", ErrTeamInvalidInput, cur.ParentTaskID)
		}
		depth++
		if depth > 3 {
			return depth, nil
		}
		cur = &parent
	}
	if cur.HierarchyRole != RoleLead && cur.HierarchyRole != RoleSolo {
		return 0, fmt.Errorf("%w: root task must be lead or solo", ErrTeamInvalidInput)
	}
	return depth, nil
}

func validateTaskDependencies(tasks []NewTaskInput, byID map[string]NewTaskInput) error {
	for i := range tasks {
		if err := validateTaskDependencyTargets(&tasks[i], byID); err != nil {
			return err
		}
	}
	return detectTaskDependencyCycles(tasks, byID)
}

func validateTaskDependencyTargets(t *NewTaskInput, byID map[string]NewTaskInput) error {
	for _, dep := range t.DependsOn {
		if dep == "" || dep == t.TaskID {
			return fmt.Errorf("%w: self or empty depends_on", ErrTeamInvalidInput)
		}
		if _, ok := byID[dep]; !ok {
			return fmt.Errorf("%w: depends_on target %s not in team", ErrTeamInvalidInput, dep)
		}
	}
	return nil
}

func detectTaskDependencyCycles(tasks []NewTaskInput, byID map[string]NewTaskInput) error {
	state := make(map[string]int, len(tasks))
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("%w: depends_on cycle at %s", ErrTeamInvalidInput, id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, dep := range byID[id].DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for i := range tasks {
		if state[tasks[i].TaskID] == 0 {
			if err := visit(tasks[i].TaskID); err != nil {
				return err
			}
		}
	}
	return nil
}
