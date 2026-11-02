package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// HostIncarnation is the durable host authority record in exec_host
// (storage-security §3, §9; event-storage-recovery §3).
type HostIncarnation struct {
	LockGeneration uint64
	IncarnationID  string
	AcquiredAt     time.Time
}

// AcquireHostIncarnation records a new monotonic host incarnation in exec_host.
// It fails closed with errcode.FailedPrecondition when lock is nil or no longer
// held by this process.
func AcquireHostIncarnation(
	ctx context.Context,
	db *sql.DB,
	lock *DirLock,
	clk clock.Clock,
	idGen ids.Generator,
) (HostIncarnation, error) {
	if err := validateIncarnationArgs(db, lock, clk, idGen); err != nil {
		return HostIncarnation{}, err
	}
	incarnationID, err := idGen.NewID()
	if err != nil {
		return HostIncarnation{}, errcode.Wrap(errcode.Internal, "generate host incarnation id", err)
	}
	acquiredAt := clk.Now().UTC()
	return upsertHostIncarnation(ctx, db, incarnationID, acquiredAt)
}

func validateIncarnationArgs(db *sql.DB, lock *DirLock, clk clock.Clock, idGen ids.Generator) error {
	if db == nil {
		return errcode.New(errcode.InvalidArgument, "database handle is nil")
	}
	if clk == nil {
		return errcode.New(errcode.InvalidArgument, "clock is nil")
	}
	if idGen == nil {
		return errcode.New(errcode.InvalidArgument, "id generator is nil")
	}
	if lock == nil || !lock.Held() {
		return errcode.New(errcode.FailedPrecondition, "data-directory lock is not held")
	}
	return nil
}

func upsertHostIncarnation(
	ctx context.Context,
	db *sql.DB,
	incarnationID string,
	acquiredAt time.Time,
) (HostIncarnation, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return HostIncarnation{}, errcode.Wrap(errcode.Internal, "begin exec_host transaction", err)
	}
	defer rollbackQuietly(tx)

	var currentGen sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		"SELECT lock_generation FROM exec_host WHERE singleton = 1",
	).Scan(&currentGen); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return HostIncarnation{}, errcode.Wrap(errcode.Internal, "read current exec_host generation", err)
	}
	var nextGen uint64 = 1
	if currentGen.Valid && currentGen.Int64 > 0 {
		nextGen = uint64(currentGen.Int64) + 1
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO exec_host (singleton, lock_generation, incarnation_id, acquired_at)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT(singleton) DO UPDATE SET
		   lock_generation = excluded.lock_generation,
		   incarnation_id = excluded.incarnation_id,
		   acquired_at = excluded.acquired_at`,
		nextGen,
		incarnationID,
		acquiredAt.Format(time.RFC3339Nano),
	); err != nil {
		return HostIncarnation{}, errcode.Wrap(errcode.Internal, "upsert exec_host incarnation", err)
	}
	if err := tx.Commit(); err != nil {
		return HostIncarnation{}, errcode.Wrap(errcode.Internal, "commit exec_host incarnation", err)
	}
	return HostIncarnation{
		LockGeneration: nextGen,
		IncarnationID:  incarnationID,
		AcquiredAt:     acquiredAt,
	}, nil
}

// CurrentHostIncarnation reads the singleton row from exec_host. It returns
// errcode.NotFound when no host incarnation has been recorded yet.
func CurrentHostIncarnation(ctx context.Context, db *sql.DB) (HostIncarnation, error) {
	if db == nil {
		return HostIncarnation{}, errcode.New(errcode.InvalidArgument, "database handle is nil")
	}
	var (
		gen      uint64
		id       string
		acquired string
	)
	err := db.QueryRowContext(
		ctx,
		"SELECT lock_generation, incarnation_id, acquired_at FROM exec_host WHERE singleton = 1",
	).Scan(&gen, &id, &acquired)
	if errors.Is(err, sql.ErrNoRows) {
		return HostIncarnation{}, errcode.New(errcode.NotFound, "exec_host has no recorded incarnation")
	}
	if err != nil {
		return HostIncarnation{}, errcode.Wrap(errcode.Internal, "read exec_host incarnation", err)
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, acquired)
	if err != nil {
		return HostIncarnation{}, errcode.Wrap(
			errcode.Internal,
			fmt.Sprintf("parse exec_host acquired_at %q", acquired),
			err,
		)
	}
	return HostIncarnation{
		LockGeneration: gen,
		IncarnationID:  id,
		AcquiredAt:     parsedTime,
	}, nil
}

// Store bundles the exclusive data-directory lock, the migrated SQLite handle
// and the host incarnation for a single host process.
type Store struct {
	lock        *DirLock
	db          *sql.DB
	incarnation HostIncarnation
}

// OpenStore acquires <cfg.DataDir>/fugue.lock, opens <cfg.DataDir>/fugue.db,
// runs migrations and records a new host incarnation in exec_host.
func OpenStore(
	ctx context.Context,
	cfg config.Config,
	clk clock.Clock,
	idGen ids.Generator,
) (*Store, error) {
	return OpenStoreWith(ctx, cfg, clk, idGen, nil)
}

// OpenStoreWith is OpenStore with optional lock overrides.
func OpenStoreWith(
	ctx context.Context,
	cfg config.Config,
	clk clock.Clock,
	idGen ids.Generator,
	lockOpts *LockOptions,
) (*Store, error) {
	lock, err := AcquireLockWith(cfg.DataDir, lockOpts)
	if err != nil {
		return nil, err
	}
	db, err := Open(ctx, DBPath(lock.DataDir()))
	if err != nil {
		releaseQuietly(lock)
		return nil, err
	}
	if migErr := Migrate(ctx, db, clk); migErr != nil {
		closeQuietly(db)
		releaseQuietly(lock)
		return nil, migErr
	}
	incarnation, err := AcquireHostIncarnation(ctx, db, lock, clk, idGen)
	if err != nil {
		closeQuietly(db)
		releaseQuietly(lock)
		return nil, err
	}
	return &Store{
		lock:        lock,
		db:          db,
		incarnation: incarnation,
	}, nil
}

func releaseQuietly(lock *DirLock) {
	if lock == nil {
		return
	}
	if err := lock.Release(); err != nil {
		return
	}
}

// DB returns the underlying single-connection SQLite handle.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Lock returns the active data-directory lock.
func (s *Store) Lock() *DirLock {
	if s == nil {
		return nil
	}
	return s.lock
}

// Incarnation returns the host incarnation acquired when the store was opened.
func (s *Store) Incarnation() HostIncarnation {
	if s == nil {
		return HostIncarnation{}
	}
	return s.incarnation
}

// DataDir returns the canonical data directory path.
func (s *Store) DataDir() string {
	if s == nil || s.lock == nil {
		return ""
	}
	return s.lock.DataDir()
}

// Close closes the SQLite database handle before releasing the data-directory
// lock (event-storage-recovery §3).
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var dbErr error
	if s.db != nil {
		dbErr = s.db.Close()
		s.db = nil
	}
	lockErr := s.lock.Release()
	if dbErr != nil {
		return errcode.Wrap(errcode.Internal, "close sqlite database", dbErr)
	}
	return lockErr
}
