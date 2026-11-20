package teams

import (
	"path"
	"strings"
)

// The input bounds of one proposal (§5.3). They are input-sanity bounds, not
// policy: the §11 agent budget governs how much of a team runs at once, and
// the user approves every proposal before anything starts.
const (
	MaxProposalTasks = 64
	MaxProposalRoles = 24
	// maxTitleBytes and maxBriefBytes bound the free-text fields of the
	// proposal card; the brief is also the §6.5 team brief.
	maxTitleBytes = 512
	maxBriefBytes = 16 * 1024
)

// Warning codes of the §5.3 card.
const (
	WarnNewRole         = "new_role"
	WarnModelUnverified = "model_unverified"
	WarnScopeOverlap    = "scope_overlap"
)

// ProposedRole is one role entry of a proposal (§5.3). A name that matches a
// catalog role inherits the catalog definition; any other name proposes a new
// role, which the card marks with WarnNewRole.
type ProposedRole struct {
	Name       string     `json:"name"`
	Vendor     Vendor     `json:"vendor"`
	Model      string     `json:"model,omitempty"`
	Effort     Effort     `json:"effort,omitempty"`
	Permission Permission `json:"permission"`
	Checks     []string   `json:"checks,omitempty"`
}

// ProposedTask is one task of a proposal tree (§5.3). TeamRole carries the
// OR-01 hierarchy position; Role names a catalog or proposed role. Budget is
// set by an approved edit or amend (§11); the propose tool does not offer it.
type ProposedTask struct {
	ID           string           `json:"id"`
	ParentTaskID string           `json:"parent_task_id,omitempty"`
	TeamRole     TeamRole         `json:"team_role"`
	Role         string           `json:"role"`
	Goal         string           `json:"goal"`
	Context      string           `json:"context,omitempty"`
	WriteScope   []string         `json:"write_scope"`
	Deliverable  Deliverable      `json:"deliverable"`
	DependsOn    []string         `json:"depends_on,omitempty"`
	Checks       []string         `json:"checks,omitempty"`
	Profile      *ProfileOverride `json:"profile,omitempty"`
	Budget       *TaskBudget      `json:"budget,omitempty"`
}

// ProposalInput is a team proposal as the proposing model wrote it. It is
// untrusted data: nothing in it becomes an instruction to Fugue (OR-13, §13).
// The JSON shape is also the sealed TeamProposed payload, so the decide path
// (F11.2b) can re-validate edits against exactly what was proposed.
type ProposalInput struct {
	Title  string         `json:"title"`
	Brief  string         `json:"brief"`
	Roles  []ProposedRole `json:"roles,omitempty"`
	Tasks  []ProposedTask `json:"tasks"`
	Budget *TeamBudget    `json:"budget,omitempty"`
}

// Warning is one §5.3 finding shown on the proposal card. Codes are stable
// identifiers; Detail names fields and IDs and never quotes task content.
type Warning struct {
	Code   string
	Detail string
}

// AgentCount returns how many vendor sessions the team needs beyond the
// proposing session: every team_manager and member task gets its own agent
// when F11.3 starts it, while the root lead task is the proposing session.
func AgentCount(in *ProposalInput) int {
	count := 0
	for i := range in.Tasks {
		if in.Tasks[i].TeamRole == RoleTeamManager || in.Tasks[i].TeamRole == RoleMember {
			count++
		}
	}
	return count
}

// TaskSpec returns the task's §4 specification for sealing and validation.
func (t *ProposedTask) TaskSpec() TaskSpec { return taskSpecOf(t) }

// ValidateProposal validates a proposal against the F11.1 rules (§4 task
// contract, depth 2 tree, depends_on DAG) and the workspace role catalog, and
// returns the §5.3 warnings of §5.3 in stable order. Catalog definitions win
// over proposed entries of the same name (§5.2); an unknown task role is a
// validation error, not a warning.
func ValidateProposal(in *ProposalInput, catalog []Role) ([]Warning, error) {
	if err := validateProposalBounds(in); err != nil {
		return nil, err
	}
	proposed, err := validateProposalRoles(in, catalog)
	if err != nil {
		return nil, err
	}
	if err := validateProposalTasks(in, proposed); err != nil {
		return nil, err
	}
	return proposalWarnings(in, catalog), nil
}

func validateProposalBounds(in *ProposalInput) error {
	if len(strings.TrimSpace(in.Title)) == 0 || len(in.Title) > maxTitleBytes || strings.ContainsRune(in.Title, '\x00') {
		return &ValidationError{Field: "proposal.title", Detail: "title is required, at most 512 bytes, without NUL", Reason: ErrInvalidSpec}
	}
	if len(in.Brief) == 0 || len(in.Brief) > maxBriefBytes || strings.ContainsRune(in.Brief, '\x00') {
		return &ValidationError{Field: "proposal.brief", Detail: "brief is required, at most 16 KiB, without NUL", Reason: ErrInvalidSpec}
	}
	if len(in.Tasks) == 0 || len(in.Tasks) > MaxProposalTasks {
		return &ValidationError{Field: "proposal.tasks", Detail: "a proposal carries 1..64 tasks", Reason: ErrInvalidSpec}
	}
	if len(in.Roles) > MaxProposalRoles {
		return &ValidationError{Field: "proposal.roles", Detail: "a proposal carries at most 24 roles", Reason: ErrInvalidSpec}
	}
	return ValidateTeamBudget(in.Budget)
}

// validateProposalRoles checks every proposed entry and returns the resolved
// role views of the proposal: catalog definitions where the name matches,
// validated new entries elsewhere.
func validateProposalRoles(in *ProposalInput, catalog []Role) (map[string]Role, error) {
	resolved := make(map[string]Role, len(in.Roles)+len(catalog))
	for i := range catalog {
		resolved[catalog[i].Name] = catalog[i]
	}
	for i := range in.Roles {
		p := &in.Roles[i]
		if _, known := resolved[p.Name]; known {
			continue
		}
		role := Role{Name: p.Name, Vendor: p.Vendor, Model: p.Model, Effort: p.Effort, Permission: p.Permission, Checks: p.Checks}
		if err := ValidateRole(&role); err != nil {
			return nil, err
		}
		resolved[p.Name] = role
	}
	return resolved, nil
}

func validateProposalTasks(in *ProposalInput, resolved map[string]Role) error {
	nodes := make([]TaskNode, 0, len(in.Tasks))
	specs := make([]TeamTaskSpec, 0, len(in.Tasks))
	for i := range in.Tasks {
		t := &in.Tasks[i]
		nodes = append(nodes, TaskNode{ID: t.ID, ParentTaskID: t.ParentTaskID, Role: t.TeamRole})
		if _, known := resolved[t.Role]; !known {
			return &ValidationError{Field: "task.role", Detail: "role " + t.Role + " is neither in the catalog nor proposed", Reason: ErrUnknownRole}
		}
		specs = append(specs, TeamTaskSpec{ID: t.ID, Spec: taskSpecOf(t)})
	}
	if err := ValidateTeamTree(nodes); err != nil {
		return err
	}
	return ValidateTeamTaskSpecs(specs)
}

func taskSpecOf(t *ProposedTask) TaskSpec {
	return TaskSpec{
		Role: t.Role, Goal: t.Goal, Context: t.Context, WriteScope: t.WriteScope,
		Deliverable: t.Deliverable, DependsOn: t.DependsOn, Checks: t.Checks, Profile: t.Profile,
		Budget: t.Budget,
	}
}

// proposalWarnings computes the §5.3 warnings: new roles, models the catalog
// cannot vouch for, and overlapping write scopes without depends_on.
func proposalWarnings(in *ProposalInput, catalog []Role) []Warning {
	var out []Warning
	catalogNames := make(map[string]struct{}, len(catalog))
	for i := range catalog {
		catalogNames[catalog[i].Name] = struct{}{}
	}
	for i := range in.Roles {
		if _, known := catalogNames[in.Roles[i].Name]; !known {
			out = append(out, Warning{Code: WarnNewRole, Detail: "role " + in.Roles[i].Name + " is new"})
		}
	}
	out = append(out, modelWarnings(in, catalogNames)...)
	return append(out, scopeOverlapWarnings(in)...)
}

// modelWarnings flags task profile overrides: the model a task would run is
// then not the catalog's vetted pair, and no vendor publishes a model list
// Fugue could check it against in stage A (vendor-runtime §6), so the card
// says unverified rather than guessing.
func modelWarnings(in *ProposalInput, catalogNames map[string]struct{}) []Warning {
	var out []Warning
	seen := make(map[string]struct{}, len(in.Tasks))
	for i := range in.Tasks {
		t := &in.Tasks[i]
		if t.Profile == nil || (t.Profile.Model == "" && t.Profile.Vendor == "") {
			continue
		}
		_, knownRole := catalogNames[t.Role]
		key := t.Role + "/" + t.Profile.Model
		if _, dup := seen[key]; dup || knownRole && t.Profile.Model == "" {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Warning{Code: WarnModelUnverified, Detail: "task " + t.ID + " overrides the model of role " + t.Role})
	}
	return out
}

// scopeOverlapWarnings reports pairs of tasks whose write scopes overlap
// without a depends_on between them (§5.3). Overlap is conservative: equal
// patterns, or a literal path matched by the other task's pattern. Different
// non-literal patterns may still intersect; the merge queue remains the real
// gate (§10).
func scopeOverlapWarnings(in *ProposalInput) []Warning {
	var out []Warning
	for i := range in.Tasks {
		if len(in.Tasks[i].WriteScope) == 0 {
			continue
		}
		for j := i + 1; j < len(in.Tasks); j++ {
			other := &in.Tasks[j]
			if len(other.WriteScope) == 0 || dependsEitherWay(&in.Tasks[i], other) {
				continue
			}
			if scopesOverlap(in.Tasks[i].WriteScope, other.WriteScope) {
				out = append(out, Warning{Code: WarnScopeOverlap, Detail: "tasks " + in.Tasks[i].ID + " and " + other.ID + " may write the same paths without depends_on"})
			}
		}
	}
	return out
}

func dependsEitherWay(a, b *ProposedTask) bool {
	return containsTask(a.DependsOn, b.ID) || containsTask(b.DependsOn, a.ID)
}

func containsTask(deps []string, id string) bool {
	for _, dep := range deps {
		if dep == id {
			return true
		}
	}
	return false
}

func scopesOverlap(a, b []string) bool {
	for _, pa := range a {
		for _, pb := range b {
			if pa == pb || (isLiteralPath(pa) && matches(pb, pa)) || (isLiteralPath(pb) && matches(pa, pb)) {
				return true
			}
		}
	}
	return false
}

func isLiteralPath(p string) bool {
	return !strings.ContainsAny(p, "*?[\\")
}

func matches(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}
