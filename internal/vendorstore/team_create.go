package vendorstore

// The create path of the team store (F11.1, F11.2b, F11.10a): one transaction
// writes the team record with its wrapped key, the TeamProposed event and its
// channel `plan` line, every initial task row and its TaskCreated event, all
// sealed under the fresh per-team key (INV-09).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/vault"
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
	if propErr := s.insertTeamProposedEvent(ctx, tx, in, wrapped.KeyID, rawKey, now); propErr != nil {
		return TeamRecord{}, nil, propErr
	}
	tasks, err := s.insertInitialTasks(ctx, tx, in, depths, rawKey, wrapped.KeyID, now)
	if err != nil {
		return TeamRecord{}, nil, err
	}
	if err := s.commitTeamTx(in.TeamID, lastSeq, snap, true, tx.Commit); err != nil {
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

func (s *Store) insertTeamProposedEvent(ctx context.Context, tx *sql.Tx, in *NewTeamInput, keyID string, rawKey []byte, now int64) error {
	var envelope any
	if len(in.ProposedPayload) > 0 {
		env, err := sealing.SealPayload(randomReader{s.random}, teamEventRecordType, in.TeamID, in.ProposedEventID, string(EventTeamProposed), keyID, rawKey, in.ProposedPayload)
		if err != nil {
			return fmt.Errorf("seal TeamProposed payload: %w", err)
		}
		envelope = env
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO team_task_events (
			team_id, sequence, event_id, task_id, event_type, outcome,
			from_state, to_state, failure_code, payload_envelope, created_at
		) VALUES (?, 1, ?, '', ?, '', '', ?, '', ?, ?)`,
		in.TeamID, in.ProposedEventID, string(EventTeamProposed), string(TeamStateProposed), envelope, now,
	)
	if err != nil {
		return fmt.Errorf("insert TeamProposed event: %w", err)
	}
	// §6.7: the proposed plan is the team channel's first `plan` line; the
	// content stays sealed in the event payload, the row is metadata only.
	if err := appendChannelForEvent(ctx, tx, in.TeamID, 1, &AppendTeamEventInput{
		EventType: EventTeamProposed, ProtectedPayload: in.ProposedPayload,
	}, now); err != nil {
		return err
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
			spec_envelope, last_event_seq, created_at, updated_at, ended_at, explicit_start
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, NULL, ?)`,
		t.TaskID, teamID, t.ParentTaskID, string(t.HierarchyRole), t.CatalogRole,
		string(t.Deliverable), string(depsJSON), depth, string(TaskStateQueued),
		specEnv, seq, now, now, t.ExplicitStart,
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
