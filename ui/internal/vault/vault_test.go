package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/syurodev/fugue/pkg/client"
)

const (
	testVaultInput    = "ui-vault-passphrase-123"
	testVaultRecInput = "ui-recovery-secret-456"
	testVaultInc      = "inc-vault-ui"
)

var (
	errUnexpectedPass = errors.New("unexpected input")
	errUnexpectedRec  = errors.New("unexpected recovery")
)

type fakeVaultClient struct {
	mu      chan struct{}
	calls   []string
	err     error
	status  client.VaultStatus
	setup   client.VaultSetupResult
	blockCh chan struct{}
}

func newFakeVaultClient() *fakeVaultClient {
	return &fakeVaultClient{mu: make(chan struct{}, 1)}
}

func (f *fakeVaultClient) record(name string) {
	f.mu <- struct{}{}
	f.calls = append(f.calls, name)
	<-f.mu
}

func (f *fakeVaultClient) VaultStatus(context.Context) (client.VaultStatus, error) {
	f.record("status")
	return f.status, f.err
}

func (f *fakeVaultClient) SetupVault(_ context.Context, passphrase []byte) (client.VaultSetupResult, error) {
	f.record("setup")
	if !bytes.Equal(passphrase, []byte(testVaultInput)) {
		return client.VaultSetupResult{}, errUnexpectedPass
	}
	res := f.setup
	res.RecoveryKey = bytes.Clone(f.setup.RecoveryKey)
	return res, f.err
}

func (f *fakeVaultClient) UnlockVault(ctx context.Context, passphrase []byte) (client.VaultStatus, error) {
	f.record("unlock")
	if f.blockCh != nil {
		select {
		case <-f.blockCh:
		case <-ctx.Done():
			return client.VaultStatus{}, fmt.Errorf("unlock canceled: %w", ctx.Err())
		}
	}
	if !bytes.Equal(passphrase, []byte(testVaultInput)) && f.err == nil {
		return client.VaultStatus{}, errUnexpectedPass
	}
	return f.status, f.err
}

func (f *fakeVaultClient) UnlockVaultWithRecovery(_ context.Context, key []byte) (client.VaultStatus, error) {
	f.record("recovery")
	if !bytes.Equal(key, []byte(testVaultRecInput)) && f.err == nil {
		return client.VaultStatus{}, errUnexpectedRec
	}
	return f.status, f.err
}

func (f *fakeVaultClient) LockVault(context.Context) (client.VaultStatus, error) {
	f.record("lock")
	return f.status, f.err
}

func mustMarshalVaultStatus(t *testing.T, st Status) []byte {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func awaitWorkerResult(t *testing.T, w *Worker) Result {
	t.Helper()
	select {
	case r := <-w.Results():
		return r
	case <-t.Context().Done():
		t.Fatalf("context ended waiting for vault worker result: %v", t.Context().Err())
		return Result{}
	}
}

func TestVaultModelFailClosedAndRevisionOrdering(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if m.Known() || !m.Locked() || m.Unlocked() {
		t.Fatalf("initial model must fail closed: known=%v locked=%v unlocked=%v", m.Known(), m.Locked(), m.Unlocked())
	}

	snap := mustMarshalVaultStatus(t, Status{State: client.VaultStateLocked, Revision: 3, Incarnation: testVaultInc, RetryAfterSeconds: 4})
	if err := m.ApplySnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if !m.Known() || !m.Locked() || m.Status().RetryAfterSeconds != 4 {
		t.Fatalf("after snapshot: known=%v status=%+v", m.Known(), m.Status())
	}

	assertVaultModelEventTransitions(t, m)
}

func assertVaultModelEventTransitions(t *testing.T, m *Model) {
	t.Helper()
	stale := mustMarshalVaultStatus(t, Status{State: client.VaultStateUnlocked, Revision: 2, Incarnation: testVaultInc})
	if err := m.ApplyEvent(EventVaultChanged, stale); err != nil {
		t.Fatal(err)
	}
	if !m.Locked() {
		t.Fatal("stale revision unlocked the vault model")
	}
	fresh := mustMarshalVaultStatus(t, Status{State: client.VaultStateUnlocked, Revision: 4, Incarnation: testVaultInc})
	if err := m.ApplyEvent(EventVaultChanged, fresh); err != nil {
		t.Fatal(err)
	}
	if !m.Unlocked() {
		t.Fatal("fresh event failed to unlock the vault model")
	}
	m.ApplyStatus(Status{State: client.VaultStateLocked, Revision: 1, Incarnation: "inc-vault-2"})
	if !m.Locked() || m.Status().Incarnation != "inc-vault-2" {
		t.Fatalf("new incarnation status = %+v", m.Status())
	}
	if err := m.ApplyEvent("other.event", stale); err == nil {
		t.Fatal("unknown vault event accepted")
	}
}

func TestVaultModelAndWorkerClearBytesAfterSend(t *testing.T) {
	t.Parallel()
	fc := &fakeVaultClient{
		mu:     make(chan struct{}, 1),
		status: client.VaultStatus{State: client.VaultStateUnlocked, Revision: 2, Incarnation: testVaultInc},
		setup: client.VaultSetupResult{
			Status:      client.VaultStatus{State: client.VaultStateUnlocked, Revision: 1, Incarnation: testVaultInc},
			RecoveryKey: []byte(testVaultRecInput),
		},
	}
	assertWorkerRedaction(t)

	w := NewWorker(func() Client { return fc })
	w.Start(t.Context())
	defer w.Stop()

	assertWorkerSetupClearsBytes(t, w)
	assertWorkerOpsClearBytes(t, w)

	wantCalls := []string{"setup", "status", "unlock", "recovery", "lock"}
	if !slices.Equal(fc.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", fc.calls, wantCalls)
	}
}

func assertWorkerRedaction(t *testing.T) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	reqSample := Request{Kind: OpSetup, Secret: []byte(testVaultInput)}
	resSample := Result{Kind: OpSetup, Status: &Status{State: client.VaultStateUnlocked}, RecoveryKey: []byte(testVaultRecInput)}
	logger.Info("vault op", "req", reqSample, "res", resSample)
	for _, s := range []string{
		fmt.Sprintf("%v|%#v|%+v|%s", reqSample, reqSample, reqSample, reqSample),
		fmt.Sprintf("%v|%#v|%+v|%s", resSample, resSample, resSample, resSample),
		logs.String(),
	} {
		if strings.Contains(s, testVaultInput) || strings.Contains(s, testVaultRecInput) {
			t.Fatalf("worker Request/Result formatting leaked secret: %q", s)
		}
	}
	resSample.Clear()
}

func assertWorkerSetupClearsBytes(t *testing.T, w *Worker) {
	t.Helper()
	passBuf := []byte(testVaultInput)
	backing := passBuf
	req := Request{Kind: OpSetup, Secret: passBuf}
	w.Do(&req)
	if req.Secret != nil {
		t.Fatal("Do did not detach caller Secret slice reference")
	}
	res := awaitWorkerResult(t, w)
	if res.Err != nil || !bytes.Equal(res.RecoveryKey, []byte(testVaultRecInput)) {
		t.Fatalf("Setup result = %+v", res)
	}
	res.Clear()
	if !bytes.Equal(backing, make([]byte, len(backing))) {
		t.Fatal("worker did not zero request backing byte slice after execution")
	}
}

func assertWorkerOpsClearBytes(t *testing.T, w *Worker) {
	t.Helper()
	for _, op := range []Request{
		{Kind: OpStatus},
		{Kind: OpUnlock, Secret: []byte(testVaultInput)},
		{Kind: OpUnlockRecovery, Secret: []byte(testVaultRecInput)},
		{Kind: OpLock},
	} {
		secretCopy := op.Secret
		w.Do(&op)
		if r := awaitWorkerResult(t, w); r.Err != nil {
			t.Fatalf("op %d err = %v", r.Kind, r.Err)
		}
		if !bytes.Equal(secretCopy, make([]byte, len(secretCopy))) {
			t.Fatalf("op %d left non-zero secret bytes", op.Kind)
		}
	}
}

func TestVaultWorkerQueueFullAndCancelClearSecrets(t *testing.T) {
	t.Parallel()
	fc := newFakeVaultClient()
	fc.blockCh = make(chan struct{})
	w := NewWorker(func() Client { return fc })
	w.Start(t.Context())

	first := Request{Kind: OpUnlock, Secret: []byte("blocked-secret")}
	w.Do(&first)
	for range workerBuffer {
		queued := Request{Kind: OpUnlock, Secret: []byte("queued-secret")}
		w.Do(&queued)
	}
	overflowBuf := []byte("overflow-secret")
	overflow := Request{Kind: OpUnlock, Secret: overflowBuf}
	w.Do(&overflow)
	if !bytes.Equal(overflowBuf, make([]byte, len(overflowBuf))) {
		t.Fatal("queue-full Do did not clear overflowing Secret bytes")
	}
	if r := awaitWorkerResult(t, w); !errors.Is(r.Err, errWorkerQueueFull) {
		t.Fatalf("overflow result err = %v, want errWorkerQueueFull", r.Err)
	}
	close(fc.blockCh)
	w.Stop()
}
