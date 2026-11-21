package vendorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/vault"
)

const (
	teamKeyRecordType     = "team.key"
	teamSpecRecordType    = "team.task.spec"
	teamEventRecordType   = "team.event.payload"
	teamDefaultKeyVersion = "v1"
)

// AppendTeamEvent optimistically appends one validated team or task event and
// updates the team/task projection within the same transaction.
func (s *Store) AppendTeamEvent(ctx context.Context, teamID string, expectedSeq int64, ev *AppendTeamEventInput) (int64, error) {
	if ev == nil || teamID == "" || ev.EventID == "" || expectedSeq < 1 {
		return 0, fmt.Errorf("%w: team_id, event_id, and positive expected_seq are required", ErrTeamInvalidInput)
	}
	if !IsValidTeamEventType(ev.EventType) {
		return 0, fmt.Errorf("%w: unknown team event type %q", ErrTeamInvalidInput, ev.EventType)
	}
	snap, needVault, err := s.appendEventGate(ev)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin append team event tx: %w", err)
	}
	defer rollback(ctx, tx)

	meta, err := loadTeamMetaForUpdate(ctx, tx, teamID, expectedSeq)
	if err != nil {
		return 0, err
	}
	nextSeq := expectedSeq + 1
	now := s.clock.Now().UnixMilli()
	if err := s.writeAppendedEvent(ctx, tx, teamID, nextSeq, ev, meta, now); err != nil {
		return 0, err
	}
	if err := s.commitTeamTx(snap, needVault, tx.Commit); err != nil {
		return 0, err
	}
	return nextSeq, nil
}

// writeAppendedEvent projects, seals and writes one appended event and its
// mailbox rows and timer transitions inside the open transaction (Q-122).
func (s *Store) writeAppendedEvent(ctx context.Context, tx *sql.Tx, teamID string, seq int64, ev *AppendTeamEventInput, meta teamMetaRow, now int64) error {
	fromState, toState, err := applyTeamOrTaskProjection(ctx, tx, teamID, meta.State, seq, now, ev)
	if err != nil {
		return err
	}
	envelope, err := s.sealEventPayloadIfPresent(teamID, meta.KeyID, meta.WrappedKey, ev)
	if err != nil {
		return err
	}
	if err := insertAppendedTeamEvent(ctx, tx, teamID, seq, ev, fromState, toState, envelope, now); err != nil {
		return err
	}
	if err := appendChannelForEvent(ctx, tx, teamID, seq, ev, now); err != nil {
		return err
	}
	if err := s.appendMailboxForEvent(ctx, tx, teamID, seq, ev, meta, now); err != nil {
		return err
	}
	if err := fireTeamTimerTx(ctx, tx, ev.FireTimer, now); err != nil {
		return err
	}
	if err := armTeamTimersTx(ctx, tx, teamID, ev.ArmTimers, now); err != nil {
		return err
	}
	return cancelTimersOnTerminalEvent(ctx, tx, teamID, ev.TaskID, ev.EventType, toState, now)
}

// appendEventGate decides the vault stance of one append: an event payload or
// a mailbox row both seal under the team key, so either needs the vault.
func (s *Store) appendEventGate(ev *AppendTeamEventInput) (vault.Snapshot, bool, error) {
	snap, needVault, err := s.teamPayloadGate(ev.ProtectedPayload)
	if err != nil {
		return vault.Snapshot{}, false, err
	}
	if needVault || len(ev.Mailbox) == 0 {
		return snap, needVault, nil
	}
	snap, err = s.requireUnlockedVault()
	return snap, true, err
}

// appendMailboxForEvent seals and inserts the mailbox rows of one appended
// event inside its transaction (Q-122).
func (s *Store) appendMailboxForEvent(ctx context.Context, tx *sql.Tx, teamID string, seq int64, ev *AppendTeamEventInput, meta teamMetaRow, now int64) error {
	if len(ev.Mailbox) == 0 {
		return nil
	}
	rawKey, err := s.unwrapTeamKey(teamID, meta.KeyID, meta.WrappedKey)
	if err != nil {
		return err
	}
	defer clear(rawKey)
	return s.insertMailboxRows(ctx, tx, teamID, seq, ev.Mailbox, meta.KeyID, rawKey, now)
}

type teamMetaRow struct {
	State      TeamState
	LastSeq    int64
	KeyID      string
	WrappedKey []byte
}

func loadTeamMetaForUpdate(ctx context.Context, tx *sql.Tx, teamID string, expectedSeq int64) (teamMetaRow, error) {
	var row teamMetaRow
	var stateStr string
	err := tx.QueryRowContext(ctx,
		`SELECT state, last_seq, key_id, wrapped_key FROM team_records WHERE team_id = ?`,
		teamID,
	).Scan(&stateStr, &row.LastSeq, &row.KeyID, &row.WrappedKey)
	if errors.Is(err, sql.ErrNoRows) {
		return teamMetaRow{}, fmt.Errorf("%w: %s", ErrTeamNotFound, teamID)
	}
	if err != nil {
		return teamMetaRow{}, fmt.Errorf("query team record: %w", err)
	}
	if row.LastSeq != expectedSeq {
		return teamMetaRow{}, fmt.Errorf("%w: expected %d, actual %d", ErrSequenceConflict, expectedSeq, row.LastSeq)
	}
	row.State = TeamState(stateStr)
	return row, nil
}

func applyTeamOrTaskProjection(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	curTeamState TeamState,
	nextSeq, now int64,
	ev *AppendTeamEventInput,
) (string, string, error) {
	if !IsValidTeamEventType(ev.EventType) {
		return "", "", fmt.Errorf("%w: unknown team event type %q", ErrTeamInvalidInput, ev.EventType)
	}
	if isTeamEventType(ev.EventType) {
		return applyTeamProjection(ctx, tx, teamID, curTeamState, nextSeq, now, ev)
	}
	if ev.TaskID == "" {
		return "", "", fmt.Errorf("%w: task event %s requires task_id", ErrTeamInvalidInput, ev.EventType)
	}
	// Q-84: once the team is terminal, nothing further is appended for it;
	// F11.2/F11.3 cancel every task first, then append TeamEnded.
	if isTeamTerminal(curTeamState) {
		return "", "", fmt.Errorf("%w: %s on team %s in terminal state %s", ErrTeamTerminal, ev.EventType, teamID, curTeamState)
	}
	return applyTaskProjection(ctx, tx, teamID, nextSeq, now, ev)
}

func applyTeamProjection(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	curTeamState TeamState,
	nextSeq, now int64,
	ev *AppendTeamEventInput,
) (string, string, error) {
	if ev.TaskID != "" {
		return "", "", fmt.Errorf("%w: team event %s must not set task_id", ErrTeamInvalidInput, ev.EventType)
	}
	nextState, err := nextTeamState(curTeamState, ev.EventType, ev.Outcome, ev.FailureCode)
	if err != nil {
		return "", "", err
	}
	// Q-84 (settle tasks first): a team event that ends the team is accepted
	// only when every task is already terminal. The check runs inside this
	// transaction, so the append and the settledness it relies on commit or
	// roll back together.
	if isTeamTerminal(nextState) {
		if err := ensureTeamTasksSettled(ctx, tx, teamID); err != nil {
			return "", "", err
		}
	}
	if err := updateTeamProjection(ctx, tx, teamID, nextState, nextSeq, ev.EventType, ev.FailureCode, now); err != nil {
		return "", "", err
	}
	return string(curTeamState), string(nextState), nil
}

// ensureTeamTasksSettled rejects a terminal team outcome while any task of the
// team is still non-terminal; unknown counts as not settled, so a restarted
// task must go through its recovery decision before the team may end.
func ensureTeamTasksSettled(ctx context.Context, tx *sql.Tx, teamID string) error {
	var unsettled int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM team_tasks
		WHERE team_id = ? AND state NOT IN ('done', 'failed', 'stopped', 'cancelled')`,
		teamID,
	).Scan(&unsettled)
	if err != nil {
		return fmt.Errorf("count unsettled team tasks: %w", err)
	}
	if unsettled > 0 {
		return fmt.Errorf("%w: %d task(s) not terminal", ErrTeamTasksUnsettled, unsettled)
	}
	return nil
}

func applyTaskProjection(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	nextSeq, now int64,
	ev *AppendTeamEventInput,
) (string, string, error) {
	var curStateStr, roleStr string
	err := tx.QueryRowContext(ctx,
		`SELECT state, hierarchy_role FROM team_tasks WHERE team_id = ? AND task_id = ?`,
		teamID, ev.TaskID,
	).Scan(&curStateStr, &roleStr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: %s", ErrTaskNotFound, ev.TaskID)
	}
	if err != nil {
		return "", "", fmt.Errorf("query task row: %w", err)
	}
	curState := TaskState(curStateStr)
	nextState, err := nextTaskState(curState, ev.EventType, ev.Outcome, ev.FailureCode)
	if err != nil {
		return "", "", err
	}
	if nextState == TaskStateChecking && TeamRole(roleStr) == RoleTeamManager {
		if err := ensureManagerChildrenTerminal(ctx, tx, teamID, ev.TaskID); err != nil {
			return "", "", err
		}
	}
	if err := updateTaskAndTeamSeq(ctx, tx, teamID, ev.TaskID, nextState, nextSeq, ev.FailureCode, now); err != nil {
		return "", "", err
	}
	if err := updateTaskSessionOnEvent(ctx, tx, teamID, ev); err != nil {
		return "", "", err
	}
	if err := reparentChildrenOnManagerCancel(ctx, tx, teamID, ev, TeamRole(roleStr), now); err != nil {
		return "", "", err
	}
	return curStateStr, string(nextState), nil
}

func updateTaskSessionOnEvent(ctx context.Context, tx *sql.Tx, teamID string, ev *AppendTeamEventInput) error {
	if ev.SessionID == "" {
		return nil
	}
	if ev.EventType != EventTaskAssigned && ev.EventType != EventTaskRecoveryDecided && ev.EventType != EventTaskQuestionAnswered {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE team_tasks SET session_id = ? WHERE team_id = ? AND task_id = ?`,
		ev.SessionID, teamID, ev.TaskID); err != nil {
		return fmt.Errorf("record the task session: %w", err)
	}
	return nil
}

func ensureManagerChildrenTerminal(ctx context.Context, tx *sql.Tx, teamID, parentTaskID string) error {
	var nonTerminal int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM team_tasks
		WHERE team_id = ? AND parent_task_id = ?
		  AND state NOT IN ('done', 'failed', 'stopped', 'cancelled')`,
		teamID, parentTaskID,
	).Scan(&nonTerminal)
	if err != nil {
		return fmt.Errorf("count manager non-terminal children: %w", err)
	}
	if nonTerminal > 0 {
		return fmt.Errorf("%w: %d child task(s) still non-terminal", ErrTeamChildrenNotTerminal, nonTerminal)
	}
	return nil
}

func updateTeamProjection(ctx context.Context, tx *sql.Tx, teamID string, state TeamState, seq int64, evType TeamEventType, failureCode string, now int64) error {
	var endedAt any
	if isTeamTerminal(state) {
		endedAt = now
	}
	waitingOp, waitingVal := 0, ""
	switch {
	case evType == EventTeamDeadlineReached:
		waitingOp, waitingVal = 1, TeamWaitingDeadline
	case evType == EventTeamDeadlineExtended || isTeamTerminal(state):
		waitingOp, waitingVal = 1, ""
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE team_records
		SET state = ?, last_seq = ?, failure_code = ?, updated_at = ?, ended_at = COALESCE(?, ended_at),
		    waiting_reason = CASE WHEN ? = 1 THEN ? ELSE waiting_reason END
		WHERE team_id = ?`,
		string(state), seq, failureCode, now, endedAt, waitingOp, waitingVal, teamID,
	)
	if err != nil {
		return fmt.Errorf("update team projection: %w", err)
	}
	return nil
}
