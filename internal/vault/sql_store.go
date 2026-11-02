package vault

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

const keyringRowID = "keyring"

// SQLKeyringStore persists the single vault keyring row in the vault_meta
// SQLite table. Only wrapped key material and KDF parameters are stored.
type SQLKeyringStore struct {
	db *sql.DB
}

// NewSQLKeyringStore returns a KeyringStore backed by vault_meta in db.
func NewSQLKeyringStore(db *sql.DB) *SQLKeyringStore {
	return &SQLKeyringStore{db: db}
}

// Exists reports whether the keyring row is present in vault_meta.
func (s *SQLKeyringStore) Exists(ctx context.Context) (bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM vault_meta WHERE id = ?", keyringRowID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errcode.Wrap(errcode.Internal, "query vault_meta", err)
	}
	return true, nil
}

// Load reads and decodes the keyring row from vault_meta.
func (s *SQLKeyringStore) Load(ctx context.Context) (*crypto.Keyring, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM vault_meta WHERE id = ?", keyringRowID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "keyring not found")
	}
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "read vault_meta", err)
	}
	var keyring crypto.Keyring
	if err := json.Unmarshal(raw, &keyring); err != nil {
		return nil, errcode.Wrap(errcode.Internal, "decode keyring", err)
	}
	return &keyring, nil
}

// Save writes the encoded keyring to vault_meta.
func (s *SQLKeyringStore) Save(ctx context.Context, keyring *crypto.Keyring) error {
	if keyring == nil {
		return errcode.New(errcode.InvalidArgument, "keyring is nil")
	}
	raw, err := json.Marshal(keyring)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "encode keyring", err)
	}
	if _, err := s.db.ExecContext(
		ctx,
		"INSERT INTO vault_meta (id, data) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data",
		keyringRowID,
		raw,
	); err != nil {
		return errcode.Wrap(errcode.Internal, "write vault_meta", err)
	}
	return nil
}
