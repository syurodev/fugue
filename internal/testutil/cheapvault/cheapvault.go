package cheapvault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

// MasterPassword opens the fixture keyring.
const MasterPassword = "master-pw"

// Keyring returns the minimum-cost keyring, read from the vault's golden
// vector so there is exactly one copy of it.
func Keyring(tb testing.TB) *crypto.Keyring {
	tb.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		tb.Fatal("cannot locate the cheap keyring fixture")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "vault", "testdata", "keyring-cheap.json"))
	if err != nil {
		tb.Fatal(err)
	}
	var ring crypto.Keyring
	if err = json.Unmarshal(raw, &ring); err != nil {
		tb.Fatal(err)
	}
	return &ring
}

// Save writes the minimum-cost keyring into store, for a fixture that builds
// its own vault.Service on a database store.
func Save(tb testing.TB, store vault.KeyringStore) {
	tb.Helper()
	if err := store.Save(tb.Context(), Keyring(tb)); err != nil {
		tb.Fatal(err)
	}
}

// Unlock opens svc with MasterPassword.
func Unlock(tb testing.TB, svc *vault.Service) {
	tb.Helper()
	if err := svc.Unlock(tb.Context(), redact.NewSecret(MasterPassword)); err != nil {
		tb.Fatal(err)
	}
}

// Unlocked returns a vault on an in-memory store holding the keyring, already
// unlocked with MasterPassword.
func Unlocked(tb testing.TB) *vault.Service {
	tb.Helper()
	svc, err := vault.NewService(tb.Context(), vault.NewMemoryKeyringStore(Keyring(tb)), ids.NewSequence("cheap-vault-"))
	if err != nil {
		tb.Fatal(err)
	}
	Unlock(tb, svc)
	return svc
}

// DataDir returns a fresh data directory that already holds the keyring, so a
// host started on it comes up with a locked vault that Unlock opens.
func DataDir(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	SeedDataDir(tb, dir)
	return dir
}

// SeedDataDir saves the keyring into the store of an existing data directory
// and closes the store, so a host can take the directory lock afterwards.
func SeedDataDir(tb testing.TB, dir string) {
	tb.Helper()
	clk := clock.NewFake(time.Unix(0, 0))
	store, err := storage.OpenStore(tb.Context(), config.Config{DataDir: dir}, clk, ids.NewSequence("cheap-vault"))
	if err != nil {
		tb.Fatal(err)
	}
	Save(tb, vault.NewSQLKeyringStore(store.DB()))
	if err = store.Close(); err != nil {
		tb.Fatal(err)
	}
}
