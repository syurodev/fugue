package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/internal/vault"
)

// unlockCheapVault unlocks the vault of a host started on cheapvault.DataDir.
// A host fixture that ran vault.Setup would pay three Argon2id derivations at
// the production cost, which dominates the package's run time under -race.
func unlockCheapVault(t *testing.T, hp *HostProcess) {
	t.Helper()
	cheapvault.Unlock(t, hp.Vault())
}

// appTemplateDir is where TestMain keeps the pre-migrated database templates
// of the test process pid; deriving it from the pid needs no package-level state.
func appTemplateDir(pid int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("fugue-app-template-%d", pid))
}

const (
	migratedTemplateFile = "migrated.db"
	seededTemplateName   = "seeded"
)

// buildTestTemplates migrates a fresh database once for the whole test process
// and saves a second copy with the cheapvault keyring, returning a cleanup for
// goleak.Cleanup. Under -race, running 15 SQLite migrations per test in
// modernc.org/sqlite consumed ~42% of package CPU time.
func buildTestTemplates() (func(int), error) {
	dir := appTemplateDir(os.Getpid())
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("reset app template dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create app template dir: %w", err)
	}
	cleanup := func(int) {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return
		}
	}
	if err := buildMigratedTemplateFile(dir); err != nil {
		cleanup(0)
		return nil, err
	}
	if err := buildSeededTemplateDir(dir); err != nil {
		cleanup(0)
		return nil, err
	}
	if err := buildGitRepoTemplate(dir); err != nil {
		cleanup(0)
		return nil, err
	}
	return cleanup, nil
}

func buildMigratedTemplateFile(dir string) error {
	ctx := context.Background()
	clk := clock.NewFake(time.Unix(0, 0))
	db, err := storage.Open(ctx, filepath.Join(dir, migratedTemplateFile))
	if err != nil {
		return fmt.Errorf("open migrated template: %w", err)
	}
	if migErr := storage.Migrate(ctx, db, clk); migErr != nil {
		return fmt.Errorf("migrate template: %w", errors.Join(migErr, db.Close()))
	}
	if cerr := db.Close(); cerr != nil {
		return fmt.Errorf("close migrated template: %w", cerr)
	}
	return nil
}

func buildSeededTemplateDir(dir string) error {
	seedDir := filepath.Join(dir, seededTemplateName)
	if err := os.MkdirAll(seedDir, 0o700); err != nil {
		return fmt.Errorf("create seeded template dir: %w", err)
	}
	if err := copyTemplateFile(dir, migratedTemplateFile, storage.DBPath(seedDir)); err != nil {
		return err
	}
	ctx := context.Background()
	seedDB, err := storage.Open(ctx, storage.DBPath(seedDir))
	if err != nil {
		return fmt.Errorf("open seeded template: %w", err)
	}
	ring, err := readCheapKeyring()
	if err != nil {
		return fmt.Errorf("read cheap keyring: %w", errors.Join(err, seedDB.Close()))
	}
	if saveErr := vault.NewSQLKeyringStore(seedDB).Save(ctx, ring); saveErr != nil {
		return fmt.Errorf("save cheap keyring: %w", errors.Join(saveErr, seedDB.Close()))
	}
	if cerr := seedDB.Close(); cerr != nil {
		return fmt.Errorf("close seeded template: %w", cerr)
	}
	return nil
}

func copyTemplateFile(srcDir, srcName, dstPath string) error {
	raw, err := fs.ReadFile(os.DirFS(srcDir), srcName)
	if err != nil {
		return fmt.Errorf("read template %s: %w", srcName, err)
	}
	root, err := os.OpenRoot(filepath.Dir(dstPath))
	if err != nil {
		return fmt.Errorf("open destination dir: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			return
		}
	}()
	if writeErr := root.WriteFile(filepath.Base(dstPath), raw, 0o600); writeErr != nil {
		return fmt.Errorf("write template %s: %w", dstPath, writeErr)
	}
	return nil
}

// cheapVaultDataDir returns a fresh data directory seeded from the process-wide
// template built in TestMain.
func cheapVaultDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	seedCheapVaultDataDir(t, dir)
	return dir
}

// seedCheapVaultDataDir copies the pre-migrated seeded database into dir when
// dir has no database yet, or saves the keyring into the existing database.
func seedCheapVaultDataDir(t *testing.T, dir string) {
	t.Helper()
	dstPath := storage.DBPath(dir)
	if _, err := os.Stat(dstPath); err == nil {
		cheapvault.SeedDataDir(t, dir)
		return
	}
	srcDir := filepath.Join(appTemplateDir(os.Getpid()), seededTemplateName)
	if err := copyTemplateFile(srcDir, storage.DBFileName, dstPath); err != nil {
		t.Fatal(err)
	}
}

// copyMigratedTestDB copies the pre-migrated database template to path and
// returns path for storage.Open.
func copyMigratedTestDB(t *testing.T, path string) string {
	t.Helper()
	if err := copyTemplateFile(appTemplateDir(os.Getpid()), migratedTemplateFile, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// seedMigratedDataDir copies the pre-migrated database (no keyring) into dir
// when dir has no database yet, so a host started on it skips the migrations.
func seedMigratedDataDir(t *testing.T, dir string) {
	t.Helper()
	dstPath := storage.DBPath(dir)
	if _, err := os.Stat(dstPath); err == nil {
		return
	}
	copyMigratedTestDB(t, dstPath)
}

func readCheapKeyring() (*crypto.Keyring, error) {
	_, here, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(here), "..", "vault", "testdata")
	raw, err := fs.ReadFile(os.DirFS(dir), "keyring-cheap.json")
	if err != nil {
		return nil, fmt.Errorf("read cheap keyring: %w", err)
	}
	var ring crypto.Keyring
	if unmarshalErr := json.Unmarshal(raw, &ring); unmarshalErr != nil {
		return nil, fmt.Errorf("decode cheap keyring: %w", unmarshalErr)
	}
	return &ring, nil
}
