package teams

import (
	"errors"
	"strings"
	"testing"
)

var testCatalog = []Role{
	{Name: "researcher", Vendor: VendorClaude, Permission: PermissionRead, IsLeadEligible: true},
	{Name: "coder", Vendor: VendorCodex, Model: "codex-mini", Permission: PermissionWrite, IsLeadEligible: true},
}

func validProposalTasks() []ProposedTask {
	return []ProposedTask{
		{ID: "t-lead", TeamRole: RoleLead, Role: "researcher", Goal: "Coordinate the streams", WriteScope: []string{}, Deliverable: DeliverableReport},
		{ID: "t-a", ParentTaskID: "t-lead", TeamRole: RoleMember, Role: "coder", Goal: "Write the lexer", WriteScope: []string{"internal/lexer/**"}, Deliverable: DeliverableDiff},
		{ID: "t-b", ParentTaskID: "t-lead", TeamRole: RoleMember, Role: "coder", Goal: "Write the parser", WriteScope: []string{"internal/parser/**"}, Deliverable: DeliverableDiff},
	}
}

func validProposalInput() *ProposalInput {
	return &ProposalInput{Title: "Split the syntax work", Brief: "Two independent write streams.", Tasks: validProposalTasks()}
}

func TestProposalValidatesAgainstTheRoleCatalog(t *testing.T) {
	t.Parallel()
	if _, err := ValidateProposal(validProposalInput(), testCatalog); err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	unknownRole := validProposalInput()
	unknownRole.Tasks[1].Role = "astronaut"
	if _, err := ValidateProposal(unknownRole, testCatalog); !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("unknown task role err = %v, want ErrUnknownRole", err)
	}
	// A proposed new role satisfies the reference after its own shape validates.
	withNewRole := validProposalInput()
	withNewRole.Roles = []ProposedRole{{Name: "astronaut", Vendor: VendorAGY, Permission: PermissionRead}}
	withNewRole.Tasks[1].Role = "astronaut"
	if _, err := ValidateProposal(withNewRole, testCatalog); err != nil {
		t.Fatalf("proposed role rejected: %v", err)
	}
	// A new role entry that fails catalog validation is a typed error.
	badNewRole := validProposalInput()
	badNewRole.Roles = []ProposedRole{{Name: "astronaut", Vendor: "gopher", Permission: PermissionRead}}
	badNewRole.Tasks[1].Role = "astronaut"
	if _, err := ValidateProposal(badNewRole, testCatalog); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("invalid new role err = %v, want ErrInvalidCatalog", err)
	}
	// The §4 task contract is enforced by the F11.1 validators.
	noGoal := validProposalInput()
	noGoal.Tasks[2].Goal = " "
	if _, err := ValidateProposal(noGoal, testCatalog); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("empty goal err = %v, want ErrInvalidSpec", err)
	}
	// Depth stays at 2 (OR-01): a member cannot gain children.
	tooDeep := validProposalInput()
	tooDeep.Tasks = append(tooDeep.Tasks, ProposedTask{
		ID: "t-c", ParentTaskID: "t-a", TeamRole: RoleMember, Role: "coder",
		Goal: "Deeper", WriteScope: []string{"internal/deep/**"}, Deliverable: DeliverableDiff,
	})
	// The natural input order surfaces the member-shape rule; the depth-2
	// rule (OR-01) rejects the proposal either way, and the raw tree rule is
	// F11.1 evidence (TestTeamDepthAndRoleShapeAreEnforced).
	if _, err := ValidateProposal(tooDeep, testCatalog); !errors.Is(err, ErrMemberCannotHaveChildren) {
		t.Fatalf("depth 3 err = %v, want ErrMemberCannotHaveChildren", err)
	}
	// depends_on cycles stay rejected (§4).
	cycle := validProposalInput()
	cycle.Tasks[1].DependsOn = []string{"t-b"}
	cycle.Tasks[2].DependsOn = []string{"t-a"}
	if _, err := ValidateProposal(cycle, testCatalog); !errors.Is(err, ErrCyclicDependency) {
		t.Fatalf("cycle err = %v, want ErrCyclicDependency", err)
	}
	for name, mutate := range map[string]func(*ProposalInput){
		"empty title": func(in *ProposalInput) { in.Title = " " },
		"long title":  func(in *ProposalInput) { in.Title = strings.Repeat("t", maxTitleBytes+1) },
		"empty brief": func(in *ProposalInput) { in.Brief = "" },
		"no tasks":    func(in *ProposalInput) { in.Tasks = nil },
		"too many tasks": func(in *ProposalInput) {
			in.Tasks = make([]ProposedTask, MaxProposalTasks+1)
			for i := range in.Tasks {
				in.Tasks[i] = ProposedTask{ID: "t" + strings.Repeat("x", i/10) + string(rune('a'+i%26)), TeamRole: RoleMember, Role: "coder", Goal: "g", WriteScope: []string{"a"}, Deliverable: DeliverableDiff}
			}
			in.Tasks[0] = ProposedTask{ID: "lead", TeamRole: RoleLead, Role: "coder", Goal: "g", WriteScope: []string{}, Deliverable: DeliverableReport}
		},
	} {
		in := validProposalInput()
		mutate(in)
		if _, err := ValidateProposal(in, testCatalog); !errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("%s err = %v, want ErrInvalidSpec", name, err)
		}
	}
}

func TestOverlappingWriteScopeWithoutDependsOnWarns(t *testing.T) {
	t.Parallel()
	in := validProposalInput()
	in.Tasks[1].WriteScope = []string{"internal/shared/**"}
	in.Tasks[2].WriteScope = []string{"internal/shared/**", "docs/x.md"}
	warnings, err := ValidateProposal(in, testCatalog)
	if err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	if !hasWarning(warnings, WarnScopeOverlap) {
		t.Fatalf("overlapping scopes produced no warning: %+v", warnings)
	}
	ordered := validProposalInput()
	ordered.Tasks[1].WriteScope = []string{"internal/shared/**"}
	ordered.Tasks[2].WriteScope = []string{"internal/shared/other.md"}
	ordered.Tasks[2].DependsOn = []string{"t-a"}
	warnings, err = ValidateProposal(ordered, testCatalog)
	if err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	if hasWarning(warnings, WarnScopeOverlap) {
		t.Fatalf("depends_on pair warned: %+v", warnings)
	}
	disjoint := validProposalInput()
	warnings, err = ValidateProposal(disjoint, testCatalog)
	if err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	if hasWarning(warnings, WarnScopeOverlap) {
		t.Fatalf("disjoint scopes warned: %+v", warnings)
	}
	// A literal path under a glob pattern overlaps conservatively.
	literal := validProposalInput()
	literal.Tasks[1].WriteScope = []string{"internal/shared/one.go"}
	literal.Tasks[2].WriteScope = []string{"internal/shared/**"}
	warnings, err = ValidateProposal(literal, testCatalog)
	if err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	if !hasWarning(warnings, WarnScopeOverlap) {
		t.Fatalf("literal under glob not warned: %+v", warnings)
	}
}

func TestProposalWarningsNameNewRolesAndUnverifiedModels(t *testing.T) {
	t.Parallel()
	in := validProposalInput()
	in.Roles = []ProposedRole{{Name: "critic", Vendor: VendorClaude, Model: "opus-x", Permission: PermissionRead}}
	in.Tasks[2].Role = "critic"
	warnings, err := ValidateProposal(in, testCatalog)
	if err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	if !hasWarning(warnings, WarnNewRole) {
		t.Fatalf("new role not warned: %+v", warnings)
	}
	// A task-level profile override of the model is unverified: no vendor
	// publishes a model list the host could check in stage A (§5.3).
	override := validProposalInput()
	override.Tasks[2].Profile = &ProfileOverride{Model: "codex-max"}
	warnings, err = ValidateProposal(override, testCatalog)
	if err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	if !hasWarning(warnings, WarnModelUnverified) {
		t.Fatalf("model override not warned: %+v", warnings)
	}
	// A catalog role without an override is vetted and stays unwarned.
	if _, err := ValidateProposal(validProposalInput(), testCatalog); err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	if clean, err := ValidateProposal(validProposalInput(), testCatalog); err != nil || len(clean) != 0 {
		t.Fatalf("clean proposal warnings = %+v, err %v, want none", clean, err)
	}
}

func TestAgentCountCountsManagersAndMembers(t *testing.T) {
	t.Parallel()
	if got := AgentCount(validProposalInput()); got != 2 {
		t.Fatalf("agent count = %d, want 2", got)
	}
	solo := &ProposalInput{Title: "t", Brief: "b", Tasks: []ProposedTask{{ID: "s", TeamRole: RoleSolo, Role: "researcher", Goal: "g", WriteScope: []string{}, Deliverable: DeliverableReport}}}
	if got := AgentCount(solo); got != 0 {
		t.Fatalf("solo agent count = %d, want 0", got)
	}
}

func hasWarning(warnings []Warning, code string) bool {
	for i := range warnings {
		if warnings[i].Code == code {
			return true
		}
	}
	return false
}
