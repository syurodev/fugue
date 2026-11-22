package client_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/syurodev/fugue/pkg/client"
	tasksv1 "github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1"
)

const (
	testReadTeamID    = "team-read-1"
	testReadSessionID = "sess-read-1"
)

func (f *tasksFake) ListTeams(_ context.Context, req *connect.Request[tasksv1.ListTeamsRequest]) (*connect.Response[tasksv1.ListTeamsResponse], error) {
	if req.Msg.GetWorkspaceId() == "err-ws" {
		return nil, errTasksDetail("tasks.team_not_found", connect.CodeNotFound)
	}
	ts := timestamppb.New(time.Unix(1700000000, 0).UTC())
	return connect.NewResponse(&tasksv1.ListTeamsResponse{
		Teams: []*tasksv1.TeamSummary{{
			TeamId:       testReadTeamID,
			WorkspaceId:  req.Msg.GetWorkspaceId(),
			SessionId:    req.Msg.GetSessionId(),
			State:        "active",
			TaskCount:    2,
			RunningCount: 1,
			AskingCount:  1,
			LastSeq:      7,
			CreatedAt:    ts,
			UpdatedAt:    ts,
			EndedAt:      ts,
		}},
	}), nil
}

func (f *tasksFake) GetTeam(_ context.Context, req *connect.Request[tasksv1.GetTeamRequest]) (*connect.Response[tasksv1.GetTeamResponse], error) {
	if req.Msg.GetTeamId() == "missing-team-read" {
		return nil, errTasksDetail("tasks.team_not_found", connect.CodeNotFound)
	}
	return connect.NewResponse(&tasksv1.GetTeamResponse{
		Team: &tasksv1.TeamView{
			Summary: &tasksv1.TeamSummary{
				TeamId:    req.Msg.GetTeamId(),
				State:     "active",
				TaskCount: 1,
			},
			Title: "Fake Team",
			Brief: "Brief",
			Budget: &tasksv1.TeamBudget{
				MaxAgents:          4,
				MaxDurationSeconds: 600,
			},
			PendingRequestId: "req-1",
			Tasks: []*tasksv1.TaskNode{{
				TaskId:         "t-1",
				HierarchyRole:  "member",
				CatalogRole:    "coder",
				Vendor:         "claude",
				Model:          "sonnet",
				State:          "unknown",
				Goal:           "fix bug",
				WriteScope:     []string{"pkg/**"},
				LastChannelSeq: 3,
				ElapsedMs:      1200,
			}},
			DiffText:      "diff --git a/x b/x\n",
			DiffTruncated: req.Msg.GetDiffTaskId() == "@team",
		},
	}), nil
}

func (f *tasksFake) RecoverTask(_ context.Context, req *connect.Request[tasksv1.RecoverTaskRequest]) (*connect.Response[tasksv1.RecoverTaskResponse], error) {
	if req.Msg.GetTaskId() == "not-unknown" {
		return nil, errTasksDetail("tasks.task_not_unknown", connect.CodeFailedPrecondition)
	}
	return connect.NewResponse(&tasksv1.RecoverTaskResponse{
		Task: &tasksv1.TaskNode{
			TaskId: req.Msg.GetTaskId(),
			State:  "running",
		},
	}), nil
}

func TestTasksTeamClientMethodsOverRealSocket(t *testing.T) {
	t.Parallel()
	c := serveTasksFake(t, &tasksFake{posted: map[string]int64{}})

	assertListTeamsClient(t, c)
	assertGetTeamClient(t, c)
	assertRecoverTaskClient(t, c)
}

func assertListTeamsClient(t *testing.T, c *client.Client) {
	t.Helper()
	list, err := c.ListTeams(t.Context(), &client.ListTeamsInput{WorkspaceID: "ws-1", SessionID: testReadSessionID})
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(list) != 1 || list[0].TeamID != testReadTeamID || list[0].RunningCount != 1 || list[0].AskingCount != 1 || list[0].EndedAt.IsZero() {
		t.Fatalf("ListTeams = %+v", list)
	}
	if _, listErr := c.ListTeams(t.Context(), &client.ListTeamsInput{WorkspaceID: "err-ws"}); listErr == nil {
		t.Fatal("expected error from ListTeams(err-ws)")
	}
}

func assertGetTeamClient(t *testing.T, c *client.Client) {
	t.Helper()
	view, err := c.GetTeam(t.Context(), &client.GetTeamInput{TeamID: testReadTeamID, DiffTaskID: "@team"})
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if view.Title != "Fake Team" || view.Budget == nil || view.Budget.MaxAgents != 4 || !view.DiffTruncated || len(view.Tasks) != 1 {
		t.Fatalf("GetTeam = %+v", view)
	}
	if _, getErr := c.GetTeam(t.Context(), &client.GetTeamInput{TeamID: "missing-team-read"}); getErr == nil {
		t.Fatal("expected error from GetTeam(missing-team-read)")
	}
}

func assertRecoverTaskClient(t *testing.T, c *client.Client) {
	t.Helper()
	node, err := c.RecoverTask(t.Context(), &client.RecoverTaskInput{
		CommandID: "rec-1", TeamID: testReadTeamID, TaskID: "t-1", Decision: "resume",
	})
	if err != nil || node.State != "running" {
		t.Fatalf("RecoverTask = (%+v, %v)", node, err)
	}
	_, codedErr := c.RecoverTask(t.Context(), &client.RecoverTaskInput{
		CommandID: "rec-2", TeamID: testReadTeamID, TaskID: "not-unknown", Decision: "cancel",
	})
	if code, ok := client.TasksCodeOf(codedErr); !ok || code != "tasks.task_not_unknown" {
		t.Fatalf("TasksCodeOf = (%q, %v), want tasks.task_not_unknown", code, ok)
	}
}
