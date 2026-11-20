package teams_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/teams"
)

const (
	testSpecRoleCoder  = "coder"
	testSpecGoalDoWork = "do work"
	testSpecTaskID     = "t1"
)

type specErrorCase struct {
	name    string
	tasks   []teams.TeamTaskSpec
	wantErr error
}

func invalidTaskSpecCases() []specErrorCase {
	return []specErrorCase{
		{name: "invalid task ID delimiters", tasks: []teams.TeamTaskSpec{{ID: "t1⟫", Spec: teams.TaskSpec{
			Role: testSpecRoleCoder, Goal: testSpecGoalDoWork, WriteScope: []string{}, Deliverable: teams.DeliverableReport,
		}}}, wantErr: teams.ErrInvalidSpec},
		{name: "invalid task ID leading dash", tasks: []teams.TeamTaskSpec{{ID: "-t1", Spec: teams.TaskSpec{
			Role: testSpecRoleCoder, Goal: testSpecGoalDoWork, WriteScope: []string{}, Deliverable: teams.DeliverableReport,
		}}}, wantErr: teams.ErrInvalidSpec},
		{name: "missing role", tasks: []teams.TeamTaskSpec{{ID: testSpecTaskID, Spec: teams.TaskSpec{
			Goal: testSpecGoalDoWork, WriteScope: []string{}, Deliverable: teams.DeliverableReport,
		}}}, wantErr: teams.ErrInvalidCatalog},
		{name: "missing goal", tasks: []teams.TeamTaskSpec{{ID: testSpecTaskID, Spec: teams.TaskSpec{
			Role: testSpecRoleCoder, WriteScope: []string{}, Deliverable: teams.DeliverableReport,
		}}}, wantErr: teams.ErrInvalidSpec},
		{name: "nil write_scope", tasks: []teams.TeamTaskSpec{{ID: testSpecTaskID, Spec: teams.TaskSpec{
			Role: testSpecRoleCoder, Goal: testSpecGoalDoWork, Deliverable: teams.DeliverableDiff,
		}}}, wantErr: teams.ErrInvalidSpec},
		{name: "invalid deliverable", tasks: []teams.TeamTaskSpec{{ID: testSpecTaskID, Spec: teams.TaskSpec{
			Role: testSpecRoleCoder, Goal: testSpecGoalDoWork, WriteScope: []string{}, Deliverable: teams.Deliverable("patch"),
		}}}, wantErr: teams.ErrInvalidSpec},
		{name: "invalid output_schema json", tasks: []teams.TeamTaskSpec{{ID: testSpecTaskID, Spec: teams.TaskSpec{
			Role: testSpecRoleCoder, Goal: testSpecGoalDoWork, WriteScope: []string{}, Deliverable: teams.DeliverableReport,
			OutputSchema: json.RawMessage(`{not json`),
		}}}, wantErr: teams.ErrInvalidSpec},
		{name: "cross-team depends_on", tasks: []teams.TeamTaskSpec{{ID: testSpecTaskID, Spec: teams.TaskSpec{
			Role: testSpecRoleCoder, Goal: testSpecGoalDoWork, WriteScope: []string{}, Deliverable: teams.DeliverableDiff,
			DependsOn: []string{"other-team-task"},
		}}}, wantErr: teams.ErrMissingDependency},
		{name: "self dependency cycle", tasks: []teams.TeamTaskSpec{{ID: testSpecTaskID, Spec: teams.TaskSpec{
			Role: testSpecRoleCoder, Goal: testSpecGoalDoWork, WriteScope: []string{}, Deliverable: teams.DeliverableDiff,
			DependsOn: []string{testSpecTaskID},
		}}}, wantErr: teams.ErrCyclicDependency},
		{name: "3-node dependency cycle", tasks: []teams.TeamTaskSpec{
			{ID: testSpecTaskID, Spec: teams.TaskSpec{Role: testSpecRoleCoder, Goal: "g1", WriteScope: []string{}, Deliverable: teams.DeliverableDiff, DependsOn: []string{"t2"}}},
			{ID: "t2", Spec: teams.TaskSpec{Role: testSpecRoleCoder, Goal: "g2", WriteScope: []string{}, Deliverable: teams.DeliverableDiff, DependsOn: []string{"t3"}}},
			{ID: "t3", Spec: teams.TaskSpec{Role: testSpecRoleCoder, Goal: "g3", WriteScope: []string{}, Deliverable: teams.DeliverableDiff, DependsOn: []string{testSpecTaskID}}},
		}, wantErr: teams.ErrCyclicDependency},
	}
}

func TestTaskSpecValidationAndDependsOnCycles(t *testing.T) {
	t.Parallel()
	validDAG := []teams.TeamTaskSpec{
		{
			ID: "t-plan",
			Spec: teams.TaskSpec{
				Role: "researcher", Goal: "Investigate auth", Context: "internal/auth",
				WriteScope: []string{}, Deliverable: teams.DeliverableReport,
				OutputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
		{
			ID: "t-code",
			Spec: teams.TaskSpec{
				Role: testSpecRoleCoder, Goal: "Implement token check", WriteScope: []string{"internal/auth/*.go"},
				Deliverable: teams.DeliverableBoth, DependsOn: []string{"t-plan"}, Checks: []string{"task check"},
				Profile: &teams.ProfileOverride{Vendor: teams.VendorCodex, Model: "gpt-5-codex", Effort: teams.EffortHigh},
				Budget:  &teams.TaskBudget{MaxTurns: 12, MaxTokens: 50000, MaxDurationSeconds: 300},
			},
		},
	}
	if err := teams.ValidateTeamTaskSpecs(validDAG); err != nil {
		t.Fatalf("valid DAG rejected: %v", err)
	}
	protected := validDAG[1].Spec.ProtectedContent()
	if protected.Goal != "Implement token check" || len(protected.Checks) != 1 {
		t.Fatalf("unexpected ProtectedContent: %+v", protected)
	}
	for _, tc := range invalidTaskSpecCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := teams.ValidateTeamTaskSpecs(tc.tasks); !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateTeamTaskSpecs() err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateTeamBudget(t *testing.T) {
	t.Parallel()
	if err := teams.ValidateTeamBudget(nil); err != nil {
		t.Fatalf("ValidateTeamBudget(nil) = %v", err)
	}
	if err := teams.ValidateTeamBudget(&teams.TeamBudget{MaxAgents: 20, MaxDurationSeconds: 28800}); err != nil {
		t.Fatalf("ValidateTeamBudget(valid) = %v", err)
	}
	for _, tc := range []struct {
		name string
		b    teams.TeamBudget
	}{
		{"negative agents", teams.TeamBudget{MaxAgents: -1}},
		{"agents over ceiling", teams.TeamBudget{MaxAgents: teams.MaxTeamBudgetAgents + 1}},
		{"negative duration", teams.TeamBudget{MaxDurationSeconds: -5}},
		{"duration over ceiling", teams.TeamBudget{MaxDurationSeconds: teams.MaxTeamBudgetDurationSeconds + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := teams.ValidateTeamBudget(&tc.b); !errors.Is(err, teams.ErrInvalidSpec) {
				t.Fatalf("ValidateTeamBudget(%+v) = %v, want ErrInvalidSpec", tc.b, err)
			}
		})
	}
}
