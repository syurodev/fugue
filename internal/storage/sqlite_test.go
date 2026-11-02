package storage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

var errChmodInjected = errors.New("chmod failed")

func TestOpenRestrictsPermissionsAndSetsDurabilityPragmas(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "Fugue")

	db, err := OpenDataDir(ctx, dataDir)
	if err != nil {
		t.Fatalf("OpenDataDir: %v", err)
	}
	closeTestDB(t, db)

	path := DBPath(dataDir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if runtime.GOOS != "windows" {
		if got, want := info.Mode().Perm(), os.FileMode(dbFilePerm); got != want {
			t.Fatalf("database permissions = %o, want %o", got, want)
		}
	}
	if err := VerifyPragmas(ctx, db); err != nil {
		t.Fatalf("VerifyPragmas: %v", err)
	}
}

func TestSQLiteDoesNotCreateUnprotectedWALSidecars(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := DBPath(t.TempDir())

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeTestDB(t, db)

	if migErr := Migrate(ctx, db, clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))); migErr != nil {
		t.Fatalf("Migrate: %v", migErr)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, statErr := os.Stat(path + suffix); !os.IsNotExist(statErr) {
			t.Fatalf("SQLite sidecar %q exists or stat failed: %v", suffix, statErr)
		}
	}
}

func TestVerifyPragmasRejectsWrongJournalModeSynchronousAndForeignKeys(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	if err := VerifyPragmas(ctx, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("VerifyPragmas(nil) code = %v, want %v", errcode.CodeOf(err), errcode.InvalidArgument)
	}
	if _, err := Open(ctx, "   "); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("Open(blank) code = %v, want %v", errcode.CodeOf(err), errcode.InvalidArgument)
	}
	if _, err := OpenDataDir(ctx, "   "); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("OpenDataDir(blank) code = %v, want %v", errcode.CodeOf(err), errcode.InvalidArgument)
	}

	cases := []struct {
		name   string
		pragma string
	}{
		{name: "wrong journal_mode", pragma: "PRAGMA journal_mode=MEMORY"},
		{name: "wrong synchronous", pragma: "PRAGMA synchronous=NORMAL"},
		{name: "wrong foreign_keys", pragma: "PRAGMA foreign_keys=OFF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, err := Open(ctx, DBPath(t.TempDir()))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			closeTestDB(t, db)

			if _, execErr := db.ExecContext(ctx, tc.pragma); execErr != nil {
				t.Fatalf("exec %q: %v", tc.pragma, execErr)
			}
			if verifyErr := VerifyPragmas(ctx, db); errcode.CodeOf(verifyErr) != errcode.FailedPrecondition {
				t.Fatalf("VerifyPragmas(%s) code = %v, want %v", tc.name, errcode.CodeOf(verifyErr), errcode.FailedPrecondition)
			}
		})
	}
}

func TestOpenClosesDatabaseWhenRestrictingPermissionsFails(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := DBPath(t.TempDir())

	_, err := openWith(ctx, path, func(string, os.FileMode) error {
		return errChmodInjected
	})
	if !errors.Is(err, errChmodInjected) || errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("openWith error = %v (code %v), want %v (%v)", err, errcode.CodeOf(err), errChmodInjected, errcode.Internal)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen after chmod failure: %v", err)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close reopened database: %v", closeErr)
	}
}
