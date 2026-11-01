package vault

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// The cheap keyring in testdata was produced by the dev-kit crypto code at the
// lowest accepted KDF cost, so unlocking stays fast. It wraps the data key whose
// hex is cheapDEK under cheapPassword and cheapRecovery.
const (
	longPassword  = "a long master password"
	cheapPassword = "master-pw"
	cheapRecovery = "recovery-secret"
	cheapDEK      = "641d3674a31222496001aed616f727dc434f785d851100bc4071498719b802c7"
)

func cheapKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	raw, err := os.ReadFile("testdata/keyring-cheap.json")
	if err != nil {
		t.Fatal(err)
	}
	var keyring crypto.Keyring
	if err := json.Unmarshal(raw, &keyring); err != nil {
		t.Fatal(err)
	}
	return &keyring
}

const (
	testRecordType = "exec.session_key"
	testRecordID   = "sess-1"
)

func secret(s string) redact.Secret { return redact.NewSecret(s) }

// newService returns a Service over a store that already holds the cheap
// keyring, so it starts locked like every process start does.
func newService(t *testing.T) *Service {
	t.Helper()
	s, err := NewService(t.Context(), NewMemoryKeyringStore(cheapKeyring(t)), ids.NewSequence("inc-"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func unlocked(t *testing.T) *Service {
	t.Helper()
	s := newService(t)
	if err := s.Unlock(t.Context(), secret(cheapPassword)); err != nil {
		t.Fatal(err)
	}
	return s
}

func wantCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", want)
	}
	if got := errcode.CodeOf(err); got != want {
		t.Fatalf("code = %q, want %q (%v)", got, want, err)
	}
}

// flakyStore fails the operations named by its fields.
type flakyStore struct {
	inner                       *MemoryKeyringStore
	existsErr, loadErr, saveErr error
	mu                          sync.Mutex
	saves                       int
}

func (f *flakyStore) Exists(ctx context.Context) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.inner.Exists(ctx)
}

func (f *flakyStore) Load(ctx context.Context) (*crypto.Keyring, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.inner.Load(ctx)
}

func (f *flakyStore) Save(ctx context.Context, keyring *crypto.Keyring) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves++
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.inner.Save(ctx, keyring)
}
