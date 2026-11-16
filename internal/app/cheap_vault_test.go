package app

// A host fixture that runs vault.Setup pays three Argon2id derivations at the
// production cost (64 MiB, 3 passes), which under the race detector dominates
// the package's run time. These helpers start the host on a data directory
// whose keyring was made with the lowest cost the format allows, so the
// fixture still exercises a real locked-then-unlocked vault.

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// cheapVaultDir returns a fresh data directory that already holds the
// minimum-cost keyring for testMasterPW, so the host starts with a locked
// vault.
func cheapVaultDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	seedCheapVaultDir(t, dir)
	return dir
}

// seedCheapVaultDir saves the minimum-cost keyring into the data directory's
// store and closes the store so StartHost can take the directory lock.
func seedCheapVaultDir(t *testing.T, dir string) {
	t.Helper()
	clk := clock.NewFake(time.Unix(0, 0))
	store, err := storage.OpenStore(t.Context(), config.Config{DataDir: dir}, clk, ids.NewSequence("cheap-vault"))
	if err != nil {
		t.Fatal(err)
	}
	saveCheapKeyring(t, vault.NewSQLKeyringStore(store.DB()))
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
}

// saveCheapKeyring stores the minimum-cost keyring for testMasterPW.
func saveCheapKeyring(t *testing.T, keyrings vault.KeyringStore) {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "vault", "testdata", "keyring-cheap.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ring crypto.Keyring
	if err = json.Unmarshal(fixture, &ring); err != nil {
		t.Fatal(err)
	}
	if err = keyrings.Save(t.Context(), &ring); err != nil {
		t.Fatal(err)
	}
}

// unlockCheapVault unlocks the host's vault made by cheapVaultDir.
func unlockCheapVault(t *testing.T, hp *HostProcess) {
	t.Helper()
	if err := hp.Vault().Unlock(t.Context(), redact.NewSecret(testMasterPW)); err != nil {
		t.Fatal(err)
	}
}
