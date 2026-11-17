package cheapvault

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

// tb is the part of testing.TB the helpers use; a narrow interface lets the
// failure paths be tested with a recording stand-in. After a failure the helper
// returns, so a stand-in that does not stop the goroutine is safe.
type tb interface {
	Helper()
	Fatal(args ...any)
	Context() context.Context
	TempDir() string
}

// MasterPassword opens the fixture keyring.
const MasterPassword = "master-pw"

// Keyring returns the minimum-cost keyring, read from the vault's golden
// vector so there is exactly one copy of it.
func Keyring(t tb) *crypto.Keyring {
	t.Helper()
	// A failed Caller yields an empty path, which readKeyring then reports.
	_, here, _, _ := runtime.Caller(0)
	ring, err := readKeyring(filepath.Join(filepath.Dir(here), "..", "..", "vault", "testdata", "keyring-cheap.json"))
	if err != nil {
		t.Fatal(err)
		return nil
	}
	return ring
}

func readKeyring(path string) (*crypto.Keyring, error) {
	raw, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("read the cheap keyring: %w", err)
	}
	var ring crypto.Keyring
	if err = json.Unmarshal(raw, &ring); err != nil {
		return nil, fmt.Errorf("decode the cheap keyring: %w", err)
	}
	return &ring, nil
}

// Save writes the minimum-cost keyring into store, for a fixture that builds
// its own vault.Service on a database store.
func Save(t tb, store vault.KeyringStore) {
	t.Helper()
	if err := store.Save(t.Context(), Keyring(t)); err != nil {
		t.Fatal(err)
	}
}

// Unlock opens svc with MasterPassword.
func Unlock(t tb, svc *vault.Service) {
	t.Helper()
	if err := svc.Unlock(t.Context(), redact.NewSecret(MasterPassword)); err != nil {
		t.Fatal(err)
	}
}

// Unlocked returns a vault on an in-memory store holding the keyring, already
// unlocked with MasterPassword.
func Unlocked(t tb) *vault.Service {
	t.Helper()
	svc, err := vault.NewService(t.Context(), vault.NewMemoryKeyringStore(Keyring(t)), ids.NewSequence("cheap-vault-"))
	if err != nil {
		t.Fatal(err)
		return nil
	}
	Unlock(t, svc)
	return svc
}

// DataDir returns a fresh data directory that already holds the keyring, so a
// host started on it comes up with a locked vault that Unlock opens.
func DataDir(t tb) string {
	t.Helper()
	dir := t.TempDir()
	SeedDataDir(t, dir)
	return dir
}

// SeedDataDir saves the keyring into the store of an existing data directory
// and closes the store, so a host can take the directory lock afterwards.
func SeedDataDir(t tb, dir string) {
	t.Helper()
	clk := clock.NewFake(time.Unix(0, 0))
	store, err := storage.OpenStore(t.Context(), config.Config{DataDir: dir}, clk, ids.NewSequence("cheap-vault"))
	if err != nil {
		t.Fatal(err)
		return
	}
	Save(t, vault.NewSQLKeyringStore(store.DB()))
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
}
