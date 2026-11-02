package credentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	accountColumns = "id, provider, kind, label, signed_in, exhausted_until, cooldown_until, usage, observed_at, created_at"
	maxLabelBytes  = 200
)

// Book keeps the accounts in the credential_accounts table. A secret is sealed
// by the vault before it is written and opened only on request, so every call
// that needs one fails with vault.ErrLocked while the vault is locked.
type Book struct {
	db    *sql.DB
	vault *vault.Service
	clock clock.Clock
	ids   ids.Generator

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewBook returns a Book over db, which must hold the migrated schema.
func NewBook(db *sql.DB, v *vault.Service, clk clock.Clock, generator ids.Generator) *Book {
	return &Book{db: db, vault: v, clock: clk, ids: generator, mu: sync.Mutex{}, locks: map[string]*sync.Mutex{}}
}

// Get returns one account without its secret.
func (b *Book) Get(ctx context.Context, id string) (Account, error) {
	row := b.db.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM credential_accounts WHERE id = ?", id)
	return scanAccount(row)
}

// List returns every account, oldest first, without secrets.
func (b *Book) List(ctx context.Context) ([]Account, error) {
	rows, err := b.db.QueryContext(ctx, "SELECT "+accountColumns+" FROM credential_accounts ORDER BY created_at, id")
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "list accounts failed", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			return
		}
	}()
	var accounts []Account
	for rows.Next() {
		account, scanErr := scanAccount(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		accounts = append(accounts, account)
	}
	if err = rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.Internal, "list accounts failed", err)
	}
	return accounts, nil
}

// Delete removes an account and its sealed secret.
func (b *Book) Delete(ctx context.Context, id string) error {
	res, err := b.db.ExecContext(ctx, "DELETE FROM credential_accounts WHERE id = ?", id)
	if err = requireRow(res, err, "delete the account"); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.locks, id)
	b.mu.Unlock()
	return nil
}

// SetSignedIn records whether the provider still accepts the account's credential.
func (b *Book) SetSignedIn(ctx context.Context, id string, signedIn bool) error {
	res, err := b.db.ExecContext(ctx, "UPDATE credential_accounts SET signed_in = ? WHERE id = ?", signedIn, id)
	return requireRow(res, err, "update the sign-in state")
}

// MarkExhausted records that a real limit refusal says the account is usable
// again at until. It only moves the marker later, never earlier.
func (b *Book) MarkExhausted(ctx context.Context, id string, until time.Time) error {
	res, err := b.db.ExecContext(ctx,
		"UPDATE credential_accounts SET exhausted_until = max(exhausted_until, ?) WHERE id = ?", millis(until), id)
	return requireRow(res, err, "mark the account exhausted")
}

// MarkCooldown keeps the account out of selection until the given time without
// saying it reached a limit. It only moves the marker later, never earlier.
func (b *Book) MarkCooldown(ctx context.Context, id string, until time.Time) error {
	res, err := b.db.ExecContext(ctx,
		"UPDATE credential_accounts SET cooldown_until = max(cooldown_until, ?) WHERE id = ?", millis(until), id)
	return requireRow(res, err, "put the account on cooldown")
}

// add seals the secret under the new account's binding and inserts the row.
func (b *Book) add(ctx context.Context, provider Provider, kind Kind, label string, plaintext []byte) (Account, error) {
	if err := validateKind(provider, kind); err != nil {
		return Account{}, err
	}
	label = strings.TrimSpace(label)
	if label == "" || len(label) > maxLabelBytes {
		return Account{}, errcode.New(errcode.InvalidArgument, "account label is empty or too long")
	}
	id, err := b.ids.NewID()
	if err != nil {
		return Account{}, errcode.Wrap(errcode.Internal, "account id failed", err)
	}
	sealed, err := b.vault.SealRecord(binding(provider, kind, id), plaintext)
	if err != nil {
		return Account{}, errcode.Wrap(errcode.CodeOf(err), "seal the credential failed", err)
	}
	account := Account{
		ID: id, Provider: provider, Kind: kind, Label: label, SignedIn: true,
		ExhaustedUntil: time.Time{}, CooldownUntil: time.Time{}, Usage: nil, ObservedAt: time.Time{},
		CreatedAt: b.clock.Now().UTC().Truncate(time.Millisecond),
	}
	_, err = b.db.ExecContext(ctx,
		"INSERT INTO credential_accounts (id, provider, kind, label, signed_in, secret, created_at) VALUES (?, ?, ?, ?, 1, ?, ?)",
		id, string(provider), string(kind), label, sealed, millis(account.CreatedAt))
	if err != nil {
		return Account{}, errcode.Wrap(errcode.Internal, "add the account failed", err)
	}
	return account, nil
}

// lockAccount serializes work on one account and returns the function that ends it.
func (b *Book) lockAccount(id string) func() {
	b.mu.Lock()
	lock, ok := b.locks[id]
	if !ok {
		lock = &sync.Mutex{}
		b.locks[id] = lock
	}
	b.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccount(row rowScanner) (Account, error) {
	var (
		a                                      Account
		provider, kind, usage                  string
		signedIn                               bool
		exhausted, cooldown, observed, created int64
	)
	err := row.Scan(&a.ID, &provider, &kind, &a.Label, &signedIn, &exhausted, &cooldown, &usage, &observed, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, errcode.Wrap(errcode.NotFound, "account not found", err)
	}
	if err != nil {
		return Account{}, errcode.Wrap(errcode.Internal, "read the account failed", err)
	}
	if err = json.Unmarshal([]byte(usage), &a.Usage); err != nil {
		return Account{}, errcode.Wrap(errcode.Internal, "decode the account usage failed", err)
	}
	if len(a.Usage) == 0 {
		a.Usage = nil
	}
	a.Provider, a.Kind, a.SignedIn = Provider(provider), Kind(kind), signedIn
	a.ExhaustedUntil, a.CooldownUntil = fromMillis(exhausted), fromMillis(cooldown)
	a.ObservedAt, a.CreatedAt = fromMillis(observed), fromMillis(created)
	return a, nil
}

// requireRow turns a statement that touched no row into NotFound.
func requireRow(res sql.Result, err error, what string) error {
	if err != nil {
		return errcode.Wrap(errcode.Internal, what+" failed", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errcode.Wrap(errcode.Internal, what+" failed", err)
	}
	if n == 0 {
		return errcode.New(errcode.NotFound, "account not found")
	}
	return nil
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
