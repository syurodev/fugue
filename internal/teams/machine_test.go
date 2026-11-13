package teams_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
)

const (
	testTaskLead = "t-lead"
	testTaskMgr1 = "t-mgr1"
	testTaskMgr2 = "t-mgr2"
	testTask1    = "t-1"
)

func expectedTaskTransitions() map[teams.TaskTransitionKey]teams.TaskState {
	return map[teams.TaskTransitionKey]teams.TaskState{
		{From: teams.TaskStateQueued, Event: teams.TaskEventAssign}:          teams.TaskStateStarting,
		{From: teams.TaskStateQueued, Event: teams.TaskEventFail}:            teams.TaskStateFailed,
		{From: teams.TaskStateQueued, Event: teams.TaskEventCancel}:          teams.TaskStateCancelled,
		{From: teams.TaskStateStarting, Event: teams.TaskEventStart}:         teams.TaskStateRunning,
		{From: teams.TaskStateStarting, Event: teams.TaskEventStop}:          teams.TaskStateStopped,
		{From: teams.TaskStateStarting, Event: teams.TaskEventFail}:          teams.TaskStateFailed,
		{From: teams.TaskStateStarting, Event: teams.TaskEventCancel}:        teams.TaskStateCancelled,
		{From: teams.TaskStateStarting, Event: teams.TaskEventHostRestart}:   teams.TaskStateUnknown,
		{From: teams.TaskStateRunning, Event: teams.TaskEventAsk}:            teams.TaskStateAsking,
		{From: teams.TaskStateRunning, Event: teams.TaskEventReport}:         teams.TaskStateChecking,
		{From: teams.TaskStateRunning, Event: teams.TaskEventStop}:           teams.TaskStateStopped,
		{From: teams.TaskStateRunning, Event: teams.TaskEventFail}:           teams.TaskStateFailed,
		{From: teams.TaskStateRunning, Event: teams.TaskEventCancel}:         teams.TaskStateCancelled,
		{From: teams.TaskStateRunning, Event: teams.TaskEventHostRestart}:    teams.TaskStateUnknown,
		{From: teams.TaskStateAsking, Event: teams.TaskEventAnswer}:          teams.TaskStateRunning,
		{From: teams.TaskStateAsking, Event: teams.TaskEventStop}:            teams.TaskStateStopped,
		{From: teams.TaskStateAsking, Event: teams.TaskEventFail}:            teams.TaskStateFailed,
		{From: teams.TaskStateAsking, Event: teams.TaskEventCancel}:          teams.TaskStateCancelled,
		{From: teams.TaskStateAsking, Event: teams.TaskEventHostRestart}:     teams.TaskStateUnknown,
		{From: teams.TaskStateChecking, Event: teams.TaskEventChecksPassed}:  teams.TaskStateReview,
		{From: teams.TaskStateChecking, Event: teams.TaskEventChecksFailed}:  teams.TaskStateFailed,
		{From: teams.TaskStateChecking, Event: teams.TaskEventStop}:          teams.TaskStateStopped,
		{From: teams.TaskStateChecking, Event: teams.TaskEventFail}:          teams.TaskStateFailed,
		{From: teams.TaskStateChecking, Event: teams.TaskEventCancel}:        teams.TaskStateCancelled,
		{From: teams.TaskStateChecking, Event: teams.TaskEventHostRestart}:   teams.TaskStateUnknown,
		{From: teams.TaskStateReview, Event: teams.TaskEventApprove}:         teams.TaskStateIntegrating,
		{From: teams.TaskStateReview, Event: teams.TaskEventRequestChanges}:  teams.TaskStateRunning,
		{From: teams.TaskStateReview, Event: teams.TaskEventCompleteNoDiff}:  teams.TaskStateDone,
		{From: teams.TaskStateReview, Event: teams.TaskEventFail}:            teams.TaskStateFailed,
		{From: teams.TaskStateReview, Event: teams.TaskEventCancel}:          teams.TaskStateCancelled,
		{From: teams.TaskStateIntegrating, Event: teams.TaskEventIntegrate}:  teams.TaskStateDone,
		{From: teams.TaskStateIntegrating, Event: teams.TaskEventFail}:       teams.TaskStateFailed,
		{From: teams.TaskStateIntegrating, Event: teams.TaskEventCancel}:     teams.TaskStateCancelled,
		{From: teams.TaskStateUnknown, Event: teams.TaskEventDecisionResume}: teams.TaskStateStarting,
		{From: teams.TaskStateUnknown, Event: teams.TaskEventDecisionKeep}:   teams.TaskStateReview,
		{From: teams.TaskStateUnknown, Event: teams.TaskEventDecisionCancel}: teams.TaskStateCancelled,
	}
}

func assertTaskStateEventPair(t *testing.T, s teams.TaskState, e teams.TaskEvent, allowed map[teams.TaskTransitionKey]teams.TaskState) {
	t.Helper()
	wantNext, ok := allowed[teams.TaskTransitionKey{From: s, Event: e}]
	got, err := teams.TransitionTask(teams.TaskTransitionInput{
		From: s, Event: e, Role: teams.RoleMember, ChildrenTerminal: true, FailureCode: "test_failure",
	})
	if ok {
		if err != nil {
			t.Fatalf("TransitionTask(%s, %s) unexpected err: %v", s, e, err)
		}
		if diff := cmp.Diff(wantNext, got); diff != "" {
			t.Fatalf("TransitionTask(%s, %s) state mismatch (-want +got):\n%s", s, e, diff)
		}
		return
	}
	var te *teams.TransitionError
	if err == nil || !errors.As(err, &te) || !errors.Is(err, teams.ErrInvalidTransition) || te.Code() != errcode.FailedPrecondition {
		t.Fatalf("TransitionTask(%s, %s) = (%q, %v), want invalid *TransitionError", s, e, got, err)
	}
}

func TestTaskTransitionTableCoversEveryStateAndEvent(t *testing.T) {
	t.Parallel()
	allowed := expectedTaskTransitions()
	states := teams.AllTaskStates()
	events := teams.AllTaskEvents()
	if len(states) != 12 {
		t.Fatalf("expected 12 task states, got %d", len(states))
	}
	for _, s := range states {
		if !teams.IsValidTaskState(s) {
			t.Fatalf("IsValidTaskState(%q) = false", s)
		}
		for _, e := range events {
			assertTaskStateEventPair(t, s, e, allowed)
		}
	}
	assertTaskPredicates(t, states)
}

func assertTaskPredicates(t *testing.T, states []teams.TaskState) {
	t.Helper()
	for _, s := range states {
		wantRestart := s == teams.TaskStateRunning || s == teams.TaskStateStarting || s == teams.TaskStateAsking || s == teams.TaskStateChecking
		if s.NeedsUnknownOnRestart() != wantRestart {
			t.Fatalf("NeedsUnknownOnRestart(%s) = %v, want %v", s, s.NeedsUnknownOnRestart(), wantRestart)
		}
	}
	if _, err := teams.TransitionTask(teams.TaskTransitionInput{From: teams.TaskStateRunning, Event: teams.TaskEventFail, Role: teams.RoleMember}); err == nil {
		t.Fatal("missing FailureCode unexpectedly succeeded")
	}
	if teams.IsValidTaskState(teams.TaskState("bogus")) || !teams.TaskStateDone.IsTerminal() || teams.TaskStateUnknown.IsTerminal() {
		t.Fatal("unexpected state predicate result")
	}
}

func expectedTeamTransitions() map[teams.TeamTransitionKey]teams.TeamState {
	return map[teams.TeamTransitionKey]teams.TeamState{
		{From: teams.TeamStateProposed, Event: teams.TeamEventStart}:              teams.TeamStateActive,
		{From: teams.TeamStateProposed, Event: teams.TeamEventCancel}:             teams.TeamStateCancelled,
		{From: teams.TeamStateActive, Event: teams.TeamEventRequestFinalReview}:   teams.TeamStateFinalReview,
		{From: teams.TeamStateActive, Event: teams.TeamEventCancel}:               teams.TeamStateCancelled,
		{From: teams.TeamStateActive, Event: teams.TeamEventFail}:                 teams.TeamStateFailed,
		{From: teams.TeamStateFinalReview, Event: teams.TeamEventRequestRevision}: teams.TeamStateActive,
		{From: teams.TeamStateFinalReview, Event: teams.TeamEventComplete}:        teams.TeamStateDone,
		{From: teams.TeamStateFinalReview, Event: teams.TeamEventCancel}:          teams.TeamStateCancelled,
		{From: teams.TeamStateFinalReview, Event: teams.TeamEventFail}:            teams.TeamStateFailed,
	}
}

func assertTeamStateEventPair(t *testing.T, s teams.TeamState, e teams.TeamEvent, allowed map[teams.TeamTransitionKey]teams.TeamState) {
	t.Helper()
	wantNext, ok := allowed[teams.TeamTransitionKey{From: s, Event: e}]
	got, err := teams.TransitionTeam(s, e)
	if ok {
		if err != nil || got != wantNext {
			t.Fatalf("TransitionTeam(%s, %s) = (%s, %v), want %s", s, e, got, err, wantNext)
		}
		return
	}
	var te *teams.TransitionError
	if err == nil || !errors.As(err, &te) || !errors.Is(err, teams.ErrInvalidTransition) {
		t.Fatalf("TransitionTeam(%s, %s) = (%q, %v), want *TransitionError", s, e, got, err)
	}
}

func TestTeamTransitionTableCoversEveryStateAndEvent(t *testing.T) {
	t.Parallel()
	allowed := expectedTeamTransitions()
	states := teams.AllTeamStates()
	if len(states) != 6 {
		t.Fatalf("expected 6 team states, got %d", len(states))
	}
	for _, s := range states {
		if !teams.IsValidTeamState(s) {
			t.Fatalf("IsValidTeamState(%q) = false", s)
		}
		for _, e := range teams.AllTeamEvents() {
			assertTeamStateEventPair(t, s, e, allowed)
		}
	}
	if teams.IsValidTeamState(teams.TeamState("bogus")) || !teams.TeamStateDone.IsTerminal() || !teams.TeamStateFailed.IsTerminal() || teams.TeamStateActive.IsTerminal() {
		t.Fatal("unexpected team state predicate")
	}
}

func TestTeamManagerChecksOnlyAfterAllChildrenAreTerminal(t *testing.T) {
	t.Parallel()
	_, err := teams.TransitionTask(teams.TaskTransitionInput{
		From: teams.TaskStateRunning, Event: teams.TaskEventReport, Role: teams.RoleTeamManager, ChildrenTerminal: false,
	})
	if !errors.Is(err, teams.ErrChildrenNotTerminal) || !errors.Is(err, teams.ErrInvalidTransition) {
		t.Fatalf("manager report with non-terminal children err = %v, want ErrChildrenNotTerminal", err)
	}
	next, err := teams.TransitionTask(teams.TaskTransitionInput{
		From: teams.TaskStateRunning, Event: teams.TaskEventReport, Role: teams.RoleTeamManager, ChildrenTerminal: true,
	})
	if err != nil || next != teams.TaskStateChecking {
		t.Fatalf("manager report with terminal children = (%s, %v), want (checking, nil)", next, err)
	}
}

type hierarchyErrorCase struct {
	name    string
	nodes   []teams.TaskNode
	wantErr error
}

func teamHierarchyErrorCases() []hierarchyErrorCase {
	return []hierarchyErrorCase{
		{name: "empty tree", nodes: nil, wantErr: teams.ErrInvalidSpec},
		{name: "member cannot have children", nodes: []teams.TaskNode{
			{ID: testTaskLead, Role: teams.RoleLead},
			{ID: "t-m1", ParentTaskID: testTaskLead, Role: teams.RoleMember},
			{ID: "t-m2", ParentTaskID: "t-m1", Role: teams.RoleMember},
		}, wantErr: teams.ErrMemberCannotHaveChildren},
		{name: "depth exceeds 2", nodes: []teams.TaskNode{
			{ID: testTaskLead, Role: teams.RoleLead},
			{ID: testTaskMgr1, ParentTaskID: testTaskLead, Role: teams.RoleTeamManager},
			{ID: testTaskMgr2, ParentTaskID: testTaskMgr1, Role: teams.RoleTeamManager},
			{ID: "t-m", ParentTaskID: testTaskMgr2, Role: teams.RoleMember},
		}, wantErr: teams.ErrDepthExceeded},
		{name: "team_manager cannot parent team_manager", nodes: []teams.TaskNode{
			{ID: testTaskLead, Role: teams.RoleLead},
			{ID: testTaskMgr1, ParentTaskID: testTaskLead, Role: teams.RoleTeamManager},
			{ID: testTaskMgr2, ParentTaskID: testTaskMgr1, Role: teams.RoleTeamManager},
		}, wantErr: teams.ErrInvalidRole},
		{name: "solo cannot mix", nodes: []teams.TaskNode{
			{ID: testTaskLead, Role: teams.RoleLead},
			{ID: "t-solo", ParentTaskID: testTaskLead, Role: teams.RoleSolo},
		}, wantErr: teams.ErrInvalidRole},
		{name: "unknown role", nodes: []teams.TaskNode{{ID: testTask1, Role: teams.TeamRole("boss")}}, wantErr: teams.ErrInvalidRole},
		{name: "parent cycle", nodes: []teams.TaskNode{
			{ID: testTask1, ParentTaskID: "t-2", Role: teams.RoleTeamManager},
			{ID: "t-2", ParentTaskID: testTask1, Role: teams.RoleLead},
		}, wantErr: teams.ErrCyclicDependency},
		{name: "missing parent", nodes: []teams.TaskNode{{ID: testTask1, ParentTaskID: "missing", Role: teams.RoleMember}}, wantErr: teams.ErrMissingDependency},
	}
}

func TestTeamDepthAndRoleShapeAreEnforced(t *testing.T) {
	t.Parallel()
	validTeam := []teams.TaskNode{
		{ID: testTaskLead, Role: teams.RoleLead},
		{ID: "t-mgr", ParentTaskID: testTaskLead, Role: teams.RoleTeamManager},
		{ID: "t-direct-member", ParentTaskID: testTaskLead, Role: teams.RoleMember},
		{ID: "t-sub-member", ParentTaskID: "t-mgr", Role: teams.RoleMember},
	}
	if err := teams.ValidateTeamTree(validTeam); err != nil {
		t.Fatalf("valid depth-2 tree rejected: %v", err)
	}
	if err := teams.ValidateTeamTree([]teams.TaskNode{{ID: "t-solo", Role: teams.RoleSolo}}); err != nil {
		t.Fatalf("valid solo task rejected: %v", err)
	}
	for _, tc := range teamHierarchyErrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := teams.ValidateTeamTree(tc.nodes); !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateTeamTree() err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
