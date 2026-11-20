package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

var errSkipEventAppend = errors.New("skip team event append")

func (b *teamBudgets) fireTeamDeadline(ctx context.Context, tm *vendorstore.TeamTimer) {
	var reachedRec vendorstore.TeamRecord
	var reachedTasks []vendorstore.TaskRecord
	err := b.appendTeamEventWithRetry(ctx, tm.TeamID, func(rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		if rec.State != vendorstore.TeamStateActive || rec.WaitingReason == vendorstore.TeamWaitingDeadline {
			return nil, errcode.New(errcode.FailedPrecondition, "team deadline is not applicable")
		}
		evID, idErr := b.deps.IDs.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate team deadline event ID", idErr)
		}
		payload, pErr := json.Marshal(teamDeadlineEventPayload{Generation: tm.Generation, DeadlineMs: tm.DeadlineMs})
		if pErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "marshal team deadline payload", pErr)
		}
		reachedRec, reachedTasks = *rec, tasks
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, EventType: vendorstore.EventTeamDeadlineReached, ProtectedPayload: payload,
			FireTimer: &vendorstore.TeamTimerRef{TeamID: tm.TeamID, Kind: vendorstore.TimerKindTeamDeadline, Generation: tm.Generation},
		}, nil
	})
	if err == nil {
		b.ensureTeamDeadlineCard(ctx, &reachedRec, reachedTasks, tm.Generation)
	}
}

func (b *teamBudgets) reconcileWaitingDeadlineCards(ctx context.Context) {
	if b.deps.Interactions == nil {
		return
	}
	teamIDs, err := b.deps.Store.ActiveTeamIDs(ctx)
	if err != nil {
		return
	}
	for _, teamID := range teamIDs {
		if rec, tasks, snapErr := b.deps.Store.TeamSnapshot(ctx, teamID); snapErr == nil && rec.State == vendorstore.TeamStateActive && rec.WaitingReason == vendorstore.TeamWaitingDeadline {
			if tm, ok, tmErr := b.deps.Store.GetTeamTimer(ctx, teamID, "", vendorstore.TimerKindTeamDeadline); tmErr == nil && ok {
				b.ensureTeamDeadlineCard(ctx, &rec, tasks, tm.Generation)
			}
		}
	}
}

func (b *teamBudgets) ensureTeamDeadlineCard(_ context.Context, rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord, gen int64) {
	if b.deps.Interactions == nil || gen < 1 {
		return
	}
	queued, running := 0, 0
	for i := range tasks {
		if tasks[i].HierarchyRole == vendorstore.RoleLead || tasks[i].HierarchyRole == vendorstore.RoleSolo {
			continue
		}
		if tasks[i].State == vendorstore.TaskStateQueued {
			queued++
			continue
		}
		if tasks[i].State == vendorstore.TaskStateStarting || tasks[i].State == vendorstore.TaskStateRunning || tasks[i].State == vendorstore.TaskStateAsking {
			running++
		}
	}
	uGen := uint64(gen)
	if opKey, err := interaction.NativeOperationKey(rec.TeamID, "team_deadline", uGen); err == nil {
		if _, ensureErr := b.deps.Interactions.Ensure(&interaction.Spec{
			OperationID: opKey, Kind: interaction.KindQuestion, Summary: "Team deadline reached",
			Body: fmt.Sprintf("team: %s\ngeneration: %d\nrunning tasks: %d (kept running)\nqueued tasks paused: %d", rec.TeamID, gen, running, queued),
			Questions: []interaction.Question{{
				ID: teamDeadlineQuestionID, Header: "team deadline",
				Question: "The team deadline has expired. New task dispatches are paused while running tasks continue. How should the team proceed?",
				Options: []interaction.Option{
					{ID: teamDeadlineOptionExtend, Label: "Extend deadline", Description: "Extend the team deadline and resume queued task dispatches"},
					{ID: teamDeadlineOptionCancel, Label: "Cancel queued tasks", Description: "Cancel remaining queued tasks while letting running tasks finish"},
				},
			}},
			Binding: interaction.Binding{
				SessionID: rec.SessionID, WorkspaceID: rec.WorkspaceID, InvocationID: rec.TeamID,
				AttemptID: "team_deadline", CheckGeneration: uGen, ToolName: teamDeadlineToolName,
			},
		}); ensureErr != nil {
			return
		}
	}
}

func signedGeneration(u uint64) int64 {
	if u > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(u)
}

func (b *teamBudgets) validateTeamDeadlineAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	option, text, err := decisionAnswerOf(teamDeadlineQuestionID, decision)
	if err != nil || option == "" {
		return err
	}
	if option != teamDeadlineOptionExtend && option != teamDeadlineOptionCancel {
		return errcode.New(errcode.InvalidArgument, "the team deadline decision option was not offered")
	}
	if option == teamDeadlineOptionExtend && strings.TrimSpace(text) != "" {
		if sec, parseErr := strconv.Atoi(strings.TrimSpace(text)); parseErr != nil || sec <= 0 || sec > teams.MaxTeamBudgetDurationSeconds {
			return errcode.New(errcode.InvalidArgument, "custom extension seconds must be a positive integer within the team budget ceiling")
		}
	}
	rec, _, err := b.deps.Store.TeamSnapshot(ctx, ticket.Binding.InvocationID)
	if err != nil {
		return boundaryError("read the team", err)
	}
	return b.verifyDeadlineGeneration(ctx, &rec, signedGeneration(ticket.Binding.CheckGeneration))
}

func (b *teamBudgets) applyTeamDeadlineAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	option, text, err := decisionAnswerOf(teamDeadlineQuestionID, decision)
	if err != nil || option == "" {
		return err
	}
	teamID, gen := ticket.Binding.InvocationID, signedGeneration(ticket.Binding.CheckGeneration)
	if option == teamDeadlineOptionExtend {
		return b.applyTeamDeadlineExtend(ctx, teamID, gen, text)
	}
	return b.applyTeamDeadlineCancelQueued(ctx, teamID, gen)
}

func (b *teamBudgets) applyTeamDeadlineExtend(ctx context.Context, teamID string, gen int64, text string) error {
	err := b.appendTeamEventWithRetry(ctx, teamID, func(rec *vendorstore.TeamRecord, _ []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		if err := b.verifyDeadlineGeneration(ctx, rec, gen); err != nil {
			return nil, err
		}
		_, extSec := resolveSealedTeamBudget(ctx, b.deps.Store, b.deps.TeamsConfig, rec)
		if parsed, pErr := strconv.Atoi(strings.TrimSpace(text)); pErr == nil && parsed > 0 {
			extSec = parsed
		}
		newDeadlineMs := b.now().Add(time.Duration(extSec) * time.Second).UnixMilli()
		evID, idErr := b.deps.IDs.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate team deadline extend event ID", idErr)
		}
		payload, pErr := json.Marshal(teamDeadlineEventPayload{Generation: gen, DeadlineMs: newDeadlineMs, ExtendSeconds: extSec, DecidedBy: decidedByUser})
		if pErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "marshal team deadline extend payload", pErr)
		}
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, EventType: vendorstore.EventTeamDeadlineExtended, ProtectedPayload: payload,
			ArmTimers: []vendorstore.TeamTimerArmInput{{TeamID: teamID, Kind: vendorstore.TimerKindTeamDeadline, DeadlineMs: newDeadlineMs}},
		}, nil
	})
	if err == nil {
		b.Notify()
		if b.deps.OnTeamResume != nil {
			b.deps.OnTeamResume(teamID)
		}
	}
	return err
}

func (b *teamBudgets) applyTeamDeadlineCancelQueued(ctx context.Context, teamID string, gen int64) error {
	rec, tasks, err := b.deps.Store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return boundaryError("read the team", err)
	}
	if err := b.verifyDeadlineGeneration(ctx, &rec, gen); err != nil {
		return err
	}
	for i := range tasks {
		if t := &tasks[i]; t.HierarchyRole != vendorstore.RoleLead && t.HierarchyRole != vendorstore.RoleSolo && t.State == vendorstore.TaskStateQueued {
			if err := b.cancelOneQueuedTask(ctx, teamID, t.TaskID, gen); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *teamBudgets) cancelOneQueuedTask(ctx context.Context, teamID, targetID string, gen int64) error {
	return b.appendTeamEventWithRetry(ctx, teamID, func(curRec *vendorstore.TeamRecord, curTasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		if err := b.verifyDeadlineGeneration(ctx, curRec, gen); err != nil {
			return nil, err
		}
		if cur, ok := findTask(curTasks, targetID); !ok || cur.State != vendorstore.TaskStateQueued {
			return nil, errSkipEventAppend
		}
		evID, idErr := b.deps.IDs.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate queued cancel event ID", idErr)
		}
		return &vendorstore.AppendTeamEventInput{EventID: evID, TaskID: targetID, EventType: vendorstore.EventTaskEnded, Outcome: string(vendorstore.TaskStateCancelled), FailureCode: teamDeadlineCancelCode}, nil
	})
}

func (b *teamBudgets) verifyDeadlineGeneration(ctx context.Context, rec *vendorstore.TeamRecord, gen int64) error {
	if rec.State != vendorstore.TeamStateActive || rec.WaitingReason != vendorstore.TeamWaitingDeadline {
		return errcode.New(errcode.FailedPrecondition, "the team is no longer waiting on a deadline decision")
	}
	tm, ok, err := b.deps.Store.GetTeamTimer(ctx, rec.TeamID, "", vendorstore.TimerKindTeamDeadline)
	if err != nil {
		return boundaryError("read the team deadline timer", err)
	}
	if !ok || tm.Generation != gen || tm.State != vendorstore.TimerStateFired {
		return errcode.New(errcode.FailedPrecondition, "the team deadline card belongs to a stale timer generation")
	}
	return nil
}

func (b *teamBudgets) appendTeamEventWithRetry(ctx context.Context, teamID string, build func(*vendorstore.TeamRecord, []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error)) error {
	for attempt := range maxAppendRetries {
		rec, tasks, err := b.deps.Store.TeamSnapshot(ctx, teamID)
		if err != nil {
			return boundaryError("read the team", err)
		}
		if rec.State != vendorstore.TeamStateActive {
			return errcode.New(errcode.FailedPrecondition, "the team is not active")
		}
		ev, err := build(&rec, tasks)
		if errors.Is(err, errSkipEventAppend) {
			return nil
		}
		if err != nil {
			return err
		}
		_, appErr := b.deps.Store.AppendTeamEvent(ctx, teamID, rec.LastSeq, ev)
		if appErr == nil {
			return nil
		}
		if !errors.Is(appErr, vendorstore.ErrSequenceConflict) || attempt+1 >= maxAppendRetries {
			return boundaryError("append team budget event", appErr)
		}
	}
	return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

func workspaceMaxTeamAgents(eff *workspace.EffectiveConfig) int {
	if eff != nil && eff.Teams.TeamMaxAgents > 0 {
		return eff.Teams.TeamMaxAgents
	}
	return defaultTeamMaxAgents
}

func workspaceTeamDeadlineSeconds(eff *workspace.EffectiveConfig) int {
	if eff != nil && eff.Teams.TeamDeadlineSeconds > 0 {
		return eff.Teams.TeamDeadlineSeconds
	}
	return defaultTeamDeadlineSeconds
}

func effectiveProposalAgentCap(eff *workspace.EffectiveConfig, budget *teams.TeamBudget) int {
	if budget != nil && budget.MaxAgents > 0 {
		return budget.MaxAgents
	}
	return workspaceMaxTeamAgents(eff)
}

func effectiveProposalTeamDeadlineSeconds(eff *workspace.EffectiveConfig, budget *teams.TeamBudget) int {
	if budget != nil && budget.MaxDurationSeconds > 0 {
		return int(budget.MaxDurationSeconds)
	}
	return workspaceTeamDeadlineSeconds(eff)
}

func countNonLeadTasks(tasks []vendorstore.TaskRecord) (n int) {
	for i := range tasks {
		if tasks[i].HierarchyRole != vendorstore.RoleLead && tasks[i].HierarchyRole != vendorstore.RoleSolo {
			n++
		}
	}
	return n
}

func countProposedNonLeadTasks(tasks []teams.ProposedTask) (n int) {
	for i := range tasks {
		if tasks[i].TeamRole != teams.RoleLead && tasks[i].TeamRole != teams.RoleSolo {
			n++
		}
	}
	return n
}

func resolveTeamsConfig(ctx context.Context, fn func(context.Context, string) (workspace.TeamsConfig, error), workspaceID string) workspace.TeamsConfig {
	if fn != nil {
		if cfg, err := fn(ctx, workspaceID); err == nil {
			return cfg
		}
	}
	return workspace.TeamsConfig{TaskDeadlineSeconds: defaultTaskDeadlineSeconds, TaskMaxTurns: defaultTaskMaxTurns, SilenceTimeoutSeconds: defaultSilenceTimeoutSeconds, TeamDeadlineSeconds: defaultTeamDeadlineSeconds, TeamMaxAgents: defaultTeamMaxAgents}
}

func resolveSilenceTimeoutSeconds(ctx context.Context, fn func(context.Context, string) (workspace.TeamsConfig, error), workspaceID string) int {
	if fn != nil {
		if cfg, err := fn(ctx, workspaceID); err == nil {
			return cfg.SilenceTimeoutSeconds
		}
	}
	return defaultSilenceTimeoutSeconds
}

func resolveTeamAgentCap(ctx context.Context, store *vendorstore.Store, fn func(context.Context, string) (workspace.TeamsConfig, error), rec *vendorstore.TeamRecord) int {
	maxAgents, _ := resolveSealedTeamBudget(ctx, store, fn, rec)
	return maxAgents
}

func resolveSealedTeamBudget(ctx context.Context, store *vendorstore.Store, fn func(context.Context, string) (workspace.TeamsConfig, error), rec *vendorstore.TeamRecord) (maxAgents, durationSec int) {
	cfg := resolveTeamsConfig(ctx, fn, rec.WorkspaceID)
	maxAgents, durationSec = cmp.Or(cfg.TeamMaxAgents, defaultTeamMaxAgents), cmp.Or(cfg.TeamDeadlineSeconds, defaultTeamDeadlineSeconds)
	if store == nil {
		return maxAgents, durationSec
	}
	events, err := store.TeamEventsSince(ctx, rec.TeamID, 0, 500)
	if err != nil {
		return maxAgents, durationSec
	}
	for i := range events {
		if ev := &events[i]; ev.EventType == vendorstore.EventTeamStarted || ev.EventType == vendorstore.EventTeamAmended {
			var holder struct {
				Budget *teams.TeamBudget `json:"budget"`
			}
			if raw, openErr := store.OpenTeamEventPayload(ctx, rec.TeamID, ev.Sequence); openErr == nil && json.Unmarshal(raw, &holder) == nil && holder.Budget != nil {
				maxAgents, durationSec = cmp.Or(holder.Budget.MaxAgents, maxAgents), cmp.Or(int(holder.Budget.MaxDurationSeconds), durationSec)
			}
		}
	}
	return maxAgents, durationSec
}

func readSealedTaskBudget(ctx context.Context, store *vendorstore.Store, teamID, taskID string) teams.TaskBudget {
	if store != nil {
		var spec teams.ProtectedTaskContent
		if raw, err := store.OpenTaskSpec(ctx, teamID, taskID); err == nil && json.Unmarshal(raw, &spec) == nil && spec.Budget != nil {
			return *spec.Budget
		}
	}
	return teams.TaskBudget{}
}

func resolveTaskDeadlineSeconds(ctx context.Context, store *vendorstore.Store, fn func(context.Context, string) (workspace.TeamsConfig, error), rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord) int {
	return cmp.Or(int(readSealedTaskBudget(ctx, store, rec.TeamID, task.TaskID).MaxDurationSeconds), resolveTeamsConfig(ctx, fn, rec.WorkspaceID).TaskDeadlineSeconds, defaultTaskDeadlineSeconds)
}

func resolveTaskMaxTurns(ctx context.Context, store *vendorstore.Store, fn func(context.Context, string) (workspace.TeamsConfig, error), rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord) int {
	return cmp.Or(readSealedTaskBudget(ctx, store, rec.TeamID, task.TaskID).MaxTurns, resolveTeamsConfig(ctx, fn, rec.WorkspaceID).TaskMaxTurns, defaultTaskMaxTurns)
}
