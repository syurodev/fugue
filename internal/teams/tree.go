package teams

// TeamRole classifies a task's structural role within OR-01 team orchestration.
type TeamRole string

// Allowed OR-01 team hierarchy roles.
const (
	RoleSolo        TeamRole = "solo"
	RoleLead        TeamRole = "lead"
	RoleTeamManager TeamRole = "team_manager"
	RoleMember      TeamRole = "member"
)

const (
	fieldTaskID     = "task.id"
	fieldTaskRole   = "task.role"
	fieldTaskParent = "task.parent_task_id"
)

// MaxTreeDepth is the maximum allowed depth in OR-01 (lead=0, team_manager/member=1, member=2).
const MaxTreeDepth = 2

// IsValidTeamRole reports whether role is one of the four OR-01 roles.
func IsValidTeamRole(role TeamRole) bool {
	switch role {
	case RoleSolo, RoleLead, RoleTeamManager, RoleMember:
		return true
	default:
		return false
	}
}

// TaskNode describes a task's position in the team hierarchy tree.
type TaskNode struct {
	ID           string
	ParentTaskID string
	Role         TeamRole
}

// ValidateTeamTree validates OR-01 hierarchy rules across all nodes in a team:
// depth <= 2 (lead -> team_manager -> member), member and solo cannot have children,
// solo cannot share a tree, and parent links are acyclic and point within the team.
func ValidateTeamTree(nodes []TaskNode) error {
	if len(nodes) == 0 {
		return &ValidationError{Field: "tasks", Detail: "team must have at least one task", Reason: ErrInvalidSpec}
	}
	byID, err := indexTaskNodes(nodes)
	if err != nil {
		return err
	}
	hasChildren := collectHasChildren(nodes)
	depths := make([]int, len(nodes))
	for i := range nodes {
		if err := validateRootAndChildShape(nodes[i], hasChildren[nodes[i].ID], len(nodes)); err != nil {
			return err
		}
		depth, err := computeNodeDepth(nodes[i], byID)
		if err != nil {
			return err
		}
		if depth > MaxTreeDepth {
			return &ValidationError{Field: fieldTaskParent, Detail: "depth exceeds max 2", Reason: ErrDepthExceeded}
		}
		depths[i] = depth
	}
	for i := range nodes {
		if nodes[i].ParentTaskID == "" {
			continue
		}
		if err := validateParentChildPair(byID[nodes[i].ParentTaskID].Role, nodes[i].Role, depths[i]); err != nil {
			return err
		}
	}
	return nil
}

func indexTaskNodes(nodes []TaskNode) (map[string]TaskNode, error) {
	byID := make(map[string]TaskNode, len(nodes))
	for i := range nodes {
		n := nodes[i]
		if n.ID == "" {
			return nil, &ValidationError{Field: fieldTaskID, Detail: "task ID is required", Reason: ErrInvalidSpec}
		}
		if _, exists := byID[n.ID]; exists {
			return nil, &ValidationError{Field: fieldTaskID, Detail: "duplicate task ID " + n.ID, Reason: ErrInvalidSpec}
		}
		if !IsValidTeamRole(n.Role) {
			return nil, &ValidationError{Field: fieldTaskRole, Detail: string(n.Role), Reason: ErrInvalidRole}
		}
		byID[n.ID] = n
	}
	return byID, nil
}

func collectHasChildren(nodes []TaskNode) map[string]bool {
	out := make(map[string]bool, len(nodes))
	for i := range nodes {
		if nodes[i].ParentTaskID != "" {
			out[nodes[i].ParentTaskID] = true
		}
	}
	return out
}

func validateRootAndChildShape(n TaskNode, nodeHasChildren bool, total int) error {
	if n.Role == RoleSolo && total > 1 {
		return &ValidationError{Field: fieldTaskRole, Detail: "solo cannot belong to a multi-task team", Reason: ErrInvalidRole}
	}
	if (n.Role == RoleMember || n.Role == RoleSolo) && nodeHasChildren {
		return &ValidationError{Field: fieldTaskParent, Detail: string(n.Role) + " cannot have children", Reason: ErrMemberCannotHaveChildren}
	}
	if n.ParentTaskID == "" && n.Role != RoleLead && n.Role != RoleSolo {
		return &ValidationError{Field: fieldTaskRole, Detail: "root task must be lead or solo", Reason: ErrInvalidRole}
	}
	return nil
}

func validateParentChildPair(parentRole, childRole TeamRole, childDepth int) error {
	switch parentRole {
	case RoleSolo, RoleMember:
		return &ValidationError{Field: fieldTaskParent, Detail: string(parentRole) + " cannot have children", Reason: ErrMemberCannotHaveChildren}
	case RoleLead:
		if childDepth != 1 || (childRole != RoleTeamManager && childRole != RoleMember) {
			return &ValidationError{Field: fieldTaskRole, Detail: "lead children must be team_manager or member at depth 1", Reason: ErrInvalidRole}
		}
	case RoleTeamManager:
		if childDepth != 2 || childRole != RoleMember {
			return &ValidationError{Field: fieldTaskRole, Detail: "team_manager children must be member at depth 2", Reason: ErrInvalidRole}
		}
	default:
		return &ValidationError{Field: fieldTaskParent, Detail: string(parentRole), Reason: ErrInvalidRole}
	}
	return nil
}

func computeNodeDepth(n TaskNode, byID map[string]TaskNode) (int, error) {
	seen := make(map[string]struct{}, 4)
	cur := n
	depth := 0
	for cur.ParentTaskID != "" {
		if _, ok := seen[cur.ID]; ok {
			return 0, &ValidationError{Field: fieldTaskParent, Detail: "parent cycle", Reason: ErrCyclicDependency}
		}
		seen[cur.ID] = struct{}{}
		parent, ok := byID[cur.ParentTaskID]
		if !ok {
			return 0, &ValidationError{Field: fieldTaskParent, Detail: "parent task not in team", Reason: ErrMissingDependency}
		}
		depth++
		if depth > MaxTreeDepth+1 {
			return depth, nil
		}
		cur = parent
	}
	return depth, nil
}
