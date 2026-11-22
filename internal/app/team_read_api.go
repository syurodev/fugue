package app

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	// maxTeamDiffBytes bounds GetTeam.diff_text (host-api v0.19 §5.3).
	maxTeamDiffBytes = 256 << 10
	// teamDiffTargetAll selects the whole team branch final-review diff.
	teamDiffTargetAll = "@team"
)

// TeamSummaryView is the metadata-only summary of one team (§5.3, INV-09).
type TeamSummaryView struct {
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
	CreatedAt     int64
	UpdatedAt     int64
	EndedAt       int64
}

// TeamBudgetView carries optional team-wide §11 limits.
type TeamBudgetView struct {
	MaxAgents          uint32
	MaxDurationSeconds int64
}

// TaskNodeView is one node in a team's task tree (§5.3). Sealed fields
// (Goal, WriteScope) stay empty while the vault is locked.
type TaskNodeView struct {
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
	Summary          TeamSummaryView
	VaultLocked      bool
	Title            string
	Brief            string
	Budget           *TeamBudgetView
	PendingRequestID string
	Tasks            []TaskNodeView
	DiffText         string
	DiffTruncated    bool
}

// SetTeamServices wires the team task recovery and interaction services for
// GetTeam and RecoverTask.
func (s *TasksService) SetTeamServices(teamTasks *teamTaskService, interactions *InteractionService) {
	s.teamTasks = teamTasks
	s.interactions = interactions
}

// ListTeams returns metadata-only team summaries, optionally filtered by
// workspaceID and/or sessionID (§5.3). It succeeds while the vault is locked.
func (s *TasksService) ListTeams(ctx context.Context, workspaceID, sessionID string) ([]TeamSummaryView, error) {
	rows, err := s.store.ListTeams(ctx, workspaceID, sessionID)
	if err != nil {
		return nil, boundaryError("list teams", err)
	}
	out := make([]TeamSummaryView, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		out = append(out, TeamSummaryView{
			TeamID:        r.TeamID,
			WorkspaceID:   r.WorkspaceID,
			SessionID:     r.SessionID,
			State:         string(r.State),
			WaitingReason: r.WaitingReason,
			TaskCount:     clampUint32(r.TaskCount),
			RunningCount:  clampUint32(r.RunningCount),
			AskingCount:   clampUint32(r.AskingCount),
			FailedCount:   clampUint32(r.FailedCount),
			LastSeq:       r.LastSeq,
			CreatedAt:     r.CreatedAt,
			UpdatedAt:     r.UpdatedAt,
			EndedAt:       r.EndedAt,
		})
	}
	return out, nil
}

// GetTeam returns the read model for one team (§5.3). When the vault is
// locked, it succeeds with VaultLocked=true and metadata-only TaskNodes,
// omitting Title, Brief, Budget, Goal, WriteScope, and DiffText (INV-09).
func (s *TasksService) GetTeam(ctx context.Context, teamID, diffTaskID string) (TeamView, error) {
	if teamID == "" {
		return TeamView{}, errcode.New(errcode.TasksTeamNotFound, "team_id is required")
	}
	rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return TeamView{}, s.teamReadError(err)
	}
	lastSeqs, err := s.store.TeamTaskLastChannelSeqs(ctx, teamID)
	if err != nil {
		return TeamView{}, boundaryError("read task channel sequences", err)
	}
	view := TeamView{
		Summary:          summarizeTeam(&rec, tasks),
		VaultLocked:      s.vault == nil || s.vault.Snapshot().State != vault.StateUnlocked,
		PendingRequestID: s.pendingTeamRequestID(teamID),
		Tasks:            make([]TaskNodeView, 0, len(tasks)),
	}
	roleProfiles := s.populateTeamViewContent(ctx, &view, &rec, tasks, lastSeqs)
	if diffTaskID != "" {
		if err := validateDiffTarget(tasks, diffTaskID); err != nil {
			return TeamView{}, err
		}
		s.populateTeamDiff(&view, &rec, tasks, diffTaskID, ctx)
	}
	_ = roleProfiles
	return view, nil
}

func (s *TasksService) populateTeamViewContent(
	ctx context.Context, view *TeamView, rec *vendorstore.TeamRecord,
	tasks []vendorstore.TaskRecord, lastSeqs map[string]int64,
) map[string]teams.ProposedRole {
	var roleProfiles map[string]teams.ProposedRole
	if !view.VaultLocked {
		var lockedNow bool
		roleProfiles, lockedNow = s.loadProposalContent(ctx, view, rec.TeamID)
		if lockedNow {
			view.VaultLocked = true
		}
	}
	for i := range tasks {
		node, lockedNow := s.buildTaskNode(ctx, rec.TeamID, &tasks[i], lastSeqs, roleProfiles, view.VaultLocked)
		if lockedNow && !view.VaultLocked {
			view.VaultLocked = true
			clearSealedTeamView(view)
		}
		view.Tasks = append(view.Tasks, node)
	}
	return roleProfiles
}

func clearSealedTeamView(view *TeamView) {
	view.Title = ""
	view.Brief = ""
	view.Budget = nil
	view.DiffText = ""
	view.DiffTruncated = false
	for i := range view.Tasks {
		view.Tasks[i].Goal = ""
		view.Tasks[i].WriteScope = nil
	}
}

func (s *TasksService) loadProposalContent(ctx context.Context, view *TeamView, teamID string) (map[string]teams.ProposedRole, bool) {
	raw, found, err := s.store.OpenTeamProposedPayload(ctx, teamID)
	if err != nil {
		if errors.Is(err, vault.ErrLocked) {
			return nil, true
		}
		return nil, false
	}
	if !found {
		return nil, false
	}
	var prop teams.ProposalInput
	if json.Unmarshal(raw, &prop) != nil {
		return nil, false
	}
	view.Title = prop.Title
	view.Brief = prop.Brief
	if prop.Budget != nil {
		view.Budget = &TeamBudgetView{
			MaxAgents:          clampUint32(prop.Budget.MaxAgents),
			MaxDurationSeconds: prop.Budget.MaxDurationSeconds,
		}
	}
	roles := make(map[string]teams.ProposedRole, len(prop.Roles))
	for i := range prop.Roles {
		roles[prop.Roles[i].Name] = prop.Roles[i]
	}
	return roles, false
}

func (s *TasksService) buildTaskNode(
	ctx context.Context, teamID string, t *vendorstore.TaskRecord,
	lastSeqs map[string]int64, roleProfiles map[string]teams.ProposedRole, vaultLocked bool,
) (TaskNodeView, bool) {
	node := TaskNodeView{
		TaskID:         t.TaskID,
		ParentTaskID:   t.ParentTaskID,
		RetryOf:        t.RetryOf,
		Attempt:        clampUint32(t.Attempt),
		HierarchyRole:  string(t.HierarchyRole),
		CatalogRole:    t.CatalogRole,
		State:          string(t.State),
		FailureCode:    t.FailureCode,
		SessionID:      t.SessionID,
		DependsOn:      slices.Clone(t.DependsOn),
		LastChannelSeq: max(lastSeqs[t.TaskID], lastSeqs[vendorstore.ThreadRootTaskID(t.TaskID)]),
		ElapsedMs:      s.taskElapsedMs(t),
	}
	if t.SessionID != "" {
		if sess, err := s.store.Snapshot(ctx, t.SessionID); err == nil {
			node.Vendor = sess.Vendor
			node.Model = sess.Model
			node.AccountID = sess.AccountID
		}
	}
	if vaultLocked {
		return node, false
	}
	raw, err := s.store.OpenTaskSpec(ctx, teamID, t.TaskID)
	if err != nil {
		return node, errors.Is(err, vault.ErrLocked)
	}
	var spec teams.ProtectedTaskContent
	if json.Unmarshal(raw, &spec) == nil {
		node.Goal = spec.Goal
		node.WriteScope = slices.Clone(spec.WriteScope)
		if node.Vendor == "" && spec.Profile != nil {
			node.Vendor = string(spec.Profile.Vendor)
			node.Model = spec.Profile.Model
		}
	}
	if node.Vendor == "" && roleProfiles != nil {
		if rp, ok := roleProfiles[t.CatalogRole]; ok {
			node.Vendor = string(rp.Vendor)
			node.Model = rp.Model
		}
	}
	return node, false
}

func (s *TasksService) taskElapsedMs(t *vendorstore.TaskRecord) int64 {
	if t.EndedAt > 0 && t.CreatedAt > 0 {
		return max(0, t.EndedAt-t.CreatedAt)
	}
	if t.State != vendorstore.TaskStateQueued && t.CreatedAt > 0 && s.clk != nil {
		return max(0, s.clk.Now().UnixMilli()-t.CreatedAt)
	}
	return 0
}

func (s *TasksService) pendingTeamRequestID(teamID string) string {
	if s.interactions == nil || s.interactions.owner == nil {
		return ""
	}
	for _, ticket := range s.interactions.owner.Pending() {
		if ticket.Binding.InvocationID == teamID {
			return ticket.RequestID
		}
	}
	return ""
}

func validateDiffTarget(tasks []vendorstore.TaskRecord, diffTaskID string) error {
	if diffTaskID == teamDiffTargetAll {
		return nil
	}
	if _, ok := findTask(tasks, diffTaskID); !ok {
		return errcode.New(errcode.TasksTaskNotFound, "the team task does not exist in this team")
	}
	if err := gitops.ValidateID("diff_task", gitTaskID(diffTaskID)); err != nil {
		return errcode.New(errcode.InvalidArgument, "diff_task_id is invalid")
	}
	return nil
}

func (s *TasksService) populateTeamDiff(
	view *TeamView, rec *vendorstore.TeamRecord,
	tasks []vendorstore.TaskRecord, diffTaskID string, ctx context.Context,
) {
	if view.VaultLocked || s.git == nil {
		return
	}
	repoDir, _ := resolveTeamRepo(ctx, s.workspaces, rec)
	if repoDir == "" {
		return
	}
	baseRef, headRef, ok := s.resolveDiffRefs(ctx, rec, tasks, repoDir, diffTaskID)
	if !ok {
		return
	}
	res, diffErr := s.git.DiffBetween(ctx, repoDir, baseRef, headRef, maxTeamDiffBytes)
	if diffErr != nil {
		return
	}
	view.DiffText = res.Text
	view.DiffTruncated = res.HasMore || res.Truncated
}

func (s *TasksService) resolveDiffRefs(
	ctx context.Context, rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord,
	repoDir, diffTaskID string,
) (string, string, bool) {
	if diffTaskID == teamDiffTargetAll {
		targetBranch, err := s.git.CurrentBranch(ctx, repoDir)
		teamBranch, tbErr := gitops.TeamBranchName(rec.TeamID)
		if err != nil || tbErr != nil || targetBranch == "" {
			return "", "", false
		}
		return targetBranch, teamBranch, true
	}
	task, _ := findTask(tasks, diffTaskID)
	baseRef, err := parentBranch(task, tasks)
	headRef, hbErr := gitops.TaskBranchName(gitTaskID(diffTaskID))
	if err != nil || hbErr != nil {
		return "", "", false
	}
	return baseRef, headRef, true
}

func summarizeTeam(rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) TeamSummaryView {
	var running, asking, failed int
	for i := range tasks {
		switch tasks[i].State {
		case vendorstore.TaskStateStarting, vendorstore.TaskStateRunning,
			vendorstore.TaskStateChecking, vendorstore.TaskStateReview, vendorstore.TaskStateIntegrating:
			running++
		case vendorstore.TaskStateAsking:
			asking++
		case vendorstore.TaskStateFailed:
			failed++
		case vendorstore.TaskStateQueued, vendorstore.TaskStateDone,
			vendorstore.TaskStateStopped, vendorstore.TaskStateCancelled, vendorstore.TaskStateUnknown:
		}
	}
	return TeamSummaryView{
		TeamID:        rec.TeamID,
		WorkspaceID:   rec.WorkspaceID,
		SessionID:     rec.SessionID,
		State:         string(rec.State),
		WaitingReason: rec.WaitingReason,
		TaskCount:     clampUint32(rec.TaskCount),
		RunningCount:  clampUint32(running),
		AskingCount:   clampUint32(asking),
		FailedCount:   clampUint32(failed),
		LastSeq:       rec.LastSeq,
		CreatedAt:     rec.CreatedAt,
		UpdatedAt:     rec.UpdatedAt,
		EndedAt:       rec.EndedAt,
	}
}

func clampUint32(n int) uint32 {
	if n <= 0 {
		return 0
	}
	if n > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n)
}
