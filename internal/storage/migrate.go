package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

// Migration is a single forward-only SQL schema migration.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

func rollbackQuietly(tx *sql.Tx) {
	if tx == nil {
		return
	}
	if err := tx.Rollback(); err != nil {
		return
	}
}

// Migrations returns the built-in schema migrations loaded from
// internal/storage/migrations/*.sql in ascending version order.
func Migrations() ([]Migration, error) {
	return loadMigrations(embeddedMigrations)
}

func loadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "read migrations directory", err)
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		m, parseErr := parseMigrationEntry(fsys, entry.Name())
		if parseErr != nil {
			return nil, parseErr
		}
		migrations = append(migrations, m)
	}
	if err := validateMigrations(migrations); err != nil {
		return nil, err
	}
	return migrations, nil
}

func parseMigrationEntry(fsys fs.FS, fileName string) (Migration, error) {
	stem := strings.TrimSuffix(fileName, ".sql")
	versionPart, namePart, ok := strings.Cut(stem, "_")
	if !ok || versionPart == "" || strings.TrimSpace(namePart) == "" {
		return Migration{}, errcode.New(
			errcode.InvalidArgument,
			fmt.Sprintf("migration file %q must be named <version>_<name>.sql", fileName),
		)
	}
	version, err := strconv.Atoi(versionPart)
	if err != nil || version <= 0 {
		return Migration{}, errcode.New(
			errcode.InvalidArgument,
			fmt.Sprintf("migration file %q has invalid positive version %q", fileName, versionPart),
		)
	}
	raw, err := fs.ReadFile(fsys, "migrations/"+fileName)
	if err != nil {
		return Migration{}, errcode.Wrap(errcode.Internal, "read migration "+fileName, err)
	}
	return Migration{
		Version: version,
		Name:    strings.TrimSpace(namePart),
		SQL:     strings.TrimSpace(string(raw)),
	}, nil
}

func validateMigrations(migrations []Migration) error {
	if len(migrations) == 0 {
		return errcode.New(errcode.InvalidArgument, "migration list is empty")
	}
	slices.SortFunc(migrations, func(a, b Migration) int {
		return a.Version - b.Version
	})
	for i, m := range migrations {
		wantVersion := i + 1
		if i > 0 && migrations[i-1].Version == m.Version {
			return errcode.New(
				errcode.InvalidArgument,
				fmt.Sprintf("duplicate migration version %d", m.Version),
			)
		}
		if m.Version != wantVersion {
			return errcode.New(
				errcode.InvalidArgument,
				fmt.Sprintf("migrations must be contiguous starting at 1: got version %d at index %d", m.Version, i),
			)
		}
		if strings.TrimSpace(m.Name) == "" {
			return errcode.New(
				errcode.InvalidArgument,
				fmt.Sprintf("migration %d has an empty name", m.Version),
			)
		}
		if strings.TrimSpace(m.SQL) == "" {
			return errcode.New(
				errcode.InvalidArgument,
				fmt.Sprintf("migration %d (%s) has empty SQL", m.Version, m.Name),
			)
		}
	}
	return nil
}

// Migrate applies all built-in migrations to db in ascending version order.
func Migrate(ctx context.Context, db *sql.DB, clk clock.Clock) error {
	migrations, err := Migrations()
	if err != nil {
		return err
	}
	return RunMigrations(ctx, db, clk, migrations)
}

// RunMigrations validates migrations, ensures the schema_migrations table
// exists, verifies already-applied versions and applies any pending migrations.
func RunMigrations(ctx context.Context, db *sql.DB, clk clock.Clock, migrations []Migration) error {
	if db == nil {
		return errcode.New(errcode.InvalidArgument, "database handle is nil")
	}
	if clk == nil {
		return errcode.New(errcode.InvalidArgument, "clock is nil")
	}
	copied := slices.Clone(migrations)
	if err := validateMigrations(copied); err != nil {
		return err
	}
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return err
	}
	applied, err := readAppliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	if len(applied) > len(copied) {
		return errcode.New(
			errcode.FailedPrecondition,
			fmt.Sprintf("database has %d applied migrations, binary only knows %d", len(applied), len(copied)),
		)
	}
	for i, existing := range applied {
		expected := copied[i]
		if existing.Version != expected.Version || existing.Name != expected.Name {
			return errcode.New(
				errcode.FailedPrecondition,
				fmt.Sprintf(
					"applied migration %d (%s) does not match expected %d (%s)",
					existing.Version, existing.Name, expected.Version, expected.Name,
				),
			)
		}
	}
	for _, m := range copied[len(applied):] {
		if err := applyMigration(ctx, db, clk, m); err != nil {
			return err
		}
	}
	return nil
}

// ensureMigrationsTable executes the schema_migrations DDL from the embedded
// 0001_initial.sql file so table creation stays inside migrations/*.sql.
func ensureMigrationsTable(ctx context.Context, db *sql.DB) error {
	raw, err := fs.ReadFile(embeddedMigrations, "migrations/0001_initial.sql")
	if err != nil {
		return errcode.Wrap(errcode.Internal, "read bootstrap migration", err)
	}
	stmt, _, ok := strings.Cut(string(raw), ";")
	if !ok {
		return errcode.New(errcode.Internal, "bootstrap migration is missing statement terminator")
	}
	if _, err := db.ExecContext(ctx, stmt+";"); err != nil {
		return errcode.Wrap(errcode.Internal, "ensure schema_migrations table", err)
	}
	return nil
}

func readAppliedMigrations(ctx context.Context, db *sql.DB) ([]Migration, error) {
	rows, err := db.QueryContext(ctx, "SELECT version, name FROM schema_migrations ORDER BY version ASC")
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "query schema_migrations", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			return
		}
	}()

	var out []Migration
	for rows.Next() {
		var m Migration
		if err := rows.Scan(&m.Version, &m.Name); err != nil {
			return nil, errcode.Wrap(errcode.Internal, "scan schema_migrations", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.Internal, "iterate schema_migrations", err)
	}
	return out, nil
}

func applyMigration(ctx context.Context, db *sql.DB, clk clock.Clock, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errcode.Wrap(errcode.Internal, fmt.Sprintf("begin migration %d (%s)", m.Version, m.Name), err)
	}
	defer rollbackQuietly(tx)

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return errcode.Wrap(errcode.Internal, fmt.Sprintf("execute migration %d (%s)", m.Version, m.Name), err)
	}
	appliedAt := clk.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(
		ctx,
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
		m.Version, m.Name, appliedAt,
	); err != nil {
		return errcode.Wrap(errcode.Internal, fmt.Sprintf("record migration %d (%s)", m.Version, m.Name), err)
	}
	if err := tx.Commit(); err != nil {
		return errcode.Wrap(errcode.Internal, fmt.Sprintf("commit migration %d (%s)", m.Version, m.Name), err)
	}
	return nil
}
