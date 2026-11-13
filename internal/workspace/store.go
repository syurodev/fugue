package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	workspaceColumns = "workspace_id, config_revision, policy_revision, config_hash, accepted_config_hash, missing_roots, created_at"
)

// Record is the database metadata record for a workspace (WS-04).
type Record struct {
	WorkspaceID                    string
	ConfigRevision, PolicyRevision int64
	ConfigHash, AcceptedConfigHash string
	MissingRoots                   []string
	CreatedAt                      string
}

// Store manages database persistence for the workspaces table.
type Store struct {
	db *sql.DB
}

// NewStore returns a new Store backed by db.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Create inserts a new workspace record.
func (s *Store) Create(ctx context.Context, rec *Record) error {
	missingJSON := marshalMissing(rec.MissingRoots)
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO workspaces ("+workspaceColumns+") VALUES (?, ?, ?, ?, ?, ?, ?)",
		rec.WorkspaceID, rec.ConfigRevision, rec.PolicyRevision, rec.ConfigHash, rec.AcceptedConfigHash, missingJSON, rec.CreatedAt,
	)
	if err != nil {
		return WrapError(ErrAlreadyExists, "insert workspace "+rec.WorkspaceID, err)
	}
	return nil
}

// Get retrieves a workspace record by workspace ID.
func (s *Store) Get(ctx context.Context, id string) (*Record, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+workspaceColumns+" FROM workspaces WHERE workspace_id = ?", id)
	rec, err := scanRecord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NewError(ErrNotFound, fmt.Sprintf("workspace %q not found", id))
		}
		return nil, WrapError(ErrNotFound, "query workspace "+id, err)
	}
	return rec, nil
}

// Update updates revisions, hashes, and missing_roots for an existing workspace.
func (s *Store) Update(ctx context.Context, rec *Record) error {
	return s.execUpdate(ctx,
		"UPDATE workspaces SET config_revision = ?, policy_revision = ?, config_hash = ?, accepted_config_hash = ?, missing_roots = ? WHERE workspace_id = ?",
		rec.ConfigRevision, rec.PolicyRevision, rec.ConfigHash, rec.AcceptedConfigHash, marshalMissing(rec.MissingRoots), rec.WorkspaceID,
	)
}

// UpdateMissingRoots updates only the missing_roots column for a workspace.
func (s *Store) UpdateMissingRoots(ctx context.Context, id string, missingRoots []string) error {
	return s.execUpdate(ctx, "UPDATE workspaces SET missing_roots = ? WHERE workspace_id = ?", marshalMissing(missingRoots), id)
}

func (s *Store) execUpdate(ctx context.Context, query string, args ...any) error {
	res, execErr := s.db.ExecContext(ctx, query, args...)
	if execErr != nil {
		return WrapError(ErrNotFound, "execute update", execErr)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr != nil || rows == 0 {
		return NewError(ErrNotFound, "workspace not found")
	}
	return nil
}

// List returns all workspace records ordered by created_at.
func (s *Store) List(ctx context.Context) ([]*Record, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+workspaceColumns+" FROM workspaces ORDER BY created_at ASC")
	if err != nil {
		return nil, WrapError(ErrNotFound, "list workspaces", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			return
		}
	}()

	var records []*Record
	for rows.Next() {
		rec, scanErr := scanRecord(rows)
		if scanErr != nil {
			return nil, WrapError(ErrNotFound, "scan workspace row", scanErr)
		}
		records = append(records, rec)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, WrapError(ErrNotFound, "iterate workspace rows", rowsErr)
	}
	return records, nil
}

// Delete removes a workspace record from the database.
func (s *Store) Delete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM workspaces WHERE workspace_id = ?", id); err != nil {
		return WrapError(ErrNotFound, "delete workspace "+id, err)
	}
	return nil
}

// DeleteExpected deletes a workspace only if its stored config hash still
// matches the reviewed version.
func (s *Store) DeleteExpected(ctx context.Context, id, expectedHash string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM workspaces WHERE workspace_id = ? AND config_hash = ?", id, expectedHash)
	if err != nil {
		return WrapError(ErrDeleteFailed, "delete workspace record", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return WrapError(ErrDeleteFailed, "check workspace deletion", err)
	}
	if rows == 0 {
		return NewError(ErrConfigChanged, "workspace configuration hash does not match expected_hash")
	}
	return nil
}

func marshalMissing(roots []string) string {
	b, err := json.Marshal(roots)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func scanRecord(row interface{ Scan(...any) error }) (*Record, error) {
	var rec Record
	var missingJSON string
	if err := row.Scan(&rec.WorkspaceID, &rec.ConfigRevision, &rec.PolicyRevision, &rec.ConfigHash, &rec.AcceptedConfigHash, &missingJSON, &rec.CreatedAt); err != nil {
		return nil, WrapError(ErrNotFound, "scan workspace row", err)
	}
	if err := json.Unmarshal([]byte(missingJSON), &rec.MissingRoots); err != nil {
		rec.MissingRoots = nil
	}
	return &rec, nil
}
