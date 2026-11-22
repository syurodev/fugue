package secretstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	changeSecretPut     = "secret_put"
	changeSecretDeleted = "secret_deleted"

	// JournalRetention bounds the number of retained secret_events rows.
	JournalRetention = 4096
)

// ErrChangeCursorTooOld signals that the requested sequence was pruned.
var ErrChangeCursorTooOld = errors.New("secret change cursor is older than the retained journal")

// Change is one metadata-only secret journal entry.
type Change struct {
	Sequence  int64
	Kind      string
	SecretID  string
	ScopeKind string
	ScopeID   string
	Name      string
}

// Snapshot is one consistent read of all secret metadata and the journal sequence.
type Snapshot struct {
	Secrets  []Meta
	Sequence int64
}

// ChangeSequence converts a committed journal sequence to uint64.
func ChangeSequence(n int64) (uint64, error) {
	if n < 0 {
		return 0, errcode.New(errcode.Internal, "negative secret change sequence")
	}
	return uint64(n), nil
}

func appendChange(ctx context.Context, tx *sql.Tx, kind, secretID, scopeKind, scopeID, name string, now time.Time) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO secret_events (kind, secret_id, scope_kind, scope_id, name, recorded_at) VALUES (?, ?, ?, ?, ?, ?)`,
		kind, secretID, scopeKind, scopeID, name, now.UnixMilli())
	if err != nil {
		return 0, writeFailed("append secret change", err)
	}
	if _, pruneErr := tx.ExecContext(ctx,
		`DELETE FROM secret_events WHERE seq NOT IN (SELECT seq FROM secret_events ORDER BY seq DESC LIMIT ?)`,
		JournalRetention); pruneErr != nil {
		return 0, writeFailed("prune secret journal", pruneErr)
	}
	seq, lastErr := res.LastInsertId()
	if lastErr != nil {
		return 0, writeFailed("read secret change sequence", lastErr)
	}
	return seq, nil
}

// Snapshot reads all unsealed secret metadata and the latest journal sequence
// in a single read-only transaction (works while vault is locked, INV-09).
func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Snapshot{}, readFailed("begin secret snapshot", err)
	}
	defer rollback(ctx, tx)

	var maxSeq sql.NullInt64
	if scanErr := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM secret_events`).Scan(&maxSeq); scanErr != nil {
		return Snapshot{}, readFailed("read secret snapshot sequence", scanErr)
	}
	metas, err := listSecretsTx(ctx, tx, "", "")
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		Secrets:  metas,
		Sequence: maxSeq.Int64,
	}, nil
}

// ChangesSince returns every journal row with seq > afterSeq, oldest first.
func (s *Store) ChangesSince(ctx context.Context, afterSeq int64) ([]Change, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, readFailed("begin secret catch-up", err)
	}
	defer rollback(ctx, tx)

	var oldest sql.NullInt64
	if scanErr := tx.QueryRowContext(ctx, `SELECT MIN(seq) FROM secret_events`).Scan(&oldest); scanErr != nil {
		return nil, readFailed("read secret journal oldest row", scanErr)
	}
	if oldest.Valid && afterSeq > 0 && afterSeq < oldest.Int64-1 {
		return nil, ErrChangeCursorTooOld
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT seq, kind, secret_id, scope_kind, scope_id, name
		 FROM secret_events WHERE seq > ? ORDER BY seq ASC`,
		afterSeq)
	if err != nil {
		return nil, readFailed("query secret changes", err)
	}
	defer closeRows(ctx, rows)

	var out []Change
	for rows.Next() {
		var c Change
		if scanErr := rows.Scan(&c.Sequence, &c.Kind, &c.SecretID, &c.ScopeKind, &c.ScopeID, &c.Name); scanErr != nil {
			return nil, readFailed("scan secret change", scanErr)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, readFailed("iterate secret changes", err)
	}
	return out, nil
}
