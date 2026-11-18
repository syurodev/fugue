package teams

import (
	"bytes"
	"encoding/json"
)

// EditSetVersion is the document version of the approve-with-edits contract.
// The card answer carries the encoded document in the decision answer's free
// text; F11.7's UI builds against this type (F11.2b row notes).
const EditSetVersion = 1

// MaxEditSetBytes bounds the encoded edit document. It matches the
// interaction answer-text limit, so a card answer can always carry the
// document it was answered with.
const MaxEditSetBytes = 16 * 1024

// RoleEdit changes one role of the decided plan (§5.3 approve with edits).
// A name the plan already resolved edits that role: only vendor, model and
// effort may change, and empty means unchanged; the permission of a resolved
// role is not editable. An unresolved name proposes a new role and requires
// vendor and permission, like a proposal entry.
type RoleEdit struct {
	Name       string     `json:"name"`
	Vendor     Vendor     `json:"vendor,omitempty"`
	Model      string     `json:"model,omitempty"`
	Effort     Effort     `json:"effort,omitempty"`
	Permission Permission `json:"permission,omitempty"`
}

// TaskBudgetEdit replaces one task's §11 budget wholesale; every field of the
// embedded budget applies.
type TaskBudgetEdit struct {
	TaskID string     `json:"task_id"`
	Budget TaskBudget `json:"budget"`
}

// AccountEdit assigns an account to a role. Stage A assigns no accounts to
// roles, so decoding accepts the field and validation refuses it with a
// typed error (accepted deviation, F11.2b row notes).
type AccountEdit struct {
	Role    string `json:"role"`
	Account string `json:"account"`
}

// EditSet is the typed approve-with-edits document (§5.3). Everything in it
// is untrusted data: it is decoded strictly, re-validated against the role
// catalog and the F11.1 rules, and never logged, never audited (OR-13).
type EditSet struct {
	Version     int              `json:"version"`
	Roles       []RoleEdit       `json:"roles,omitempty"`
	RemoveTasks []string         `json:"remove_tasks,omitempty"`
	AddTasks    []ProposedTask   `json:"add_tasks,omitempty"`
	TaskBudgets []TaskBudgetEdit `json:"task_budgets,omitempty"`
	Accounts    []AccountEdit    `json:"accounts,omitempty"`
}

// DecodeEditSet decodes the edit document strictly: unknown fields, wrong
// versions and oversized documents are typed refusals, and the error never
// quotes the document.
func DecodeEditSet(raw []byte) (*EditSet, error) {
	if len(raw) == 0 {
		return &EditSet{Version: EditSetVersion}, nil
	}
	if len(raw) > MaxEditSetBytes {
		return nil, &ValidationError{Field: "edits", Detail: "the edit document is too large", Reason: ErrInvalidSpec}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var edit EditSet
	if err := decoder.Decode(&edit); err != nil {
		return nil, &ValidationError{Field: "edits", Detail: "the edit document does not match the contract", Reason: ErrInvalidSpec}
	}
	if edit.Version != EditSetVersion {
		return nil, &ValidationError{Field: "edits.version", Detail: "unsupported edit document version", Reason: ErrInvalidSpec}
	}
	if err := validateEditBounds(&edit); err != nil {
		return nil, err
	}
	return &edit, nil
}

func validateEditBounds(edit *EditSet) error {
	if len(edit.Roles) > MaxProposalRoles {
		return &ValidationError{Field: "edits.roles", Detail: "an edit carries at most 24 role entries", Reason: ErrInvalidSpec}
	}
	if len(edit.RemoveTasks)+len(edit.AddTasks) > MaxProposalTasks {
		return &ValidationError{Field: "edits.tasks", Detail: "an edit carries at most 64 task changes", Reason: ErrInvalidSpec}
	}
	return nil
}

// ApplyEdits applies a decoded edit document to the proposed plan and returns
// the edited plan for the caller to re-validate with ValidateProposal (F11.1
// rules against the role catalog). Role edits of resolved roles become per-task
// profile overrides (§4), the only channel the task contract has for the
// model a task runs; new roles become proposal entries the added tasks may
// reference. An empty document returns the plan unchanged.
func ApplyEdits(edit *EditSet, input *ProposalInput, catalog []Role) (*ProposalInput, error) {
	if len(edit.Accounts) > 0 {
		return nil, &ValidationError{Field: "edits.accounts", Detail: "accounts are not assigned to roles yet", Reason: ErrAccountsUnassigned}
	}
	edited := cloneProposalInput(input)
	resolved := resolvedRoles(input, catalog)
	profiles, newRoles, err := ValidateRoleEdits(edit.Roles, resolved)
	if err != nil {
		return nil, err
	}
	edited.Roles = append(edited.Roles, newRoles...)
	if err := applyBudgetEdits(edit.TaskBudgets, edited); err != nil {
		return nil, err
	}
	applyTaskRemovals(edit.RemoveTasks, edited)
	edited.Tasks = append(edited.Tasks, cloneTasks(edit.AddTasks)...)
	applyProfileOverrides(profiles, edited)
	return edited, nil
}

// resolvedRoles returns the roles the plan resolves to (catalog definitions
// win over proposed entries of the same name, §5.2).
func resolvedRoles(input *ProposalInput, catalog []Role) map[string]Role {
	resolved := make(map[string]Role, len(input.Roles)+len(catalog))
	for i := range catalog {
		resolved[catalog[i].Name] = catalog[i]
	}
	for i := range input.Roles {
		p := &input.Roles[i]
		if _, known := resolved[p.Name]; !known {
			resolved[p.Name] = Role{Name: p.Name, Vendor: p.Vendor, Model: p.Model, Effort: p.Effort, Permission: p.Permission, Checks: p.Checks}
		}
	}
	return resolved
}

// ValidateRoleEdits validates role edits against the roles the plan (or the
// decided team) already resolved and returns the per-role profile override
// each task of the role receives, plus the proposal entries of new roles the
// added tasks may reference. A resolved role may change vendor, model and
// effort only (§5.3, F11.2b); the amend path uses the same checks against the
// decided plan.
func ValidateRoleEdits(edits []RoleEdit, resolved map[string]Role) (map[string]ProfileOverride, []ProposedRole, error) {
	profiles := make(map[string]ProfileOverride, len(edits))
	var newRoles []ProposedRole
	for i := range edits {
		e := &edits[i]
		if known, existing := resolved[e.Name]; existing {
			override, err := editResolvedRole(e, &known)
			if err != nil {
				return nil, nil, err
			}
			profiles[e.Name] = override
			continue
		}
		newRole, err := editNewRole(e)
		if err != nil {
			return nil, nil, err
		}
		newRoles = append(newRoles, newRole)
	}
	return profiles, newRoles, nil
}

// editResolvedRole validates one edit of a role the plan already resolved:
// the permission is not editable, and blank fields keep the resolved profile.
func editResolvedRole(e *RoleEdit, known *Role) (ProfileOverride, error) {
	if e.Permission != "" && e.Permission != known.Permission {
		return ProfileOverride{}, &ValidationError{Field: "edits.roles.permission", Detail: "role " + e.Name + " keeps its resolved permission", Reason: ErrPermissionImmutable}
	}
	vendor, model, effort := known.Vendor, known.Model, known.Effort
	if e.Vendor != "" {
		if !IsValidVendor(e.Vendor) {
			return ProfileOverride{}, &ValidationError{Field: "edits.roles.vendor", Detail: string(e.Vendor), Reason: ErrInvalidCatalog}
		}
		vendor = e.Vendor
	}
	if e.Model != "" {
		if err := ValidateModelName("edits.roles.model", e.Model); err != nil {
			return ProfileOverride{}, err
		}
		model = e.Model
	}
	if e.Effort != "" {
		if !IsValidEffort(e.Effort) {
			return ProfileOverride{}, &ValidationError{Field: "edits.roles.effort", Detail: string(e.Effort), Reason: ErrInvalidCatalog}
		}
		effort = e.Effort
	}
	return ProfileOverride{Vendor: vendor, Model: model, Effort: effort}, nil
}

// editNewRole validates one edit that names a role the plan does not have:
// it proposes the role, so it needs vendor and permission like a proposal
// entry.
func editNewRole(e *RoleEdit) (ProposedRole, error) {
	if err := validateRoleIdentifier("edits.roles.name", e.Name); err != nil {
		return ProposedRole{}, err
	}
	role := Role{Name: e.Name, Vendor: e.Vendor, Model: e.Model, Effort: e.Effort, Permission: e.Permission}
	if err := ValidateRole(&role); err != nil {
		return ProposedRole{}, err
	}
	return ProposedRole{Name: e.Name, Vendor: e.Vendor, Model: e.Model, Effort: e.Effort, Permission: e.Permission}, nil
}

// applyBudgetEdits replaces the budget of tasks that exist in the edited plan.
func applyBudgetEdits(edits []TaskBudgetEdit, edited *ProposalInput) error {
	for i := range edits {
		e := &edits[i]
		if e.TaskID == "" {
			return &ValidationError{Field: "edits.task_budgets.task_id", Detail: detailTaskIDRequired, Reason: ErrInvalidSpec}
		}
		target := findTask(edited, e.TaskID)
		if target == nil {
			return &ValidationError{Field: "edits.task_budgets.task_id", Detail: "task " + e.TaskID + " is not in the plan", Reason: ErrMissingDependency}
		}
		b := e.Budget
		target.Budget = &b
	}
	return nil
}

// applyTaskRemovals drops the named tasks; a removal that orphans a parent or
// depends_on reference is refused later by the plan re-validation.
func applyTaskRemovals(remove []string, edited *ProposalInput) {
	if len(remove) == 0 {
		return
	}
	drop := make(map[string]struct{}, len(remove))
	for _, id := range remove {
		drop[id] = struct{}{}
	}
	tasks := make([]ProposedTask, 0, len(edited.Tasks))
	for i := range edited.Tasks {
		if _, gone := drop[edited.Tasks[i].ID]; !gone {
			tasks = append(tasks, edited.Tasks[i])
		}
	}
	edited.Tasks = tasks
}

// applyProfileOverrides writes the edited role profiles onto every surviving
// task of the role (§4: the profile override is the task's model).
func applyProfileOverrides(profiles map[string]ProfileOverride, edited *ProposalInput) {
	if len(profiles) == 0 {
		return
	}
	for i := range edited.Tasks {
		p, ok := profiles[edited.Tasks[i].Role]
		if !ok {
			continue
		}
		override := p
		edited.Tasks[i].Profile = &override
	}
}

func cloneProposalInput(in *ProposalInput) *ProposalInput {
	out := &ProposalInput{Title: in.Title, Brief: in.Brief, Roles: cloneRoles(in.Roles), Tasks: cloneTasks(in.Tasks)}
	return out
}

func cloneRoles(in []ProposedRole) []ProposedRole {
	out := make([]ProposedRole, len(in))
	copy(out, in)
	for i := range out {
		out[i].Checks = append([]string{}, in[i].Checks...)
	}
	return out
}

// cloneTasks copies the tasks, keeping an empty write_scope non-nil: the §4
// contract reads an empty slice as read-only, and nil is invalid.
func cloneTasks(in []ProposedTask) []ProposedTask {
	out := make([]ProposedTask, len(in))
	copy(out, in)
	for i := range out {
		out[i].WriteScope = append([]string{}, in[i].WriteScope...)
		out[i].DependsOn = append([]string{}, in[i].DependsOn...)
		out[i].Checks = append([]string{}, in[i].Checks...)
		if in[i].Profile != nil {
			p := *in[i].Profile
			out[i].Profile = &p
		}
		if in[i].Budget != nil {
			b := *in[i].Budget
			out[i].Budget = &b
		}
	}
	return out
}

func findTask(in *ProposalInput, id string) *ProposedTask {
	for i := range in.Tasks {
		if in.Tasks[i].ID == id {
			return &in.Tasks[i]
		}
	}
	return nil
}
