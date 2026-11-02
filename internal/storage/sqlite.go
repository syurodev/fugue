package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // Pure-Go SQLite driver registered as "sqlite".

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	// DBFileName is the canonical SQLite database file name inside the data directory (Q-03).
	DBFileName = "fugue.db"

	dataDirPerm       = 0o700
	dbFilePerm        = 0o600
	synchronousFull   = 2
	foreignKeysOn     = 1
	journalModeDelete = "delete"
)

func closeQuietly(c io.Closer) {
	if c == nil {
		return
	}
	if err := c.Close(); err != nil {
		return
	}
}

// DBPath returns the canonical path to fugue.db inside dataDir.
func DBPath(dataDir string) string {
	return filepath.Join(dataDir, DBFileName)
}

// Open opens (creating if needed) the SQLite database at path with a single
// connection, applies the required durability pragmas (journal_mode=DELETE,
// synchronous=FULL, foreign_keys=ON, busy_timeout=5000), restricts the file to
// mode 0600 and verifies the active pragmas.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	return openWith(ctx, path, os.Chmod)
}

// OpenDataDir opens <dataDir>/fugue.db with Open.
func OpenDataDir(ctx context.Context, dataDir string) (*sql.DB, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errcode.New(errcode.InvalidArgument, "data directory is required")
	}
	return Open(ctx, DBPath(dataDir))
}

func openWith(ctx context.Context, path string, chmod func(string, os.FileMode) error) (*sql.DB, error) {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return nil, errcode.New(errcode.InvalidArgument, "database path is required")
	}
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "resolve database path", err)
	}
	if dirErr := os.MkdirAll(filepath.Dir(absPath), dataDirPerm); dirErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "create database directory", dirErr)
	}
	db, err := sql.Open("sqlite", absPath)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "open sqlite database", err)
	}
	// modernc.org/sqlite serializes writes; a single connection guarantees that
	// connection-scoped pragmas apply to every transaction and avoids lock
	// contention within the process.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, execErr := db.ExecContext(
		ctx,
		"PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;",
	); execErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "configure sqlite pragmas", errors.Join(execErr, db.Close()))
	}
	if chmodErr := chmod(absPath, dbFilePerm); chmodErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "restrict sqlite database permissions", errors.Join(chmodErr, db.Close()))
	}
	if verifyErr := VerifyPragmas(ctx, db); verifyErr != nil {
		closeQuietly(db)
		return nil, verifyErr
	}
	return db, nil
}

// VerifyPragmas checks that db has journal_mode=delete, synchronous=FULL (2)
// and foreign_keys=ON (1) per storage-security §2. Any other setting fails
// closed with errcode.FailedPrecondition.
func VerifyPragmas(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errcode.New(errcode.InvalidArgument, "database handle is nil")
	}
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return errcode.Wrap(errcode.Internal, "read sqlite journal_mode", err)
	}
	if !strings.EqualFold(strings.TrimSpace(journalMode), journalModeDelete) {
		return errcode.New(
			errcode.FailedPrecondition,
			fmt.Sprintf("sqlite journal_mode must be %q, got %q", journalModeDelete, journalMode),
		)
	}

	var synchronous int
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return errcode.Wrap(errcode.Internal, "read sqlite synchronous", err)
	}
	if synchronous != synchronousFull {
		return errcode.New(
			errcode.FailedPrecondition,
			fmt.Sprintf("sqlite synchronous must be FULL (%d), got %d", synchronousFull, synchronous),
		)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return errcode.Wrap(errcode.Internal, "read sqlite foreign_keys", err)
	}
	if foreignKeys != foreignKeysOn {
		return errcode.New(
			errcode.FailedPrecondition,
			fmt.Sprintf("sqlite foreign_keys must be ON (%d), got %d", foreignKeysOn, foreignKeys),
		)
	}
	return nil
}
