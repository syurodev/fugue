package secretstore

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/secrets"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	// ScopeGlobal is the scope_kind for host-wide secrets.
	ScopeGlobal = "global"
	// ScopeWorkspace is the scope_kind for workspace-scoped secrets.
	ScopeWorkspace = "workspace"

	// MaxNameBytes is the maximum length of a secret name (host-api §5.5).
	MaxNameBytes = 64
	// MaxDescriptionBytes is the maximum length of a secret description (host-api §5.5).
	MaxDescriptionBytes = 256
	// MaxValueBytes is the maximum size of a secret value: 64 KiB (host-api §5.5).
	MaxValueBytes = 64 * 1024
	// MaxSecretsPerScope is the maximum number of secrets per (scope_kind, scope_id) (host-api §5.5).
	MaxSecretsPerScope = 512
	// KnownValueMinBytes is the minimum secret length fed to KnownValues (content-safety §3.2).
	KnownValueMinBytes = 8

	recordTypeSecret = "secret_record"
	recordKeyID      = "v1"
)

var validSecretName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Meta is the unsealed metadata of one stored secret.
type Meta struct {
	SecretID    string
	ScopeKind   string
	ScopeID     string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Store owns secret_records and secret_events in SQLite.
type Store struct {
	db    *sql.DB
	vault *vault.Service
	clk   clock.Clock
	ids   ids.Generator
}

// New creates a secret store over db, sealed by v.
func New(db *sql.DB, v *vault.Service, clk clock.Clock, gen ids.Generator) *Store {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Store{db: db, vault: v, clk: clk, ids: gen}
}

func recordBinding(secretID, scopeKind, scopeID, name string) vault.RecordBinding {
	return vault.RecordBinding{
		RecordType: recordTypeSecret + ":" + scopeKind + ":" + scopeID + ":" + name,
		RecordID:   secretID,
		KeyID:      recordKeyID,
	}
}

// ValidateName checks that name is 1..64 ASCII [A-Za-z0-9_.-] starting with [A-Za-z0-9].
func ValidateName(name string) error {
	if !validSecretName.MatchString(name) {
		return errcode.New(errcode.SecretsInvalidName, "secret name must be 1..64 ASCII characters [A-Za-z0-9_.-] starting with a letter or digit")
	}
	return nil
}

// ValidateScope validates scopeKind and scopeID.
func ValidateScope(scopeKind, scopeID string) error {
	switch scopeKind {
	case ScopeGlobal:
		if scopeID != "" {
			return errcode.New(errcode.InvalidArgument, "global secret scope must not have a scope_id")
		}
		return nil
	case ScopeWorkspace:
		if scopeID == "" {
			return errcode.New(errcode.InvalidArgument, "workspace secret scope requires a scope_id")
		}
		return nil
	default:
		return errcode.New(errcode.InvalidArgument, "secret scope_kind must be global or workspace")
	}
}

func (s *Store) requireUnlocked() error {
	if s.vault == nil || s.vault.Snapshot().State != vault.StateUnlocked {
		return errcode.New(errcode.FailedPrecondition, "vault is locked")
	}
	return nil
}

// ListSecrets returns unsealed metadata for secrets matching the optional filter.
// Succeeds even while the vault is locked (INV-09).
func (s *Store) ListSecrets(ctx context.Context, scopeKind, scopeID string) ([]Meta, error) {
	if scopeKind != "" {
		if err := ValidateScope(scopeKind, scopeID); err != nil {
			return nil, err
		}
	} else if scopeID != "" {
		return nil, errcode.New(errcode.InvalidArgument, "scope_id filter requires scope_kind")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, readFailed("begin list secrets", err)
	}
	defer rollback(ctx, tx)
	return listSecretsTx(ctx, tx, scopeKind, scopeID)
}

func listSecretsTx(ctx context.Context, tx *sql.Tx, scopeKind, scopeID string) ([]Meta, error) {
	var rows *sql.Rows
	var err error
	if scopeKind != "" {
		rows, err = tx.QueryContext(ctx,
			`SELECT secret_id, scope_kind, scope_id, name, description, created_at, updated_at
			 FROM secret_records WHERE scope_kind = ? AND scope_id = ?
			 ORDER BY scope_kind, scope_id, name`,
			scopeKind, scopeID)
	} else {
		rows, err = tx.QueryContext(ctx,
			`SELECT secret_id, scope_kind, scope_id, name, description, created_at, updated_at
			 FROM secret_records
			 ORDER BY scope_kind, scope_id, name`)
	}
	if err != nil {
		return nil, readFailed("query secret metadata", err)
	}
	defer closeRows(ctx, rows)
	var out []Meta
	for rows.Next() {
		var m Meta
		var createdMS, updatedMS int64
		if scanErr := rows.Scan(&m.SecretID, &m.ScopeKind, &m.ScopeID, &m.Name, &m.Description, &createdMS, &updatedMS); scanErr != nil {
			return nil, readFailed("scan secret metadata", scanErr)
		}
		m.CreatedAt = time.UnixMilli(createdMS).UTC()
		m.UpdatedAt = time.UnixMilli(updatedMS).UTC()
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, readFailed("iterate secret metadata", err)
	}
	return out, nil
}

// PutSecret creates or updates a secret, sealing its value via vault.SealRecord.
// Fails closed when the vault is locked.
func (s *Store) PutSecret(ctx context.Context, scopeKind, scopeID, name, description string, value []byte) (Meta, int64, error) {
	if err := validatePutSecret(scopeKind, scopeID, name, description, value); err != nil {
		return Meta{}, 0, err
	}
	if err := s.requireUnlocked(); err != nil {
		return Meta{}, 0, err
	}
	now := s.clk.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Meta{}, 0, writeFailed("begin put secret", err)
	}
	defer rollback(ctx, tx)

	secretID, createdMS, isNew, err := s.prepareSecretIdentity(ctx, tx, scopeKind, scopeID, name, now.UnixMilli())
	if err != nil {
		return Meta{}, 0, err
	}
	sealed, sealErr := s.vault.SealRecord(recordBinding(secretID, scopeKind, scopeID, name), value)
	if sealErr != nil {
		return Meta{}, 0, errcode.Wrap(errcode.CodeOf(sealErr), "seal secret record", sealErr)
	}
	if err := upsertSecretRow(ctx, tx, isNew, secretID, scopeKind, scopeID, name, description, sealed, createdMS, now.UnixMilli()); err != nil {
		return Meta{}, 0, err
	}
	seq, seqErr := appendChange(ctx, tx, changeSecretPut, secretID, scopeKind, scopeID, name, now)
	if seqErr != nil {
		return Meta{}, 0, seqErr
	}
	if err := tx.Commit(); err != nil {
		return Meta{}, 0, writeFailed("commit put secret", err)
	}
	return Meta{
		SecretID:    secretID,
		ScopeKind:   scopeKind,
		ScopeID:     scopeID,
		Name:        name,
		Description: description,
		CreatedAt:   time.UnixMilli(createdMS).UTC(),
		UpdatedAt:   now,
	}, seq, nil
}

func validatePutSecret(scopeKind, scopeID, name, description string, value []byte) error {
	if err := ValidateScope(scopeKind, scopeID); err != nil {
		return err
	}
	if err := ValidateName(name); err != nil {
		return err
	}
	if len(description) > MaxDescriptionBytes || !utf8.ValidString(description) {
		return errcode.New(errcode.InvalidArgument, "secret description must be valid UTF-8 and at most 256 bytes")
	}
	if len(value) == 0 || len(value) > MaxValueBytes {
		return errcode.New(errcode.SecretsValueTooLarge, "secret value must be between 1 byte and 64 KiB")
	}
	return nil
}

func (s *Store) prepareSecretIdentity(ctx context.Context, tx *sql.Tx, scopeKind, scopeID, name string, nowMS int64) (string, int64, bool, error) {
	var secretID string
	var createdMS int64
	err := tx.QueryRowContext(ctx,
		`SELECT secret_id, created_at FROM secret_records WHERE scope_kind = ? AND scope_id = ? AND name = ?`,
		scopeKind, scopeID, name).Scan(&secretID, &createdMS)
	if err == nil {
		return secretID, createdMS, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, readFailed("lookup secret", err)
	}
	var count int
	if cntErr := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM secret_records WHERE scope_kind = ? AND scope_id = ?`,
		scopeKind, scopeID).Scan(&count); cntErr != nil {
		return "", 0, false, readFailed("count secrets in scope", cntErr)
	}
	if count >= MaxSecretsPerScope {
		return "", 0, false, errcode.New(errcode.SecretsLimitExceeded, "secret count limit for this scope reached")
	}
	if s.ids == nil {
		return "", 0, false, errcode.New(errcode.Internal, "id generator is required")
	}
	newID, idErr := s.ids.NewID()
	if idErr != nil {
		return "", 0, false, errcode.Wrap(errcode.Internal, "generate secret id", idErr)
	}
	return newID, nowMS, true, nil
}

func upsertSecretRow(ctx context.Context, tx *sql.Tx, isNew bool, secretID, scopeKind, scopeID, name, description string, sealed []byte, createdMS, nowMS int64) error {
	var err error
	if isNew {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO secret_records (secret_id, scope_kind, scope_id, name, description, key_id, sealed_value, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			secretID, scopeKind, scopeID, name, description, recordKeyID, sealed, createdMS, nowMS)
	} else {
		_, err = tx.ExecContext(ctx,
			`UPDATE secret_records SET description = ?, key_id = ?, sealed_value = ?, updated_at = ? WHERE secret_id = ?`,
			description, recordKeyID, sealed, nowMS, secretID)
	}
	if err != nil {
		return writeFailed("upsert secret record", err)
	}
	return nil
}

// DeleteSecret removes a secret record and records a metadata-only deletion event.
// Fails closed when the vault is locked.
func (s *Store) DeleteSecret(ctx context.Context, secretID string) (Meta, int64, error) {
	if secretID == "" {
		return Meta{}, 0, errcode.New(errcode.InvalidArgument, "secret_id is required")
	}
	if err := s.requireUnlocked(); err != nil {
		return Meta{}, 0, err
	}
	now := s.clk.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Meta{}, 0, writeFailed("begin delete secret", err)
	}
	defer rollback(ctx, tx)

	var m Meta
	var createdMS, updatedMS int64
	err = tx.QueryRowContext(ctx,
		`SELECT secret_id, scope_kind, scope_id, name, description, created_at, updated_at
		 FROM secret_records WHERE secret_id = ?`,
		secretID).Scan(&m.SecretID, &m.ScopeKind, &m.ScopeID, &m.Name, &m.Description, &createdMS, &updatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return Meta{}, 0, errcode.New(errcode.StoreRecordNotFound, "secret not found")
	}
	if err != nil {
		return Meta{}, 0, readFailed("lookup secret for delete", err)
	}
	m.CreatedAt = time.UnixMilli(createdMS).UTC()
	m.UpdatedAt = time.UnixMilli(updatedMS).UTC()

	if _, err := tx.ExecContext(ctx, `DELETE FROM secret_records WHERE secret_id = ?`, secretID); err != nil {
		return Meta{}, 0, writeFailed("delete secret record", err)
	}
	seq, seqErr := appendChange(ctx, tx, changeSecretDeleted, m.SecretID, m.ScopeKind, m.ScopeID, m.Name, now)
	if seqErr != nil {
		return Meta{}, 0, seqErr
	}
	if err := tx.Commit(); err != nil {
		return Meta{}, 0, writeFailed("commit delete secret", err)
	}
	return m, seq, nil
}

// OpenSecretValue unseals and returns the plaintext bytes of secretID.
// Intended for host-internal use only; callers must clear the returned slice.
func (s *Store) OpenSecretValue(ctx context.Context, secretID string) ([]byte, error) {
	if secretID == "" {
		return nil, errcode.New(errcode.InvalidArgument, "secret_id is required")
	}
	if err := s.requireUnlocked(); err != nil {
		return nil, err
	}
	var scopeKind, scopeID, name, keyID string
	var sealed []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT scope_kind, scope_id, name, key_id, sealed_value FROM secret_records WHERE secret_id = ?`,
		secretID).Scan(&scopeKind, &scopeID, &name, &keyID, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.StoreRecordNotFound, "secret not found")
	}
	if err != nil {
		return nil, readFailed("read secret record", err)
	}
	binding := recordBinding(secretID, scopeKind, scopeID, name)
	binding.KeyID = keyID
	plain, openErr := s.vault.OpenRecord(binding, sealed)
	if openErr != nil {
		return nil, errcode.Wrap(errcode.CodeOf(openErr), "open secret record", openErr)
	}
	return plain, nil
}

// KnownValues unseals all stored secrets >= KnownValueMinBytes for redaction
// (content-safety §3.2, Q-34). Returns nil when the vault is locked.
func (s *Store) KnownValues(ctx context.Context) ([]secrets.KnownValue, error) {
	if s.vault == nil || s.vault.Snapshot().State != vault.StateUnlocked {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT secret_id, scope_kind, scope_id, name, key_id, sealed_value FROM secret_records`)
	if err != nil {
		return nil, readFailed("query secret records for known values", err)
	}
	defer closeRows(ctx, rows)

	var out []secrets.KnownValue
	for rows.Next() {
		var secretID, scopeKind, scopeID, name, keyID string
		var sealed []byte
		if scanErr := rows.Scan(&secretID, &scopeKind, &scopeID, &name, &keyID, &sealed); scanErr != nil {
			return nil, readFailed("scan secret record for known values", scanErr)
		}
		binding := recordBinding(secretID, scopeKind, scopeID, name)
		binding.KeyID = keyID
		plain, openErr := s.vault.OpenRecord(binding, sealed)
		if openErr != nil {
			if errors.Is(openErr, vault.ErrLocked) {
				return nil, nil
			}
			return nil, errcode.Wrap(errcode.CodeOf(openErr), "open secret for known values", openErr)
		}
		if len(plain) >= KnownValueMinBytes {
			out = append(out, secrets.KnownValue{Kind: "vault", Value: redact.NewSecret(string(plain))})
		}
		clear(plain)
	}
	if err := rows.Err(); err != nil {
		return nil, readFailed("iterate secret records for known values", err)
	}
	return out, nil
}

func readFailed(op string, err error) error {
	return errcode.Wrap(errcode.Internal, op, err)
}

func writeFailed(op string, err error) error {
	return errcode.Wrap(errcode.Internal, op, err)
}

func rollback(ctx context.Context, tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) && ctx.Err() == nil {
		slog.Warn("rollback secretstore tx", "err", err)
	}
}

func closeRows(ctx context.Context, rows *sql.Rows) {
	if err := rows.Close(); err != nil && ctx.Err() == nil {
		slog.Warn("close secretstore rows", "err", err)
	}
}
