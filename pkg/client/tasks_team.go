package client

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	tasksv1 "github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1/tasksv1connect"
)

// TeamSummary is the metadata-only summary of one team (host-api v0.19 §5.3,
// INV-09). It is returned even while the vault is locked.
type TeamSummary struct {
	TeamID        string
	WorkspaceID   string
	SessionID     string
	State         string
	WaitingReason string
	TaskCount     uint32
	RunningCount  uint32
	AskingCount   uint32
	FailedCount   uint32
	LastSeq       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	EndedAt       time.Time
}

// TeamBudget carries optional team-wide §11 limits.
type TeamBudget struct {
	MaxAgents          uint32
	MaxDurationSeconds int64
}

// TaskNode is one node in a team's task tree (§5.3). When the team view is
// returned with VaultLocked=true, Goal and WriteScope are empty.
type TaskNode struct {
	TaskID         string
	ParentTaskID   string
	RetryOf        string
	Attempt        uint32
	HierarchyRole  string
	CatalogRole    string
	Vendor         string
	Model          string
	AccountID      string
	State          string
	FailureCode    string
	SessionID      string
	Goal           string
	WriteScope     []string
	DependsOn      []string
	LastChannelSeq int64
	ElapsedMs      int64
}

// TeamView is the full read model of one team (§5.3).
type TeamView struct {
	Summary          TeamSummary
	VaultLocked      bool
	Title            string
	Brief            string
	Budget           *TeamBudget
	PendingRequestID string
	Tasks            []TaskNode
	DiffText         string
	DiffTruncated    bool
}

// ListTeamsInput carries optional filters for ListTeams.
type ListTeamsInput struct {
	WorkspaceID string
	SessionID   string
}

// GetTeamInput carries one GetTeam request. DiffTaskID may be a task ID or
// "@team" for the team branch final-review diff.
type GetTeamInput struct {
	TeamID     string
	DiffTaskID string
}

// RecoverTaskInput carries one RecoverTask request for a task in state
// unknown (decision: resume, keep_diff, or cancel).
type RecoverTaskInput struct {
	CommandID string
	TeamID    string
	TaskID    string
	Decision  string
	Reason    string
}

// ListTeams returns metadata-only team summaries (§5.3). Succeeds while the
// vault is locked.
func (c *Client) ListTeams(ctx context.Context, in *ListTeamsInput) ([]TeamSummary, error) {
	req := &tasksv1.ListTeamsRequest{}
	if in != nil {
		req.WorkspaceId = in.WorkspaceID
		req.SessionId = in.SessionID
	}
	res, err := tasksv1connect.NewTaskServiceClient(c.hc, c.base).ListTeams(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	out := make([]TeamSummary, 0, len(res.Msg.GetTeams()))
	for _, t := range res.Msg.GetTeams() {
		out = append(out, teamSummaryFromProto(t))
	}
	return out, nil
}

// GetTeam returns the read model for one team (§5.3).
func (c *Client) GetTeam(ctx context.Context, in *GetTeamInput) (TeamView, error) {
	req := &tasksv1.GetTeamRequest{}
	if in != nil {
		req.TeamId = in.TeamID
		req.DiffTaskId = in.DiffTaskID
	}
	res, err := tasksv1connect.NewTaskServiceClient(c.hc, c.base).GetTeam(ctx, connect.NewRequest(req))
	if err != nil {
		return TeamView{}, fmt.Errorf("get team: %w", err)
	}
	return teamViewFromProto(res.Msg.GetTeam()), nil
}

// RecoverTask applies a local user's recovery decision (resume, keep_diff, or
// cancel) to an unknown task with command_id deduplication (§5.3).
func (c *Client) RecoverTask(ctx context.Context, in *RecoverTaskInput) (TaskNode, error) {
	req := &tasksv1.RecoverTaskRequest{}
	if in != nil {
		req.CommandId = in.CommandID
		req.TeamId = in.TeamID
		req.TaskId = in.TaskID
		req.Decision = in.Decision
		req.Reason = in.Reason
	}
	res, err := tasksv1connect.NewTaskServiceClient(c.hc, c.base).RecoverTask(ctx, connect.NewRequest(req))
	if err != nil {
		return TaskNode{}, fmt.Errorf("recover task: %w", err)
	}
	return taskNodeFromProto(res.Msg.GetTask()), nil
}

func teamSummaryFromProto(s *tasksv1.TeamSummary) TeamSummary {
	if s == nil {
		return TeamSummary{}
	}
	out := TeamSummary{
		TeamID:        s.GetTeamId(),
		WorkspaceID:   s.GetWorkspaceId(),
		SessionID:     s.GetSessionId(),
		State:         s.GetState(),
		WaitingReason: s.GetWaitingReason(),
		TaskCount:     s.GetTaskCount(),
		RunningCount:  s.GetRunningCount(),
		AskingCount:   s.GetAskingCount(),
		FailedCount:   s.GetFailedCount(),
		LastSeq:       s.GetLastSeq(),
	}
	if s.GetCreatedAt() != nil {
		out.CreatedAt = s.GetCreatedAt().AsTime()
	}
	if s.GetUpdatedAt() != nil {
		out.UpdatedAt = s.GetUpdatedAt().AsTime()
	}
	if s.GetEndedAt() != nil {
		out.EndedAt = s.GetEndedAt().AsTime()
	}
	return out
}

func teamViewFromProto(v *tasksv1.TeamView) TeamView {
	if v == nil {
		return TeamView{}
	}
	out := TeamView{
		Summary:          teamSummaryFromProto(v.GetSummary()),
		VaultLocked:      v.GetVaultLocked(),
		Title:            v.GetTitle(),
		Brief:            v.GetBrief(),
		PendingRequestID: v.GetPendingRequestId(),
		Tasks:            make([]TaskNode, 0, len(v.GetTasks())),
		DiffText:         v.GetDiffText(),
		DiffTruncated:    v.GetDiffTruncated(),
	}
	if b := v.GetBudget(); b != nil {
		out.Budget = &TeamBudget{
			MaxAgents:          b.GetMaxAgents(),
			MaxDurationSeconds: b.GetMaxDurationSeconds(),
		}
	}
	for _, t := range v.GetTasks() {
		out.Tasks = append(out.Tasks, taskNodeFromProto(t))
	}
	return out
}

func taskNodeFromProto(t *tasksv1.TaskNode) TaskNode {
	if t == nil {
		return TaskNode{}
	}
	return TaskNode{
		TaskID:         t.GetTaskId(),
		ParentTaskID:   t.GetParentTaskId(),
		RetryOf:        t.GetRetryOf(),
		Attempt:        t.GetAttempt(),
		HierarchyRole:  t.GetHierarchyRole(),
		CatalogRole:    t.GetCatalogRole(),
		Vendor:         t.GetVendor(),
		Model:          t.GetModel(),
		AccountID:      t.GetAccountId(),
		State:          t.GetState(),
		FailureCode:    t.GetFailureCode(),
		SessionID:      t.GetSessionId(),
		Goal:           t.GetGoal(),
		WriteScope:     append([]string(nil), t.GetWriteScope()...),
		DependsOn:      append([]string(nil), t.GetDependsOn()...),
		LastChannelSeq: t.GetLastChannelSeq(),
		ElapsedMs:      t.GetElapsedMs(),
	}
}
