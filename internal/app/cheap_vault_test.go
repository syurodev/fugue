package app

import (
	"testing"

	"github.com/syurodev/fugue/internal/testutil/cheapvault"
)

// unlockCheapVault unlocks the vault of a host started on cheapvault.DataDir.
// A host fixture that ran vault.Setup would pay three Argon2id derivations at
// the production cost, which dominates the package's run time under -race.
func unlockCheapVault(t *testing.T, hp *HostProcess) {
	t.Helper()
	cheapvault.Unlock(t, hp.Vault())
}
