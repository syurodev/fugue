package storage

import (
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

const testNoopSQL = "SELECT 1;"

func TestMigrateIsIdempotentAndContiguous(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))

	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("Migrations returned empty list")
	}
	for i, m := range migrations {
		if m.Version != i+1 {
			t.Fatalf("migration[%d].Version = %d, want %d", i, m.Version, i+1)
		}
	}

	db, err := Open(ctx, DBPath(t.TempDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeTestDB(t, db)

	if migErr := Migrate(ctx, db, clk); migErr != nil {
		t.Fatalf("Migrate #1: %v", migErr)
	}
	if migErr := Migrate(ctx, db, clk); migErr != nil {
		t.Fatalf("Migrate #2 (idempotent): %v", migErr)
	}

	var count int
	if scanErr := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); scanErr != nil {
		t.Fatalf("count schema_migrations: %v", scanErr)
	}
	if count != len(migrations) {
		t.Fatalf("schema_migrations count = %d, want %d", count, len(migrations))
	}
}

func TestValidateMigrationsRejectsInvalidLists(t *testing.T) {
	t.Parallel()

	invalidCases := []struct {
		name string
		list []Migration
	}{
		{name: "empty list", list: nil},
		{
			name: "duplicate version",
			list: []Migration{
				{Version: 1, Name: "a", SQL: testNoopSQL},
				{Version: 1, Name: "b", SQL: testNoopSQL},
			},
		},
		{
			name: "non-contiguous version",
			list: []Migration{
				{Version: 1, Name: "a", SQL: testNoopSQL},
				{Version: 3, Name: "c", SQL: testNoopSQL},
			},
		},
		{
			name: "empty name",
			list: []Migration{
				{Version: 1, Name: "   ", SQL: testNoopSQL},
			},
		},
		{
			name: "empty SQL",
			list: []Migration{
				{Version: 1, Name: "a", SQL: "   "},
			},
		},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := validateMigrations(tc.list); errcode.CodeOf(err) != errcode.InvalidArgument {
				t.Fatalf("validateMigrations(%s) code = %v, want %v", tc.name, errcode.CodeOf(err), errcode.InvalidArgument)
			}
		})
	}
}

func TestRunMigrationsRejectsInvalidAndMismatchedMigrations(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))

	db, err := Open(ctx, DBPath(t.TempDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeTestDB(t, db)

	valid, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}

	if runErr := RunMigrations(ctx, nil, clk, valid); errcode.CodeOf(runErr) != errcode.InvalidArgument {
		t.Fatalf("RunMigrations(nil db) code = %v, want %v", errcode.CodeOf(runErr), errcode.InvalidArgument)
	}
	if runErr := RunMigrations(ctx, db, nil, valid); errcode.CodeOf(runErr) != errcode.InvalidArgument {
		t.Fatalf("RunMigrations(nil clk) code = %v, want %v", errcode.CodeOf(runErr), errcode.InvalidArgument)
	}
	if runErr := RunMigrations(ctx, db, clk, valid); runErr != nil {
		t.Fatalf("RunMigrations(valid): %v", runErr)
	}

	mismatched := []Migration{{Version: 1, Name: "different_name", SQL: valid[0].SQL}}
	if runErr := RunMigrations(ctx, db, clk, mismatched); errcode.CodeOf(runErr) != errcode.FailedPrecondition {
		t.Fatalf("RunMigrations(mismatched name) code = %v, want %v", errcode.CodeOf(runErr), errcode.FailedPrecondition)
	}

	next := len(valid) + 1
	withBadNext := append(slices.Clone(valid), Migration{Version: next, Name: "broken_sql", SQL: "THIS IS NOT VALID SQL;"})
	if runErr := RunMigrations(ctx, db, clk, withBadNext); errcode.CodeOf(runErr) != errcode.Internal {
		t.Fatalf("RunMigrations(broken SQL) code = %v, want %v", errcode.CodeOf(runErr), errcode.Internal)
	}

	// Simulate a database migrated by a newer binary.
	if _, execErr := db.ExecContext(
		ctx,
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, 'future', '2026-11-02T10:00:00Z')", next,
	); execErr != nil {
		t.Fatalf("insert future migration: %v", execErr)
	}
	if runErr := RunMigrations(ctx, db, clk, valid); errcode.CodeOf(runErr) != errcode.FailedPrecondition {
		t.Fatalf("RunMigrations(ahead of binary) code = %v, want %v", errcode.CodeOf(runErr), errcode.FailedPrecondition)
	}
}

func TestLoadMigrationsParsesAndValidatesFilenames(t *testing.T) {
	t.Parallel()

	mixedFS := fstest.MapFS{
		"migrations/README.txt":    &fstest.MapFile{Data: []byte("ignore")},
		"migrations/0001_init.sql": &fstest.MapFile{Data: []byte(testNoopSQL)},
	}
	loaded, loadErr := loadMigrations(mixedFS)
	if loadErr != nil || len(loaded) != 1 {
		t.Fatalf("loadMigrations(mixedFS) = %v, %v, want 1 migration", loaded, loadErr)
	}

	badFS := []struct {
		name string
		fsys fstest.MapFS
	}{
		{
			name: "missing underscore",
			fsys: fstest.MapFS{
				"migrations/0001.sql": &fstest.MapFile{Data: []byte(testNoopSQL)},
			},
		},
		{
			name: "non-positive version",
			fsys: fstest.MapFS{
				"migrations/0000_zero.sql": &fstest.MapFile{Data: []byte(testNoopSQL)},
			},
		},
	}
	for _, tc := range badFS {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := loadMigrations(tc.fsys); errcode.CodeOf(err) != errcode.InvalidArgument {
				t.Fatalf("loadMigrations(%s) code = %v, want %v", tc.name, errcode.CodeOf(err), errcode.InvalidArgument)
			}
		})
	}
}
