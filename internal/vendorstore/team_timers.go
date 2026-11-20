package vendorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TeamTimerKind identifies the purpose of a durable team_timers row.
type TeamTimerKind string

// TeamTimerState enumerates the lifecycle states of a team_timers row.
type TeamTimerState string

// Supported durable timer kinds and lifecycle states.
const (
	TimerKindTaskDeadline TeamTimerKind  = "task_deadline"
	TimerKindTeamDeadline TeamTimerKind  = "team_deadline"
	TimerStateArmed       TeamTimerState = "armed"
	TimerStateFired       TeamTimerState = "fired"
	TimerStateCancelled   TeamTimerState = "cancelled"
)

// TeamTimer is the metadata projection of a durable team_timers row.
type TeamTimer struct {
	TeamID, TaskID         string
	Kind                   TeamTimerKind
	DeadlineMs, Generation int64
	State                  TeamTimerState
	CreatedAt, UpdatedAt   int64
}

// TeamTimerRef identifies a durable timer row and optional expected generation.
type TeamTimerRef struct {
	TeamID, TaskID string
	Kind           TeamTimerKind
	Generation     int64
}

// TeamTimerArmInput describes a durable timer to arm idempotently.
type TeamTimerArmInput struct {
	TeamID, TaskID string
	Kind           TeamTimerKind
	DeadlineMs     int64
}

// ArmTeamTimer idempotently inserts or updates a durable timer keyed by (team_id, task_id, kind).
func (s *Store) ArmTeamTimer(ctx context.Context, in *TeamTimerArmInput) error {
	if in == nil || in.TeamID == "" || !isValidTimerKind(in.Kind) {
		return fmt.Errorf("%w: team_id and valid timer kind are required", ErrTeamInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin arm timer tx: %w", ErrUnavailable, err)
	}
	defer rollback(ctx, tx)
	if err := ensureTimerTargetNonTerminalTx(ctx, tx, in); err != nil {
		return err
	}
	if err := armTeamTimerTx(ctx, tx, in.TeamID, *in, s.clock.Now().UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit arm timer: %w", ErrUnavailable, err)
	}
	return nil
}

func ensureTimerTargetNonTerminalTx(ctx context.Context, tx *sql.Tx, in *TeamTimerArmInput) error {
	var teamState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM team_records WHERE team_id = ?`, in.TeamID).Scan(&teamState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrTeamNotFound, in.TeamID)
		}
		return fmt.Errorf("%w: query timer team: %w", ErrUnavailable, err)
	}
	if isTeamTerminal(TeamState(teamState)) {
		return fmt.Errorf("%w: cannot arm timer on terminal team %s", ErrTeamTerminal, in.TeamID)
	}
	if in.TaskID == "" {
		return nil
	}
	var taskState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM team_tasks WHERE team_id = ? AND task_id = ?`, in.TeamID, in.TaskID).Scan(&taskState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrTaskNotFound, in.TaskID)
		}
		return fmt.Errorf("%w: query timer task: %w", ErrUnavailable, err)
	}
	if isTaskTerminal(TaskState(taskState)) {
		return fmt.Errorf("%w: cannot arm timer on terminal task %s", ErrTeamTransition, in.TaskID)
	}
	return nil
}

// CancelTeamTimer idempotently cancels an armed durable timer.
func (s *Store) CancelTeamTimer(ctx context.Context, ref TeamTimerRef) error {
	if ref.TeamID == "" || !isValidTimerKind(ref.Kind) {
		return fmt.Errorf("%w: team_id and valid timer kind are required", ErrTeamInvalidInput)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE team_timers SET state = ?, updated_at = ? WHERE team_id = ? AND task_id = ? AND kind = ? AND state = ?`,
		string(TimerStateCancelled), s.clock.Now().UnixMilli(), ref.TeamID, ref.TaskID, string(ref.Kind), string(TimerStateArmed)); err != nil {
		return fmt.Errorf("%w: cancel team timer: %w", ErrUnavailable, err)
	}
	return nil
}

// FireTeamTimer idempotently transitions an armed timer to fired.
func (s *Store) FireTeamTimer(ctx context.Context, ref TeamTimerRef) (bool, error) {
	if ref.TeamID == "" || !isValidTimerKind(ref.Kind) {
		return false, fmt.Errorf("%w: team_id and valid timer kind are required", ErrTeamInvalidInput)
	}
	res, err := execFireTimer(ctx, s.db, &ref, s.clock.Now().UnixMilli())
	if err != nil {
		return false, fmt.Errorf("%w: fire team timer: %w", ErrUnavailable, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%w: fire team timer rows affected: %w", ErrUnavailable, err)
	}
	return n > 0, nil
}

// ListArmedTeamTimers returns all currently armed team timers ordered by deadline.
func (s *Store) ListArmedTeamTimers(ctx context.Context) ([]TeamTimer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT team_id, task_id, kind, deadline_ms, generation, state, created_at, updated_at FROM team_timers WHERE state = ? ORDER BY deadline_ms ASC, team_id ASC, task_id ASC, kind ASC`, string(TimerStateArmed))
	if err != nil {
		return nil, fmt.Errorf("%w: list armed team timers: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	var out []TeamTimer
	for rows.Next() {
		var t TeamTimer
		var k, st string
		if err := rows.Scan(&t.TeamID, &t.TaskID, &k, &t.DeadlineMs, &t.Generation, &st, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("%w: scan team timer: %w", ErrUnavailable, err)
		}
		t.Kind, t.State = TeamTimerKind(k), TeamTimerState(st)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate armed team timers: %w", ErrUnavailable, err)
	}
	return out, nil
}

// GetTeamTimer reads a single durable timer row by its composite key.
func (s *Store) GetTeamTimer(ctx context.Context, teamID, taskID string, kind TeamTimerKind) (TeamTimer, bool, error) {
	var t TeamTimer
	var k, st string
	err := s.db.QueryRowContext(ctx, `SELECT team_id, task_id, kind, deadline_ms, generation, state, created_at, updated_at FROM team_timers WHERE team_id = ? AND task_id = ? AND kind = ?`,
		teamID, taskID, string(kind)).Scan(&t.TeamID, &t.TaskID, &k, &t.DeadlineMs, &t.Generation, &st, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamTimer{}, false, nil
	}
	if err != nil {
		return TeamTimer{}, false, fmt.Errorf("%w: query team timer: %w", ErrUnavailable, err)
	}
	t.Kind, t.State = TeamTimerKind(k), TeamTimerState(st)
	return t, true, nil
}

func isValidTimerKind(kind TeamTimerKind) bool {
	return kind == TimerKindTaskDeadline || kind == TimerKindTeamDeadline
}

func armTeamTimersTx(ctx context.Context, tx *sql.Tx, teamID string, timers []TeamTimerArmInput, now int64) error {
	for i := range timers {
		if err := armTeamTimerTx(ctx, tx, teamID, timers[i], now); err != nil {
			return err
		}
	}
	return nil
}

func armTeamTimerTx(ctx context.Context, tx *sql.Tx, teamID string, in TeamTimerArmInput, now int64) error {
	if !isValidTimerKind(in.Kind) || (in.TeamID != "" && in.TeamID != teamID) {
		return fmt.Errorf("%w: invalid timer input (%q, %q)", ErrTeamInvalidInput, in.TeamID, in.Kind)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_timers (team_id, task_id, kind, deadline_ms, generation, state, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?, ?) ON CONFLICT(team_id, task_id, kind) DO UPDATE SET deadline_ms = excluded.deadline_ms, generation = CASE WHEN team_timers.deadline_ms = excluded.deadline_ms THEN team_timers.generation ELSE team_timers.generation + 1 END, state = CASE WHEN team_timers.deadline_ms = excluded.deadline_ms THEN team_timers.state ELSE 'armed' END, updated_at = excluded.updated_at WHERE team_timers.deadline_ms != excluded.deadline_ms`,
		teamID, in.TaskID, string(in.Kind), in.DeadlineMs, string(TimerStateArmed), now, now); err != nil {
		return fmt.Errorf("arm team timer: %w", err)
	}
	return nil
}

func fireTeamTimerTx(ctx context.Context, tx *sql.Tx, ref *TeamTimerRef, now int64) error {
	if ref == nil {
		return nil
	}
	if ref.TeamID == "" || !isValidTimerKind(ref.Kind) {
		return fmt.Errorf("%w: fire timer requires team_id and valid kind", ErrTeamInvalidInput)
	}
	res, err := execFireTimer(ctx, tx, ref, now)
	if err != nil {
		return fmt.Errorf("fire team timer: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("fire team timer rows affected: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("%w: (%s, %s, %s)", ErrTimerNotArmed, ref.TeamID, ref.TaskID, ref.Kind)
	}
	return nil
}

type timerExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func execFireTimer(ctx context.Context, execer timerExecer, ref *TeamTimerRef, now int64) (sql.Result, error) {
	if ref.Generation > 0 {
		res, err := execer.ExecContext(ctx, `UPDATE team_timers SET state = ?, updated_at = ? WHERE team_id = ? AND task_id = ? AND kind = ? AND generation = ? AND state = ?`,
			string(TimerStateFired), now, ref.TeamID, ref.TaskID, string(ref.Kind), ref.Generation, string(TimerStateArmed))
		if err != nil {
			return nil, fmt.Errorf("exec fire timer: %w", err)
		}
		return res, nil
	}
	res, err := execer.ExecContext(ctx, `UPDATE team_timers SET state = ?, updated_at = ? WHERE team_id = ? AND task_id = ? AND kind = ? AND state = ?`,
		string(TimerStateFired), now, ref.TeamID, ref.TaskID, string(ref.Kind), string(TimerStateArmed))
	if err != nil {
		return nil, fmt.Errorf("exec fire timer: %w", err)
	}
	return res, nil
}

func cancelTaskTimersTx(ctx context.Context, tx *sql.Tx, teamID, taskID string, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE team_timers SET state = ?, updated_at = ? WHERE team_id = ? AND task_id = ? AND state = ?`,
		string(TimerStateCancelled), now, teamID, taskID, string(TimerStateArmed))
	if err != nil {
		return fmt.Errorf("cancel task timers: %w", err)
	}
	return nil
}

func cancelTeamTimersTx(ctx context.Context, tx *sql.Tx, teamID string, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE team_timers SET state = ?, updated_at = ? WHERE team_id = ? AND state = ?`,
		string(TimerStateCancelled), now, teamID, string(TimerStateArmed))
	if err != nil {
		return fmt.Errorf("cancel team timers: %w", err)
	}
	return nil
}

func cancelTimersOnTerminalEvent(ctx context.Context, tx *sql.Tx, teamID, taskID string, evType TeamEventType, toState string, now int64) error {
	if isTeamEventType(evType) && isTeamTerminal(TeamState(toState)) {
		return cancelTeamTimersTx(ctx, tx, teamID, now)
	}
	if !isTeamEventType(evType) && isTaskTerminal(TaskState(toState)) {
		return cancelTaskTimersTx(ctx, tx, teamID, taskID, now)
	}
	return nil
}

func updateTaskAndTeamSeq(ctx context.Context, tx *sql.Tx, teamID, taskID string, state TaskState, seq int64, failureCode string, now int64) error {
	var endedAt any
	if isTaskTerminal(state) {
		endedAt = now
	}
	if _, err := tx.ExecContext(ctx, `UPDATE team_tasks SET state = ?, last_event_seq = ?, failure_code = CASE WHEN ? != '' THEN ? ELSE failure_code END, updated_at = ?, ended_at = COALESCE(?, ended_at) WHERE team_id = ? AND task_id = ?`,
		string(state), seq, failureCode, failureCode, now, endedAt, teamID, taskID); err != nil {
		return fmt.Errorf("update task projection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE team_records SET last_seq = ?, updated_at = ? WHERE team_id = ?`, seq, now, teamID); err != nil {
		return fmt.Errorf("advance team last_seq: %w", err)
	}
	return nil
}
