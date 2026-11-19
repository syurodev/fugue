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

// MaxTaskRetries bounds how many automatic/delegator retries a task may run
// without user approval via fugue_team_amend (Q-126, OR-E16).
const MaxTaskRetries = 2

// TaskRetryInput describes one atomic retry of a failed task (Q-126).
type TaskRetryInput struct {
	ExpectedSeq       int64
	SupersededEventID string
	CreatedEventID    string
	SupersededPayload []byte
}

// TaskRetryResult returns the newly created queued retry attempt and new team sequence.
type TaskRetryResult struct {
	NewTask TaskRecord
	LastSeq int64
}

type retrySourceRow struct {
	parentTaskID string
	role         TeamRole
	catalogRole  string
	deliverable  TeamDeliverable
	dependsJSON  string
	depth        int
	state        TaskState
	specEnv      []byte
	attempt      int
}

// RetryTask atomically supersedes a failed task (failed -> stopped via
// TaskSuperseded) and inserts the next attempt (<root>~r<n>, queued) with the
// identical sealed spec in one SQLite transaction (Q-126).
func (s *Store) RetryTask(ctx context.Context, teamID, taskID string, in *TaskRetryInput) (TaskRetryResult, error) {
	if in == nil || teamID == "" || taskID == "" || in.ExpectedSeq < 1 ||
		in.SupersededEventID == "" || in.CreatedEventID == "" {
		return TaskRetryResult{}, fmt.Errorf("%w: team_id, task_id, event IDs and positive expected_seq are required", ErrTeamInvalidInput)
	}
	snap, err := s.requireUnlockedVault()
	if err != nil {
		return TaskRetryResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskRetryResult{}, fmt.Errorf("begin retry task tx: %w", err)
	}
	defer rollback(ctx, tx)

	res, err := s.executeRetryTx(ctx, tx, teamID, taskID, in)
	if err != nil {
		return TaskRetryResult{}, err
	}
	if err := s.commitTeamTx(snap, true, tx.Commit); err != nil {
		return TaskRetryResult{}, err
	}
	return res, nil
}

func (s *Store) executeRetryTx(ctx context.Context, tx *sql.Tx, teamID, taskID string, in *TaskRetryInput) (TaskRetryResult, error) {
	meta, err := loadTeamMetaForUpdate(ctx, tx, teamID, in.ExpectedSeq)
	if err != nil {
		return TaskRetryResult{}, err
	}
	if isTeamTerminal(meta.State) {
		return TaskRetryResult{}, fmt.Errorf("%w: retry on terminal team %s", ErrTeamTerminal, teamID)
	}
	if meta.State != TeamStateActive {
		return TaskRetryResult{}, fmt.Errorf("%w: team %s is not active", ErrTeamTransition, teamID)
	}
	src, err := loadRetrySource(ctx, tx, teamID, taskID)
	if err != nil {
		return TaskRetryResult{}, err
	}
	if src.attempt >= 1+MaxTaskRetries {
		return TaskRetryResult{}, ErrRetryLimitReached
	}
	rootID, err := resolveRetryRoot(ctx, tx, teamID, taskID)
	if err != nil {
		return TaskRetryResult{}, err
	}
	newID := fmt.Sprintf("%s~r%d", rootID, src.attempt)
	now := s.clock.Now().UnixMilli()
	return s.commitRetryRows(ctx, tx, teamID, taskID, newID, meta, &src, in, now)
}

func (s *Store) commitRetryRows(
	ctx context.Context,
	tx *sql.Tx,
	teamID, oldTaskID, newTaskID string,
	meta teamMetaRow,
	src *retrySourceRow,
	in *TaskRetryInput,
	now int64,
) (TaskRetryResult, error) {
	rawKey, err := s.unwrapTeamKey(teamID, meta.KeyID, meta.WrappedKey)
	if err != nil {
		return TaskRetryResult{}, err
	}
	defer clear(rawKey)

	specBytes, err := sealing.OpenPayload(teamSpecRecordType, teamID, oldTaskID, "spec", meta.KeyID, rawKey, src.specEnv)
	if err != nil {
		return TaskRetryResult{}, fmt.Errorf("open superseded task spec: %w", err)
	}
	seq1 := meta.LastSeq + 1
	supEv := &AppendTeamEventInput{
		EventID: in.SupersededEventID, TaskID: oldTaskID, EventType: EventTaskSuperseded,
		ProtectedPayload: in.SupersededPayload,
	}
	if writeErr := s.writeAppendedEvent(ctx, tx, teamID, seq1, supEv, meta, now); writeErr != nil {
		return TaskRetryResult{}, writeErr
	}
	seq2 := seq1 + 1
	rec, err := s.insertRetriedTask(ctx, tx, teamID, oldTaskID, newTaskID, seq2, meta.KeyID, rawKey, specBytes, src, in.CreatedEventID, now)
	if err != nil {
		return TaskRetryResult{}, err
	}
	if err := bumpTeamAfterInsert(ctx, tx, teamID, seq2, 1, now); err != nil {
		return TaskRetryResult{}, err
	}
	return TaskRetryResult{NewTask: rec, LastSeq: seq2}, nil
}

func (s *Store) insertRetriedTask(
	ctx context.Context,
	tx *sql.Tx,
	teamID, oldTaskID, newTaskID string,
	seq int64,
	keyID string,
	rawKey, specBytes []byte,
	src *retrySourceRow,
	createdEventID string,
	now int64,
) (TaskRecord, error) {
	specEnv, err := sealing.SealPayload(randomReader{s.random}, teamSpecRecordType, teamID, newTaskID, "spec", keyID, rawKey, specBytes)
	if err != nil {
		return TaskRecord{}, fmt.Errorf("seal retry task spec: %w", err)
	}
	evEnv, err := sealing.SealPayload(randomReader{s.random}, teamEventRecordType, teamID, createdEventID, string(EventTaskCreated), keyID, rawKey, specBytes)
	if err != nil {
		return TaskRecord{}, fmt.Errorf("seal retry TaskCreated payload: %w", err)
	}
	attempt := src.attempt + 1
	if _, execErr := tx.ExecContext(ctx, `
		INSERT INTO team_tasks (
			task_id, team_id, parent_task_id, hierarchy_role, catalog_role,
			deliverable, depends_on_json, depth, state, failure_code,
			spec_envelope, last_event_seq, created_at, updated_at, ended_at,
			explicit_start, attempt, retry_of
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, NULL, 0, ?, ?)`,
		newTaskID, teamID, src.parentTaskID, string(src.role), src.catalogRole,
		string(src.deliverable), src.dependsJSON, src.depth, string(TaskStateQueued),
		specEnv, seq, now, now, attempt, oldTaskID,
	); execErr != nil {
		return TaskRecord{}, fmt.Errorf("insert retry task %s: %w", newTaskID, execErr)
	}
	if _, execErr := tx.ExecContext(ctx, `
		INSERT INTO team_task_events (
			team_id, sequence, event_id, task_id, event_type, outcome,
			from_state, to_state, failure_code, payload_envelope, created_at
		) VALUES (?, ?, ?, ?, ?, '', '', ?, '', ?, ?)`,
		teamID, seq, createdEventID, newTaskID, string(EventTaskCreated),
		string(TaskStateQueued), evEnv, now,
	); execErr != nil {
		return TaskRecord{}, fmt.Errorf("insert retry TaskCreated event: %w", execErr)
	}
	var deps []string
	if unmErr := json.Unmarshal([]byte(src.dependsJSON), &deps); unmErr != nil {
		return TaskRecord{}, fmt.Errorf("decode retry depends_on: %w", unmErr)
	}
	return TaskRecord{
		TaskID: newTaskID, TeamID: teamID, ParentTaskID: src.parentTaskID,
		HierarchyRole: src.role, CatalogRole: src.catalogRole, Deliverable: src.deliverable,
		DependsOn: deps, Depth: src.depth, State: TaskStateQueued,
		Attempt: attempt, RetryOf: oldTaskID, LastEventSeq: seq, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func loadRetrySource(ctx context.Context, tx *sql.Tx, teamID, taskID string) (retrySourceRow, error) {
	var row retrySourceRow
	var roleStr, delivStr, stateStr string
	err := tx.QueryRowContext(ctx, `
		SELECT parent_task_id, hierarchy_role, catalog_role, deliverable,
		       depends_on_json, depth, state, spec_envelope, attempt
		FROM team_tasks WHERE team_id = ? AND task_id = ?`,
		teamID, taskID,
	).Scan(&row.parentTaskID, &roleStr, &row.catalogRole, &delivStr,
		&row.dependsJSON, &row.depth, &stateStr, &row.specEnv, &row.attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return retrySourceRow{}, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if err != nil {
		return retrySourceRow{}, fmt.Errorf("query retry source task: %w", err)
	}
	row.role = TeamRole(roleStr)
	row.deliverable = TeamDeliverable(delivStr)
	row.state = TaskState(stateStr)
	if row.state != TaskStateFailed {
		return retrySourceRow{}, fmt.Errorf("%w: only failed tasks can retry (state is %s)", ErrTeamTransition, row.state)
	}
	var existingChild int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM team_tasks WHERE team_id = ? AND retry_of = ?`,
		teamID, taskID,
	).Scan(&existingChild); err != nil {
		return retrySourceRow{}, fmt.Errorf("check existing retry child: %w", err)
	}
	if existingChild > 0 {
		return retrySourceRow{}, fmt.Errorf("%w: task %s already retried", ErrTaskExists, taskID)
	}
	return row, nil
}

func resolveRetryRoot(ctx context.Context, tx *sql.Tx, teamID, taskID string) (string, error) {
	cur := taskID
	for range 16 {
		var prev string
		err := tx.QueryRowContext(ctx,
			`SELECT retry_of FROM team_tasks WHERE team_id = ? AND task_id = ?`,
			teamID, cur,
		).Scan(&prev)
		if err != nil {
			return "", fmt.Errorf("walk retry chain for %s: %w", cur, err)
		}
		if prev == "" {
			return cur, nil
		}
		cur = prev
	}
	return "", fmt.Errorf("%w: retry chain exceeds bound", ErrTeamInvalidInput)
}

// AppendTeamEvents optimistically appends multiple team or task events and
// their mailbox rows atomically in a single SQLite transaction.
func (s *Store) AppendTeamEvents(ctx context.Context, teamID string, expectedSeq int64, evs []*AppendTeamEventInput) (int64, error) {
	if teamID == "" || expectedSeq < 1 || len(evs) == 0 {
		return 0, fmt.Errorf("%w: team_id, positive expected_seq, and at least one event are required", ErrTeamInvalidInput)
	}
	snap, needVault, err := s.appendEventsGate(evs)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin append team events tx: %w", err)
	}
	defer rollback(ctx, tx)

	meta, err := loadTeamMetaForUpdate(ctx, tx, teamID, expectedSeq)
	if err != nil {
		return 0, err
	}
	seq := expectedSeq
	now := s.clock.Now().UnixMilli()
	for _, ev := range evs {
		if ev == nil || ev.EventID == "" {
			return 0, fmt.Errorf("%w: each appended event requires an event_id", ErrTeamInvalidInput)
		}
		seq++
		if writeErr := s.writeAppendedEvent(ctx, tx, teamID, seq, ev, meta, now); writeErr != nil {
			return 0, writeErr
		}
	}
	if err := s.commitTeamTx(snap, needVault, tx.Commit); err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) appendEventsGate(evs []*AppendTeamEventInput) (vault.Snapshot, bool, error) {
	for _, ev := range evs {
		if ev == nil {
			continue
		}
		if len(ev.ProtectedPayload) > 0 || len(ev.Mailbox) > 0 {
			snap, err := s.requireUnlockedVault()
			return snap, true, err
		}
	}
	return vault.Snapshot{}, false, nil
}
