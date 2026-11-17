package cheapvault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vault"
)

var errStoreDown = errors.New("store down")

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

// recorder stands in for a test and notes the failures a helper reports.
type recorder struct {
	ctx    context.Context
	dir    string
	failed []any
}

func (r *recorder) Helper()                  {}
func (r *recorder) Fatal(args ...any)        { r.failed = append(r.failed, args...) }
func (r *recorder) Context() context.Context { return r.ctx }
func (r *recorder) TempDir() string          { return r.dir }

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	return &recorder{ctx: t.Context(), dir: t.TempDir()}
}

type failingStore struct{ vault.KeyringStore }

func (failingStore) Save(context.Context, *crypto.Keyring) error { return errStoreDown }

func TestUnlockReportsAVaultThatWasNeverSetUp(t *testing.T) {
	t.Parallel()
	svc, err := vault.NewService(t.Context(), vault.NewMemoryKeyringStore(nil), ids.NewSequence("empty-"))
	if err != nil {
		t.Fatal(err)
	}
	rec := newRecorder(t)
	Unlock(rec, svc)
	if len(rec.failed) == 0 {
		t.Fatal("unlocking a vault with no keyring did not fail")
	}
}

func TestSaveReportsAStoreThatRejectsTheKeyring(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t)
	Save(rec, failingStore{})
	if len(rec.failed) == 0 {
		t.Fatal("a failing store did not fail the fixture")
	}
}

func TestSeedDataDirReportsAnUnusableDirectory(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rec := newRecorder(t)
	SeedDataDir(rec, file)
	if len(rec.failed) == 0 {
		t.Fatal("seeding a path that is a file did not fail")
	}
}

func TestReadKeyringReportsAMissingOrMalformedFixture(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"missing": filepath.Join(dir, "none.json"), "malformed": bad} {
		if _, err := readKeyring(path); err == nil {
			t.Errorf("%s fixture: no error", name)
		}
	}
}
