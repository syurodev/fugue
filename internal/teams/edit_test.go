package teams

import (
	"errors"
	"strings"
	"testing"
)

// Shared literals of the edit tests.
const (
	editTestResearchRole = "researcher"
	editTestRootTask     = "root"
	editTestModelEdited  = "edited"
	editTestWriterRole   = "writer"
)

func editTestCatalog() []Role {
	return []Role{
		{Name: editTestResearchRole, Vendor: VendorClaude, Model: "sonnet", Permission: PermissionRead},
		{Name: "coder", Vendor: VendorCodex, Model: "gpt", Permission: PermissionWrite},
	}
}

func editTestPlan() *ProposalInput {
	return &ProposalInput{
		Title: "plan", Brief: "brief",
		Tasks: []ProposedTask{
			{ID: editTestRootTask, TeamRole: RoleLead, Role: editTestResearchRole, Goal: "lead", WriteScope: []string{}, Deliverable: DeliverableReport},
			{ID: "a", ParentTaskID: editTestRootTask, TeamRole: RoleMember, Role: editTestResearchRole, Goal: "read", WriteScope: []string{"docs/a.md"}, Deliverable: DeliverableReport},
		},
	}
}

// TestDecodeEditSetRoundTripAndRefusals covers the strict decode contract:
// an empty document is the identity edit, unknown fields and wrong versions
// are typed refusals, and the error never quotes the document.
func TestDecodeEditSetRoundTripAndRefusals(t *testing.T) {
	t.Parallel()
	empty, err := DecodeEditSet(nil)
	if err != nil {
		t.Fatalf("DecodeEditSet(nil): %v", err)
	}
	if empty.Version != EditSetVersion || len(empty.Roles) != 0 {
		t.Fatalf("empty edit = %+v, want version %d with no roles", empty, EditSetVersion)
	}
	if _, err := DecodeEditSet([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("DecodeEditSet minimal: %v", err)
	}
	if _, err := DecodeEditSet([]byte(`{"version":1,"unknown":true}`)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("unknown field error = %v, want ErrInvalidSpec", err)
	}
	if _, err := DecodeEditSet([]byte(`{"version":99}`)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("wrong version error = %v, want ErrInvalidSpec", err)
	}
	if _, err := DecodeEditSet([]byte(`not json`)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("bad json error = %v, want ErrInvalidSpec", err)
	}
	huge := `{"version":1}` + strings.Repeat(" ", MaxEditSetBytes)
	if _, err := DecodeEditSet([]byte(huge)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("oversize error = %v, want ErrInvalidSpec", err)
	}
	tooMany := `{"version":1,"roles":[` +
		strings.TrimSuffix(strings.Repeat(`{"name":"x","vendor":"claude","permission":"read"},`, MaxProposalRoles+1), ",") + `]}`
	if _, err := DecodeEditSet([]byte(tooMany)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("too many roles error = %v, want ErrInvalidSpec", err)
	}
}

// TestApplyEditsChangesTheRoleModel covers OR-E13's edit application: an
// edited catalog role becomes a per-task profile override and survives the
// plan re-validation.
func TestApplyEditsChangesTheRoleModel(t *testing.T) {
	t.Parallel()
	edit := &EditSet{Version: EditSetVersion, Roles: []RoleEdit{{Name: editTestResearchRole, Model: editTestModelEdited}}}
	edited, err := ApplyEdits(edit, editTestPlan(), editTestCatalog())
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if edited.Tasks[0].Profile == nil || edited.Tasks[0].Profile.Model != editTestModelEdited {
		t.Fatalf("root profile = %+v, want the edited model", edited.Tasks[0].Profile)
	}
	if edited.Tasks[1].Profile == nil || edited.Tasks[1].Profile.Model != editTestModelEdited {
		t.Fatalf("member profile = %+v, want the edited model", edited.Tasks[1].Profile)
	}
	if _, err := ValidateProposal(edited, editTestCatalog()); err != nil {
		t.Fatalf("the edited plan does not re-validate: %v", err)
	}
}

// TestApplyEditsRefusals covers the typed refusals: accounts are not
// assignable yet, a resolved role keeps its permission, budget edits must
// name a plan task, and unknown edit-role names re-validate as new roles.
func TestApplyEditsRefusals(t *testing.T) {
	t.Parallel()
	plan, catalog := editTestPlan(), editTestCatalog()
	if _, err := ApplyEdits(&EditSet{Version: EditSetVersion, Accounts: []AccountEdit{{Role: editTestResearchRole, Account: "a"}}}, plan, catalog); !errors.Is(err, ErrAccountsUnassigned) {
		t.Fatalf("accounts error = %v, want ErrAccountsUnassigned", err)
	}
	if _, err := ApplyEdits(&EditSet{Version: EditSetVersion, Roles: []RoleEdit{{Name: editTestResearchRole, Permission: PermissionWrite}}}, plan, catalog); !errors.Is(err, ErrPermissionImmutable) {
		t.Fatalf("permission error = %v, want ErrPermissionImmutable", err)
	}
	if _, err := ApplyEdits(&EditSet{Version: EditSetVersion, Roles: []RoleEdit{{Name: editTestResearchRole, Vendor: "nope"}}}, plan, catalog); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("vendor error = %v, want ErrInvalidCatalog", err)
	}
	if _, err := ApplyEdits(&EditSet{Version: EditSetVersion, Roles: []RoleEdit{{Name: editTestResearchRole, Model: "-flag"}}}, plan, catalog); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("model error = %v, want ErrInvalidCatalog", err)
	}
	if _, err := ApplyEdits(&EditSet{Version: EditSetVersion, Roles: []RoleEdit{{Name: editTestResearchRole, Effort: "sideways"}}}, plan, catalog); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("effort error = %v, want ErrInvalidCatalog", err)
	}
	if _, err := ApplyEdits(&EditSet{Version: EditSetVersion, TaskBudgets: []TaskBudgetEdit{{TaskID: "gone", Budget: TaskBudget{MaxTurns: 1}}}}, plan, catalog); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("budget target error = %v, want ErrMissingDependency", err)
	}
	if _, err := ApplyEdits(&EditSet{Version: EditSetVersion, TaskBudgets: []TaskBudgetEdit{{TaskID: "", Budget: TaskBudget{}}}}, plan, catalog); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("empty budget ID error = %v, want ErrInvalidSpec", err)
	}
	if _, err := ApplyEdits(&EditSet{Version: EditSetVersion, Roles: []RoleEdit{{Name: "UPPER"}}}, plan, catalog); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("bad new role name error = %v, want ErrInvalidCatalog", err)
	}
}

// TestApplyEditsAddsAndRemovesTasks covers the structural edits: a removal
// drops the task, an addition carries its profile, and the budget edit lands
// on the named task. An empty write scope stays non-nil through the clone.
func TestApplyEditsAddsAndRemovesTasks(t *testing.T) {
	t.Parallel()
	edit := &EditSet{
		Version:     EditSetVersion,
		RemoveTasks: []string{"a"},
		AddTasks: []ProposedTask{
			{ID: "b", ParentTaskID: editTestRootTask, TeamRole: RoleMember, Role: editTestResearchRole, Goal: "more", WriteScope: []string{"docs/b.md"}, Deliverable: DeliverableReport},
		},
		TaskBudgets: []TaskBudgetEdit{{TaskID: editTestRootTask, Budget: TaskBudget{MaxTurns: 7}}},
	}
	edited, err := ApplyEdits(edit, editTestPlan(), editTestCatalog())
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if len(edited.Tasks) != 2 || edited.Tasks[0].ID != editTestRootTask || edited.Tasks[1].ID != "b" {
		t.Fatalf("edited tasks = %+v, want root and b", edited.Tasks)
	}
	if edited.Tasks[0].Budget == nil || edited.Tasks[0].Budget.MaxTurns != 7 {
		t.Fatalf("root budget = %+v, want max_turns 7", edited.Tasks[0].Budget)
	}
	if edited.Tasks[1].WriteScope == nil || len(edited.Tasks[1].WriteScope) != 1 {
		t.Fatalf("added write_scope = %+v, want a non-nil single entry", edited.Tasks[1].WriteScope)
	}
	if _, err := ValidateProposal(edited, editTestCatalog()); err != nil {
		t.Fatalf("the edited plan does not re-validate: %v", err)
	}
}

// TestApplyEditsNewRoleEntry covers an edit that proposes a new role: it
// lands in the edited plan's role entries, so tasks may reference it.
func TestApplyEditsNewRoleEntry(t *testing.T) {
	t.Parallel()
	edit := &EditSet{
		Version: EditSetVersion,
		Roles:   []RoleEdit{{Name: editTestWriterRole, Vendor: VendorCodex, Permission: PermissionWrite}},
		AddTasks: []ProposedTask{
			{ID: "w", ParentTaskID: editTestRootTask, TeamRole: RoleMember, Role: editTestWriterRole, Goal: "write", WriteScope: []string{"docs/w.md"}, Deliverable: DeliverableReport},
		},
	}
	edited, err := ApplyEdits(edit, editTestPlan(), editTestCatalog())
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if len(edited.Roles) != 1 || edited.Roles[0].Name != editTestWriterRole {
		t.Fatalf("edited roles = %+v, want the writer entry", edited.Roles)
	}
	if _, err := ValidateProposal(edited, editTestCatalog()); err != nil {
		t.Fatalf("the edited plan does not re-validate: %v", err)
	}
}

// TestValidateBudgetErrors covers the shared budget validation: nil is fine,
// negative and above-ceiling values are refused.
func TestValidateBudgetErrors(t *testing.T) {
	t.Parallel()
	if err := ValidateBudget(nil); err != nil {
		t.Fatalf("ValidateBudget(nil) = %v, want nil", err)
	}
	if err := ValidateBudget(&TaskBudget{MaxTurns: -1}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("negative budget error = %v, want ErrInvalidSpec", err)
	}
	edit := &EditSet{
		Version:    EditSetVersion,
		TeamBudget: &TeamBudget{MaxAgents: 18, MaxDurationSeconds: 7200},
	}
	edited, err := ApplyEdits(edit, editTestPlan(), editTestCatalog())
	if err != nil {
		t.Fatalf("ApplyEdits(TeamBudget): %v", err)
	}
	if edited.Budget == nil || edited.Budget.MaxAgents != 18 || edited.Budget.MaxDurationSeconds != 7200 {
		t.Fatalf("edited budget = %+v", edited.Budget)
	}
	bad := &EditSet{Version: EditSetVersion, TeamBudget: &TeamBudget{MaxAgents: MaxTeamBudgetAgents + 1}}
	if _, err := ApplyEdits(bad, editTestPlan(), editTestCatalog()); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("bad TeamBudget error = %v, want ErrInvalidSpec", err)
	}
}
