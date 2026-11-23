package main

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/pkg/client"
)

// A-E05 (activation §3, §5): a host start refused for a data directory
// another process holds, or for a schema this Fugue cannot serve, fails typed
// (`failed_precondition`) with the public exit code a launching app maps onto
// its failure UX, and touches nothing.

// TestServeRefusesAHeldDataDirectoryTypedWithTheInUseExit pins the held
// data directory row: with the lock held elsewhere and no live socket, serve
// exits with the data-directory-in-use code and the typed lock error.
func TestServeRefusesAHeldDataDirectoryTypedWithTheInUseExit(t *testing.T) {
	t.Parallel()
	dir := serveDir(t)
	lock, err := storage.AcquireLock(dir)
	if err != nil {
		t.Fatalf("hold the data directory: %v", err)
	}
	defer func() {
		if closeErr := lock.Release(); closeErr != nil {
			t.Errorf("release the data directory: %v", closeErr)
		}
	}()

	code, serveErr := serve(t.Context(), io.Discard, config.Config{DataDir: dir}, testVersion, serveOpts{sigs: make(chan os.Signal, 1)})
	if code != client.ExitDataDirInUse {
		t.Fatalf("serve on a held data directory exits %d, want %d", code, client.ExitDataDirInUse)
	}
	if !errors.Is(serveErr, storage.ErrDataDirLocked) {
		t.Fatalf("serve fails with %v, want the typed data-directory-locked error", serveErr)
	}
	if errcode.CodeOf(serveErr) != errcode.FailedPrecondition {
		t.Fatalf("serve fails with code %q, want %q", errcode.CodeOf(serveErr), errcode.FailedPrecondition)
	}
}

// TestServeRefusesANewerSchemaTypedWithTheUnsupportedExit pins the
// unsupported schema row: a data directory recording a migration this binary
// does not know makes serve exit with the unsupported-store code and the
// typed schema error,.
func TestServeRefusesANewerSchemaTypedWithTheUnsupportedExit(t *testing.T) {
	t.Parallel()
	dir := serveDir(t)
	seedNewerSchema(t, t.Context(), dir)

	code, serveErr := serve(t.Context(), io.Discard, config.Config{DataDir: dir}, testVersion, serveOpts{sigs: make(chan os.Signal, 1)})
	if code != client.ExitStoreUnsupported {
		t.Fatalf("serve on a newer schema exits %d, want %d", code, client.ExitStoreUnsupported)
	}
	if !errors.Is(serveErr, storage.ErrSchemaUnsupported) {
		t.Fatalf("serve fails with %v, want the typed unsupported-schema error", serveErr)
	}
	if errcode.CodeOf(serveErr) != errcode.FailedPrecondition {
		t.Fatalf("serve fails with code %q, want %q", errcode.CodeOf(serveErr), errcode.FailedPrecondition)
	}
}

// seedNewerSchema applies one migration past the binary's own, the shape a
// data directory written by a newer Fugue has.
func seedNewerSchema(t *testing.T, ctx context.Context, dir string) {
	t.Helper()
	migrations, err := storage.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	migrations = append(migrations, storage.Migration{
		Version: len(migrations) + 1, Name: "from_a_newer_fugue", SQL: "SELECT 1;",
	})
	db, err := storage.OpenDataDir(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close the database: %v", closeErr)
		}
	}()
	if err := storage.RunMigrations(ctx, db, clock.NewFake(time.Unix(0, 0)), migrations); err != nil {
		t.Fatalf("record the newer migration: %v", err)
	}
}
