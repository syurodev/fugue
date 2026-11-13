package teams

import (
	"encoding/json"
	"strings"
)

// Deliverable defines the required output artifact of a task (§4).
type Deliverable string

// Deliverable enum values required by TaskSpec (§4).
const (
	DeliverableDiff   Deliverable = "diff"
	DeliverableReport Deliverable = "report"
	DeliverableBoth   Deliverable = "both"
)

const (
	fieldSpecTaskID    = "task.id"
	fieldSpecDependsOn = "spec.depends_on"
)

// IsValidDeliverable reports whether d is a valid deliverable enum value.
func IsValidDeliverable(d Deliverable) bool {
	switch d {
	case DeliverableDiff, DeliverableReport, DeliverableBoth:
		return true
	default:
		return false
	}
}

// ProfileOverride optionally overrides vendor/model/effort for a single task.
type ProfileOverride struct {
	Vendor Vendor
	Model  string
	Effort Effort
}

// TaskBudget constrains execution limits for a task (§4).
type TaskBudget struct {
	MaxTurns           int   `json:"max_turns,omitempty"`
	MaxTokens          int   `json:"max_tokens,omitempty"`
	MaxDurationSeconds int64 `json:"max_duration_seconds,omitempty"`
}

// TaskSpec defines the full orchestration task contract (§4).
// Content fields (Goal, Context, WriteScope globs, Checks, OutputSchema) are
// protected payloads under INV-09 and must be stored sealed rather than in metadata columns.
type TaskSpec struct {
	Role         string
	Goal         string
	Context      string
	WriteScope   []string
	Deliverable  Deliverable
	DependsOn    []string
	Checks       []string
	OutputSchema json.RawMessage
	Profile      *ProfileOverride
	Budget       *TaskBudget
}

// ProtectedTaskContent extracts only the content fields of TaskSpec that must stay
// outside SQL metadata columns and error messages.
type ProtectedTaskContent struct {
	Goal         string          `json:"goal"`
	Context      string          `json:"context,omitempty"`
	WriteScope   []string        `json:"write_scope"`
	Checks       []string        `json:"checks,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// ProtectedContent returns the protected content view of spec for envelope sealing.
func (s *TaskSpec) ProtectedContent() ProtectedTaskContent {
	out := ProtectedTaskContent{
		Goal:       s.Goal,
		Context:    s.Context,
		WriteScope: append([]string{}, s.WriteScope...),
	}
	if len(s.Checks) > 0 {
		out.Checks = append([]string{}, s.Checks...)
	}
	if len(s.OutputSchema) > 0 {
		out.OutputSchema = append(json.RawMessage(nil), s.OutputSchema...)
	}
	return out
}

// TeamTaskSpec pairs a task identity inside a team with its TaskSpec for graph validation.
type TeamTaskSpec struct {
	ID   string
	Spec TaskSpec
}

// ValidateTaskSpec validates a single task specification against §4 rules.
func ValidateTaskSpec(spec *TaskSpec) error {
	if spec == nil {
		return &ValidationError{Field: "spec", Detail: "spec is required", Reason: ErrInvalidSpec}
	}
	if err := validateRoleIdentifier("spec.role", spec.Role); err != nil {
		return err
	}
	if strings.TrimSpace(spec.Goal) == "" {
		return &ValidationError{Field: "spec.goal", Detail: "goal is required", Reason: ErrInvalidSpec}
	}
	if spec.WriteScope == nil {
		return &ValidationError{Field: "spec.write_scope", Detail: "write_scope must be non-nil (empty slice for read-only)", Reason: ErrInvalidSpec}
	}
	if err := validateStringEntries("spec.write_scope", spec.WriteScope); err != nil {
		return err
	}
	if !IsValidDeliverable(spec.Deliverable) {
		return &ValidationError{Field: "spec.deliverable", Detail: "must be diff, report, or both", Reason: ErrInvalidSpec}
	}
	if err := validateStringEntries("spec.checks", spec.Checks); err != nil {
		return err
	}
	if len(spec.OutputSchema) > 0 && !json.Valid(spec.OutputSchema) {
		return &ValidationError{Field: "spec.output_schema", Detail: "must be valid JSON", Reason: ErrInvalidSpec}
	}
	return validateProfileAndBudget(spec.Profile, spec.Budget)
}

func validateProfileAndBudget(p *ProfileOverride, b *TaskBudget) error {
	if p != nil {
		if err := validateProfileOverride(p); err != nil {
			return err
		}
	}
	if b != nil {
		if b.MaxTurns < 0 || b.MaxTokens < 0 || b.MaxDurationSeconds < 0 {
			return &ValidationError{Field: "spec.budget", Detail: "budget values must be non-negative", Reason: ErrInvalidSpec}
		}
	}
	return nil
}

func validateProfileOverride(p *ProfileOverride) error {
	if p.Vendor != "" && !IsValidVendor(p.Vendor) {
		return &ValidationError{Field: "spec.profile.vendor", Detail: string(p.Vendor), Reason: ErrInvalidCatalog}
	}
	if p.Model != "" {
		if err := ValidateModelName("spec.profile.model", p.Model); err != nil {
			return err
		}
	}
	if p.Effort != "" && !IsValidEffort(p.Effort) {
		return &ValidationError{Field: "spec.profile.effort", Detail: string(p.Effort), Reason: ErrInvalidCatalog}
	}
	return nil
}

func validateStringEntries(field string, items []string) error {
	for _, s := range items {
		if strings.TrimSpace(s) == "" || strings.ContainsRune(s, '\x00') {
			return &ValidationError{Field: field, Detail: "entries must be non-empty without NUL", Reason: ErrInvalidSpec}
		}
	}
	return nil
}

// ValidateTeamTaskSpecs validates every task's spec and verifies that all depends_on
// targets exist within the same team and form an acyclic graph (DAG).
func ValidateTeamTaskSpecs(tasks []TeamTaskSpec) error {
	index := make(map[string]*TaskSpec, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		if t.ID == "" {
			return &ValidationError{Field: fieldSpecTaskID, Detail: "task ID is required", Reason: ErrInvalidSpec}
		}
		if _, dup := index[t.ID]; dup {
			return &ValidationError{Field: fieldSpecTaskID, Detail: "duplicate task ID " + t.ID, Reason: ErrInvalidSpec}
		}
		if err := ValidateTaskSpec(&t.Spec); err != nil {
			return err
		}
		index[t.ID] = &t.Spec
	}
	if err := validateDependsOnTargets(tasks, index); err != nil {
		return err
	}
	return detectDependencyCycles(tasks, index)
}

func validateDependsOnTargets(tasks []TeamTaskSpec, index map[string]*TaskSpec) error {
	for i := range tasks {
		t := &tasks[i]
		seen := make(map[string]struct{}, len(t.Spec.DependsOn))
		for _, dep := range t.Spec.DependsOn {
			if dep == "" || dep == t.ID {
				return &ValidationError{Field: fieldSpecDependsOn, Detail: "self or empty dependency in " + t.ID, Reason: ErrCyclicDependency}
			}
			if _, ok := index[dep]; !ok {
				return &ValidationError{Field: fieldSpecDependsOn, Detail: "dependency " + dep + " not in team", Reason: ErrMissingDependency}
			}
			if _, dup := seen[dep]; dup {
				return &ValidationError{Field: fieldSpecDependsOn, Detail: "duplicate dependency " + dep, Reason: ErrInvalidSpec}
			}
			seen[dep] = struct{}{}
		}
	}
	return nil
}

func detectDependencyCycles(tasks []TeamTaskSpec, index map[string]*TaskSpec) error {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int, len(tasks))
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return &ValidationError{Field: fieldSpecDependsOn, Detail: "cycle involving " + id, Reason: ErrCyclicDependency}
		case visited:
			return nil
		}
		state[id] = visiting
		for _, dep := range index[id].DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = visited
		return nil
	}
	for i := range tasks {
		if state[tasks[i].ID] == unvisited {
			if err := visit(tasks[i].ID); err != nil {
				return err
			}
		}
	}
	return nil
}
