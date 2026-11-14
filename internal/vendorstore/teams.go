package vendorstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	teamKeyRecordType     = "team.key"
	teamSpecRecordType    = "team.task.spec"
	teamEventRecordType   = "team.event.payload"
	teamDefaultKeyVersion = "v1"
)

// CreateTeam persists a new team, its TeamProposed event (seq 1), all initial tasks,
// and their TaskCreated events atomically in a single SQLite transaction.
// Every task's ProtectedSpec is sealed under a freshly wrapped per-team key while
// metadata columns remain strictly allowlisted (INV-09).
func (s *Store) CreateTeam(ctx context.Context, in *NewTeamInput) (TeamRecord, []TaskRecord, error) {
	if in == nil || in.TeamID == "" || in.WorkspaceID == "" || in.ProposedEventID == "" {
		return TeamRecord{}, nil, fmt.Errorf("%w: team_id, workspace_id, and proposed_event_id are required", ErrTeamInvalidInput)
	}
	depths, err := validateNewTeamTasks(in.Tasks)
	if err != nil {
		return TeamRecord{}, nil, err
	}
	snap, err := s.requireUnlockedVault()
	if err != nil {
		return TeamRecord{}, nil, err
	}
	rawKey, wrapped, err := sealing.GenerateSessionKey(s.vault, s.random, teamKeyRecordType, in.TeamID, teamDefaultKeyVersion)
	if err != nil {
		return TeamRecord{}, nil, fmt.Errorf("%w: generate team key: %w", ErrUnavailable, err)
	}
	// Zero the raw key on every return, not only after a successful commit:
	// a duplicate team, a sealing failure or a vault lock mid-create must not
	// leave key material in memory.
	defer clear(rawKey)
	now := s.clock.Now().UnixMilli()
	return s.commitNewTeam(ctx, snap, in, depths, rawKey, wrapped, now)
}

func (s *Store) commitNewTeam(
	ctx context.Context,
	snap vault.Snapshot,
	in *NewTeamInput,
	depths map[string]int,
	rawKey []byte,
	wrapped *sealing.SessionKey,
	now int64,
) (TeamRecord, []TaskRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamRecord{}, nil, fmt.Errorf("begin create team tx: %w", err)
	}
	defer rollback(ctx, tx)

	lastSeq := int64(1 + len(in.Tasks))
	rec := TeamRecord{
		TeamID:      in.TeamID,
		WorkspaceID: in.WorkspaceID,
		SessionID:   in.SessionID,
		State:       TeamStateProposed,
		LastSeq:     lastSeq,
		TaskCount:   len(in.Tasks),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if insErr := insertTeamRecordRow(ctx, tx, &rec, wrapped); insErr != nil {
		return TeamRecord{}, nil, insErr
	}
	if propErr := insertTeamProposedEvent(ctx, tx, in, now); propErr != nil {
		return TeamRecord{}, nil, propErr
	}
	tasks, err := s.insertInitialTasks(ctx, tx, in, depths, rawKey, wrapped.KeyID, now)
	if err != nil {
		return TeamRecord{}, nil, err
	}
	if err := s.commitTeamTx(snap, true, tx.Commit); err != nil {
		return TeamRecord{}, nil, err
	}
	return rec, tasks, nil
}

func insertTeamRecordRow(ctx context.Context, tx *sql.Tx, rec *TeamRecord, wrapped *sealing.SessionKey) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT team_id FROM team_records WHERE team_id = ?`, rec.TeamID).Scan(&existing)
	if err == nil {
		return fmt.Errorf("%w: %s", ErrTeamExists, rec.TeamID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check team existence: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_records (
			team_id, workspace_id, session_id, state, last_seq, task_count,
			key_id, wrapped_key, failure_code, created_at, updated_at, ended_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, NULL)`,
		rec.TeamID, rec.WorkspaceID, rec.SessionID, string(rec.State),
		rec.LastSeq, rec.TaskCount, wrapped.KeyID, wrapped.WrappedKey,
		rec.CreatedAt, rec.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert team record: %w", err)
	}
	return nil
}

func insertTeamProposedEvent(ctx context.Context, tx *sql.Tx, in *NewTeamInput, now int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO team_task_events (
			team_id, sequence, event_id, task_id, event_type, outcome,
			from_state, to_state, failure_code, payload_envelope, created_at
		) VALUES (?, 1, ?, '', ?, '', '', ?, '', NULL, ?)`,
		in.TeamID, in.ProposedEventID, string(EventTeamProposed), string(TeamStateProposed), now,
	)
	if err != nil {
		return fmt.Errorf("insert TeamProposed event: %w", err)
	}
	return nil
}

func (s *Store) insertInitialTasks(
	ctx context.Context,
	tx *sql.Tx,
	in *NewTeamInput,
	depths map[string]int,
	rawKey []byte,
	keyID string,
	now int64,
) ([]TaskRecord, error) {
	tasks := make([]TaskRecord, 0, len(in.Tasks))
	for i := range in.Tasks {
		t := &in.Tasks[i]
		seq := int64(2 + i)
		specEnv, err := sealing.SealPayload(randomReader{s.random}, teamSpecRecordType, in.TeamID, t.TaskID, "spec", keyID, rawKey, t.ProtectedSpec)
		if err != nil {
			return nil, fmt.Errorf("seal task spec: %w", err)
		}
		evEnv, err := sealing.SealPayload(randomReader{s.random}, teamEventRecordType, in.TeamID, t.CreatedEventID, string(EventTaskCreated), keyID, rawKey, t.ProtectedSpec)
		if err != nil {
			return nil, fmt.Errorf("seal TaskCreated payload: %w", err)
		}
		deps := append([]string{}, t.DependsOn...)
		depsJSON, err := json.Marshal(deps)
		if err != nil {
			return nil, fmt.Errorf("marshal depends_on: %w", err)
		}
		if err := insertTaskAndCreatedEvent(ctx, tx, in.TeamID, t, depths[t.TaskID], seq, depsJSON, specEnv, evEnv, now); err != nil {
			return nil, err
		}
		tasks = append(tasks, TaskRecord{
			TaskID: t.TaskID, TeamID: in.TeamID, ParentTaskID: t.ParentTaskID,
			HierarchyRole: t.HierarchyRole, CatalogRole: t.CatalogRole, Deliverable: t.Deliverable,
			DependsOn: deps, Depth: depths[t.TaskID], State: TaskStateQueued,
			LastEventSeq: seq, CreatedAt: now, UpdatedAt: now,
		})
	}
	return tasks, nil
}

func insertTaskAndCreatedEvent(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	t *NewTaskInput,
	depth int,
	seq int64,
	depsJSON, specEnv, evEnv []byte,
	now int64,
) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO team_tasks (
			task_id, team_id, parent_task_id, hierarchy_role, catalog_role,
			deliverable, depends_on_json, depth, state, failure_code,
			spec_envelope, last_event_seq, created_at, updated_at, ended_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, NULL)`,
		t.TaskID, teamID, t.ParentTaskID, string(t.HierarchyRole), t.CatalogRole,
		string(t.Deliverable), string(depsJSON), depth, string(TaskStateQueued),
		specEnv, seq, now, now,
	)
	if err != nil {
		return fmt.Errorf("insert team task %s: %w", t.TaskID, err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_task_events (
			team_id, sequence, event_id, task_id, event_type, outcome,
			from_state, to_state, failure_code, payload_envelope, created_at
		) VALUES (?, ?, ?, ?, ?, '', '', ?, '', ?, ?)`,
		teamID, seq, t.CreatedEventID, t.TaskID, string(EventTaskCreated),
		string(TaskStateQueued), evEnv, now,
	)
	if err != nil {
		return fmt.Errorf("insert TaskCreated event: %w", err)
	}
	return nil
}

// AppendTeamEvent optimistically appends one validated team or task event and
// updates the team/task projection within the same transaction.
func (s *Store) AppendTeamEvent(ctx context.Context, teamID string, expectedSeq int64, ev *AppendTeamEventInput) (int64, error) {
	if ev == nil || teamID == "" || ev.EventID == "" || expectedSeq < 1 {
		return 0, fmt.Errorf("%w: team_id, event_id, and positive expected_seq are required", ErrTeamInvalidInput)
	}
	snap, needVault, err := s.teamPayloadGate(ev.ProtectedPayload)
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
	fromState, toState, err := applyTeamOrTaskProjection(ctx, tx, teamID, meta.State, nextSeq, now, ev)
	if err != nil {
		return 0, err
	}
	envelope, err := s.sealEventPayloadIfPresent(teamID, meta.KeyID, meta.WrappedKey, ev)
	if err != nil {
		return 0, err
	}
	if err := insertAppendedTeamEvent(ctx, tx, teamID, nextSeq, ev, fromState, toState, envelope, now); err != nil {
		return 0, err
	}
	if err := s.commitTeamTx(snap, needVault, tx.Commit); err != nil {
		return 0, err
	}
	return nextSeq, nil
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
	if err := updateTeamProjection(ctx, tx, teamID, nextState, nextSeq, ev.FailureCode, now); err != nil {
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
	return curStateStr, string(nextState), nil
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

func updateTeamProjection(ctx context.Context, tx *sql.Tx, teamID string, state TeamState, seq int64, failureCode string, now int64) error {
	var endedAt any
	if isTeamTerminal(state) {
		endedAt = now
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE team_records
		SET state = ?, last_seq = ?, failure_code = ?, updated_at = ?, ended_at = COALESCE(?, ended_at)
		WHERE team_id = ?`,
		string(state), seq, failureCode, now, endedAt, teamID,
	)
	if err != nil {
		return fmt.Errorf("update team projection: %w", err)
	}
	return nil
}

func updateTaskAndTeamSeq(ctx context.Context, tx *sql.Tx, teamID, taskID string, state TaskState, seq int64, failureCode string, now int64) error {
	var endedAt any
	if isTaskTerminal(state) {
		endedAt = now
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_tasks
		SET state = ?, last_event_seq = ?, failure_code = CASE WHEN ? != '' THEN ? ELSE failure_code END,
		    updated_at = ?, ended_at = COALESCE(?, ended_at)
		WHERE team_id = ? AND task_id = ?`,
		string(state), seq, failureCode, failureCode, now, endedAt, teamID, taskID,
	); err != nil {
		return fmt.Errorf("update task projection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_records SET last_seq = ?, updated_at = ? WHERE team_id = ?`,
		seq, now, teamID,
	); err != nil {
		return fmt.Errorf("advance team last_seq: %w", err)
	}
	return nil
}

func insertAppendedTeamEvent(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	seq int64,
	ev *AppendTeamEventInput,
	fromState, toState string,
	envelope []byte,
	now int64,
) error {
	var envParam any
	if len(envelope) > 0 {
		envParam = envelope
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO team_task_events (
			team_id, sequence, event_id, task_id, event_type, outcome,
			from_state, to_state, failure_code, payload_envelope, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		teamID, seq, ev.EventID, ev.TaskID, string(ev.EventType), ev.Outcome,
		fromState, toState, ev.FailureCode, envParam, now,
	)
	if err != nil {
		return fmt.Errorf("insert team_task_event: %w", err)
	}
	return nil
}
