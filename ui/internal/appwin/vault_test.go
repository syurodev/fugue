package appwin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

const (
	testUIVaultInput = "appwin-secret-pass-123"
	testUIVaultInc   = "inc-appwin-vault"
)

var errVaultTestScripted = errors.New("scripted vault error")

type fakeAppVaultClient struct {
	mu          chan struct{}
	lastPass    string
	lastRec     string
	setupCalls  int
	unlockCalls int
	lockCalls   int
	status      client.VaultStatus
	recovery    []byte
	err         error
}

func newFakeAppVaultClient() *fakeAppVaultClient {
	return &fakeAppVaultClient{mu: make(chan struct{}, 1)}
}

func (f *fakeAppVaultClient) VaultStatus(context.Context) (client.VaultStatus, error) {
	return f.status, f.err
}

func (f *fakeAppVaultClient) SetupVault(_ context.Context, passphrase []byte) (client.VaultSetupResult, error) {
	f.mu <- struct{}{}
	f.setupCalls++
	f.lastPass = string(passphrase)
	f.status.Revision++
	st := f.status
	<-f.mu
	if f.err != nil {
		return client.VaultSetupResult{}, f.err
	}
	return client.VaultSetupResult{Status: st, RecoveryKey: bytes.Clone(f.recovery)}, nil
}

func (f *fakeAppVaultClient) UnlockVault(_ context.Context, passphrase []byte) (client.VaultStatus, error) {
	f.mu <- struct{}{}
	f.unlockCalls++
	f.lastPass = string(passphrase)
	f.status.Revision++
	st := f.status
	<-f.mu
	return st, f.err
}

func (f *fakeAppVaultClient) UnlockVaultWithRecovery(_ context.Context, key []byte) (client.VaultStatus, error) {
	f.mu <- struct{}{}
	f.unlockCalls++
	f.lastRec = string(key)
	f.status.Revision++
	st := f.status
	<-f.mu
	return st, f.err
}

func (f *fakeAppVaultClient) LockVault(context.Context) (client.VaultStatus, error) {
	f.mu <- struct{}{}
	f.lockCalls++
	f.status.Revision++
	st := f.status
	<-f.mu
	return st, f.err
}

func newVaultTestScreens(t *testing.T, vc vault.Client) *screens {
	t.Helper()
	fake := &fakeFetcher{}
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, fake, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	w := vault.NewWorker(func() vault.Client { return vc })
	w.Start(t.Context())
	t.Cleanup(w.Stop)
	sc.vaultWorker = w
	return sc
}

func mustVaultSnapshot(t *testing.T, st vault.Status) hostconn.Update {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return hostconn.Update{Topic: topicVault, Snapshot: &client.Snapshot{State: b}}
}

func awaitAppVaultResult(t *testing.T, sc *screens) vault.Result {
	t.Helper()
	select {
	case res := <-sc.vaultWorker.Results():
		return res
	case <-t.Context().Done():
		t.Fatalf("context ended waiting for vault result: %v", t.Context().Err())
		return vault.Result{}
	}
}

func TestVaultGateMasksAndClearsFieldsAcrossSetupAndUnlock(t *testing.T) {
	t.Parallel()
	vc := newFakeAppVaultClient()
	vc.status = client.VaultStatus{State: client.VaultStateUnlocked, Revision: 1, Incarnation: testUIVaultInc}
	vc.recovery = []byte("rec-one-time-key")
	sc := newVaultTestScreens(t, vc)
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateNotConfigured, Incarnation: testUIVaultInc})

	assertSetupValidationClearsFields(t, sc)
	assertSetupRecoveryAndUnlock(t, sc, vc)
}

func assertSetupValidationClearsFields(t *testing.T, sc *screens) {
	t.Helper()
	sc.vaultGate.pass.SetText("short")
	sc.vaultGate.confirm.SetText("short")
	sc.vaultGate.submitSetup(sc)
	if sc.vaultGate.lastErr == "" || sc.vaultGate.pass.Text() != "" || sc.vaultGate.confirm.Text() != "" {
		t.Fatalf("short setup: err=%q pass=%q confirm=%q", sc.vaultGate.lastErr, sc.vaultGate.pass.Text(), sc.vaultGate.confirm.Text())
	}

	sc.vaultGate.pass.SetText(testUIVaultInput)
	sc.vaultGate.confirm.SetText("different-input")
	sc.vaultGate.submitSetup(sc)
	if sc.vaultGate.lastErr == "" || sc.vaultGate.pass.Text() != "" || sc.vaultGate.confirm.Text() != "" {
		t.Fatalf("mismatch setup: err=%q pass=%q confirm=%q", sc.vaultGate.lastErr, sc.vaultGate.pass.Text(), sc.vaultGate.confirm.Text())
	}
}

func assertSetupRecoveryAndUnlock(t *testing.T, sc *screens, vc *fakeAppVaultClient) {
	t.Helper()
	sc.vaultGate.pass.SetText(testUIVaultInput)
	sc.vaultGate.confirm.SetText(testUIVaultInput)
	sc.vaultGate.submitSetup(sc)
	if sc.vaultGate.pass.Text() != "" || sc.vaultGate.confirm.Text() != "" {
		t.Fatal("submitSetup left text in TextFieldState")
	}

	sc.applyVaultResults([]vault.Result{awaitAppVaultResult(t, sc)})
	if !sc.vaultGate.active(sc.vault) || string(sc.vaultGate.recovery) != "rec-one-time-key" {
		t.Fatalf("after setup: active=%v recovery=%q", sc.vaultGate.active(sc.vault), sc.vaultGate.recovery)
	}
	recBacking := sc.vaultGate.recovery
	sc.vaultGate.clearRecovery()
	if sc.vaultGate.active(sc.vault) || !bytes.Equal(recBacking, make([]byte, len(recBacking))) {
		t.Fatal("clearRecovery did not clear recovery key and dismiss gate")
	}

	sc.applyVaultDelivery(mustVaultSnapshot(t, vault.Status{State: client.VaultStateLocked, Revision: 2, Incarnation: testUIVaultInc}))
	if !sc.vaultGate.active(sc.vault) {
		t.Fatal("vaultGate did not activate on locked snapshot")
	}

	sc.vaultGate.pass.SetText(testUIVaultInput)
	sc.vaultGate.submitUnlock(sc)
	if sc.vaultGate.pass.Text() != "" {
		t.Fatal("submitUnlock left text in TextFieldState")
	}
	sc.applyVaultResults([]vault.Result{awaitAppVaultResult(t, sc)})
	if sc.vaultGate.active(sc.vault) || vc.lastPass != testUIVaultInput {
		t.Fatalf("after unlock: active=%v lastPass=%q", sc.vaultGate.active(sc.vault), vc.lastPass)
	}
}

func TestVaultGateErrorMappingAndTopicUpdate(t *testing.T) {
	t.Parallel()
	if got := vaultErrorMessage("vault.rate_limited", 12, errVaultTestScripted); got != "Too many attempts. Wait 12s before retrying." {
		t.Fatalf("rate limit msg = %q", got)
	}
	if got := vaultErrorMessage("vault.invalid_passphrase", 0, errVaultTestScripted); got == errVaultTestScripted.Error() {
		t.Fatalf("invalid passphrase should use safe generic message, got %q", got)
	}
	vc := newFakeAppVaultClient()
	vc.status = client.VaultStatus{State: client.VaultStateUnlocked, Revision: 3, Incarnation: testUIVaultInc}
	sc := newVaultTestScreens(t, vc)
	sc.applyVaultDelivery(mustVaultSnapshot(t, vault.Status{State: client.VaultStateLocked, Revision: 1, Incarnation: testUIVaultInc}))
	sc.vaultGate.recoveryMode = true
	sc.vaultGate.pass.SetText("my-recovery-key")
	sc.vaultGate.submitUnlock(sc)
	sc.applyVaultResults([]vault.Result{awaitAppVaultResult(t, sc)})
	if sc.vaultGate.active(sc.vault) || vc.lastRec != "my-recovery-key" {
		t.Fatalf("recovery unlock: active=%v lastRec=%q", sc.vaultGate.active(sc.vault), vc.lastRec)
	}
}
