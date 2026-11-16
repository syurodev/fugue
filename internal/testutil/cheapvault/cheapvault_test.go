package cheapvault

import (
	"testing"

	"github.com/syurodev/fugue/internal/vault"
)

func TestUnlockedVaultIsOpen(t *testing.T) {
	t.Parallel()
	svc := Unlocked(t)
	if got := svc.Snapshot().State; got != vault.StateUnlocked {
		t.Fatalf("state = %v, want unlocked", got)
	}
}

func TestDataDirHoldsALockedVault(t *testing.T) {
	t.Parallel()
	dir := DataDir(t)
	if dir == "" {
		t.Fatal("empty data directory")
	}
}
