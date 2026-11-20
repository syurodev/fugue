package client_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	vaultv1 "github.com/syurodev/fugue/pkg/pb/fugue/vault/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vault/v1/vaultv1connect"
)

const (
	testClientVaultPass    = "client-vault-pass-987"
	testClientVaultRecKey  = "client-recovery-key-654"
	testClientVaultCodeErr = "vault.rate_limited"
	testClientVaultInc     = "inc-vault-1"
)

var (
	errClientVaultScripted = errors.New("scripted vault error")
	errClientVaultPlain    = errors.New("plain error")
)

type vaultFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	vaultv1connect.UnimplementedVaultServiceHandler

	mu     sync.Mutex
	err    error
	status *vaultv1.VaultStatus
}

func (f *vaultFake) Hello(_ context.Context, _ *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{HostVersion: "v1", ApiVersion: "v1"}), nil
}

func (f *vaultFake) GetStatus(_ context.Context, _ *connect.Request[vaultv1.GetStatusRequest]) (*connect.Response[vaultv1.GetStatusResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&vaultv1.GetStatusResponse{Status: f.status}), nil
}

func (f *vaultFake) Setup(_ context.Context, req *connect.Request[vaultv1.SetupRequest]) (*connect.Response[vaultv1.SetupResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if !bytes.Equal(req.Msg.GetPassphrase(), []byte(testClientVaultPass)) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errClientVaultScripted)
	}
	f.status = &vaultv1.VaultStatus{State: client.VaultStateUnlocked, Revision: 1, Incarnation: testClientVaultInc}
	return connect.NewResponse(&vaultv1.SetupResponse{
		Status:      f.status,
		RecoveryKey: []byte(testClientVaultRecKey),
	}), nil
}

func (f *vaultFake) Unlock(_ context.Context, req *connect.Request[vaultv1.UnlockRequest]) (*connect.Response[vaultv1.UnlockResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if !bytes.Equal(req.Msg.GetPassphrase(), []byte(testClientVaultPass)) {
		return nil, scriptedVaultError(testClientVaultCodeErr, 4)
	}
	f.status = &vaultv1.VaultStatus{State: client.VaultStateUnlocked, Revision: 3, Incarnation: testClientVaultInc}
	return connect.NewResponse(&vaultv1.UnlockResponse{Status: f.status}), nil
}

func (f *vaultFake) UnlockWithRecovery(_ context.Context, req *connect.Request[vaultv1.UnlockWithRecoveryRequest]) (*connect.Response[vaultv1.UnlockWithRecoveryResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if !bytes.Equal(req.Msg.GetRecoveryKey(), []byte(testClientVaultRecKey)) {
		return nil, scriptedVaultError("vault.invalid_passphrase", 0)
	}
	f.status = &vaultv1.VaultStatus{State: client.VaultStateUnlocked, Revision: 5, Incarnation: testClientVaultInc}
	return connect.NewResponse(&vaultv1.UnlockWithRecoveryResponse{Status: f.status}), nil
}

func (f *vaultFake) Lock(_ context.Context, _ *connect.Request[vaultv1.LockRequest]) (*connect.Response[vaultv1.LockResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.status = &vaultv1.VaultStatus{State: client.VaultStateLocked, Revision: 2, Incarnation: testClientVaultInc}
	return connect.NewResponse(&vaultv1.LockResponse{Status: f.status}), nil
}

func serveVaultFake(t *testing.T) (*client.Client, *vaultFake) {
	t.Helper()
	fake := &vaultFake{status: &vaultv1.VaultStatus{State: client.VaultStateNotConfigured, Incarnation: testClientVaultInc}}
	socket := filepath.Join(shortDir(t), "vault.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
	mux.Handle(vaultv1connect.NewVaultServiceHandler(fake))
	var protos http.Protocols
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)

	c, err := client.Dial(t.Context(), socket, appParams())
	if err != nil {
		t.Fatalf("dial the scripted vault host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, fake
}

func scriptedVaultError(code string, retryAfter int64) error {
	cerr := connect.NewError(connect.CodeResourceExhausted, errClientVaultScripted)
	detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: code, RetryAfterSeconds: retryAfter})
	if err != nil {
		panic(err)
	}
	cerr.AddDetail(detail)
	return cerr
}

func TestVaultClientMethodsOverRealSocket(t *testing.T) {
	t.Parallel()
	c, fake := serveVaultFake(t)

	st, err := c.VaultStatus(t.Context())
	if err != nil || st.State != client.VaultStateNotConfigured {
		t.Fatalf("VaultStatus = %+v, err=%v", st, err)
	}
	recCopy := assertClientSetupRedactsAndClears(t, c)
	assertClientUnlockLockAndRecovery(t, c, recCopy)
	assertClientVaultErrorPaths(t, c, fake)
}

func assertClientSetupRedactsAndClears(t *testing.T, c *client.Client) []byte {
	t.Helper()
	pass := []byte(testClientVaultPass)
	res, err := c.SetupVault(t.Context(), pass)
	if err != nil || res.Status.State != client.VaultStateUnlocked || !bytes.Equal(res.RecoveryKey, []byte(testClientVaultRecKey)) {
		t.Fatalf("SetupVault = %+v, err=%v", res, err)
	}
	if !bytes.Equal(pass, make([]byte, len(pass))) {
		t.Fatal("SetupVault did not clear caller passphrase slice")
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("setup", "res", res)
	for _, s := range []string{res.String(), res.GoString(), fmt.Sprintf("%v|%#v|%+v|%s", res, res, res, res), logs.String()} {
		if strings.Contains(s, testClientVaultRecKey) {
			t.Fatalf("VaultSetupResult formatting leaked recovery key: %q", s)
		}
	}
	recCopy := bytes.Clone(res.RecoveryKey)
	recSlice := res.RecoveryKey
	res.Clear()
	if !bytes.Equal(recSlice, make([]byte, len(recSlice))) {
		t.Fatal("Clear did not zero RecoveryKey backing slice")
	}
	return recCopy
}

func assertClientUnlockLockAndRecovery(t *testing.T, c *client.Client, recCopy []byte) {
	t.Helper()
	locked, err := c.LockVault(t.Context())
	if err != nil || locked.State != client.VaultStateLocked {
		t.Fatalf("LockVault = %+v, err=%v", locked, err)
	}
	assertClientWrongUnlockRateLimited(t, c)
	unlocked, err := c.UnlockVault(t.Context(), []byte(testClientVaultPass))
	if err != nil || unlocked.State != client.VaultStateUnlocked {
		t.Fatalf("UnlockVault = %+v, err=%v", unlocked, err)
	}
	byRec, err := c.UnlockVaultWithRecovery(t.Context(), recCopy)
	if err != nil || byRec.State != client.VaultStateUnlocked {
		t.Fatalf("UnlockVaultWithRecovery = %+v, err=%v", byRec, err)
	}
	if !bytes.Equal(recCopy, make([]byte, len(recCopy))) {
		t.Fatal("UnlockVaultWithRecovery did not clear caller recovery slice")
	}
}

func assertClientWrongUnlockRateLimited(t *testing.T, c *client.Client) {
	t.Helper()
	wrong := []byte("wrong-client-secret")
	_, uerr := c.UnlockVault(t.Context(), wrong)
	if !bytes.Equal(wrong, make([]byte, len(wrong))) {
		t.Fatal("UnlockVault did not clear caller slice on error")
	}
	if code, ok := client.VaultCodeOf(uerr); !ok || code != testClientVaultCodeErr {
		t.Fatalf("VaultCodeOf(uerr) = (%q, %v), want %q", code, ok, testClientVaultCodeErr)
	}
	if retry, ok := client.VaultRetryAfter(uerr); !ok || retry != 4 {
		t.Fatalf("VaultRetryAfter(uerr) = (%d, %v), want 4", retry, ok)
	}
}

func assertClientVaultErrorPaths(t *testing.T, c *client.Client, fake *vaultFake) {
	t.Helper()
	if _, ok := client.VaultCodeOf(nil); ok {
		t.Fatal("VaultCodeOf(nil) returned ok")
	}
	if _, ok := client.VaultRetryAfter(errClientVaultPlain); ok {
		t.Fatal("VaultRetryAfter(plain) returned ok")
	}
	fake.err = scriptedVaultError("vault.not_configured", 0)
	if _, err := c.VaultStatus(t.Context()); err == nil {
		t.Fatal("expected VaultStatus error")
	}
	if _, err := c.LockVault(t.Context()); err == nil {
		t.Fatal("expected LockVault error")
	}
	if _, err := c.SetupVault(t.Context(), []byte(testClientVaultPass)); err == nil {
		t.Fatal("expected SetupVault error")
	}
	if _, err := c.UnlockVaultWithRecovery(t.Context(), []byte(testClientVaultRecKey)); err == nil {
		t.Fatal("expected UnlockVaultWithRecovery error")
	}
}
