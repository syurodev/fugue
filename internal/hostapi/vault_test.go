package hostapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	vaultv1 "github.com/syurodev/fugue/pkg/pb/fugue/vault/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vault/v1/vaultv1connect"
)

const (
	testBaseURL        = "http://fugue"
	testHostVaultPass  = "host-vault-passphrase-123"
	testHostVaultWrong = "host-vault-wrong-secret"
)

func TestVaultRPCsDenySessionAndNonLocalPrincipals(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "")
	_, httpClient := newTestClient(t, v.sockPath)
	vaultClient := vaultv1connect.NewVaultServiceClient(httpClient, testBaseURL)

	// Unhelloed connection must be denied on every vault RPC.
	assertAllVaultRPCsDenied(t, vaultClient)

	// Session principal (attached with a real session token) must also be denied on every vault RPC.
	wsID := v.workspaceWithRoot(t, t.TempDir())
	_ = v.readySession(t, "vault-deny-1", wsID, "")
	tokens := sessionTokens(t, 1)
	_, sessHTTP, err := attachSession(t, v.sockPath, tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	assertAllVaultRPCsDenied(t, vaultv1connect.NewVaultServiceClient(sessHTTP, testBaseURL))
}

func assertAllVaultRPCsDenied(t *testing.T, vc vaultv1connect.VaultServiceClient) {
	t.Helper()
	if _, err := vc.GetStatus(t.Context(), connect.NewRequest(&vaultv1.GetStatusRequest{})); err == nil {
		t.Fatal("GetStatus allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := vc.Setup(t.Context(), connect.NewRequest(&vaultv1.SetupRequest{Passphrase: []byte(testHostVaultPass)})); err == nil {
		t.Fatal("Setup allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := vc.Unlock(t.Context(), connect.NewRequest(&vaultv1.UnlockRequest{Passphrase: []byte(testHostVaultPass)})); err == nil {
		t.Fatal("Unlock allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := vc.UnlockWithRecovery(t.Context(), connect.NewRequest(&vaultv1.UnlockWithRecoveryRequest{RecoveryKey: []byte("rec")})); err == nil {
		t.Fatal("UnlockWithRecovery allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := vc.Lock(t.Context(), connect.NewRequest(&vaultv1.LockRequest{})); err == nil {
		t.Fatal("Lock allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
}

func TestVaultHostAPISecretAbsentFromLogsErrorsAndProtoFormatting(t *testing.T) {
	t.Parallel()
	assertVaultProtoDebugRedact(t)

	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(redact.NewJSONHandler(&logs, slog.LevelDebug)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	vc, stream := startLocalVaultHostAndSubscribe(t)
	recPlain, badErrText := exerciseVaultHostAPI(t, vc)
	events := collectVaultHostStreamEvents(t, stream)
	for _, needle := range []string{testHostVaultPass, testHostVaultWrong, recPlain} {
		for _, hay := range []string{logs.String(), events, badErrText} {
			if strings.Contains(hay, needle) {
				t.Fatalf("protected input leaked in output %q", hay)
			}
		}
	}
}

func assertVaultProtoDebugRedact(t *testing.T) {
	t.Helper()
	for _, msg := range []protoreflect.ProtoMessage{
		&vaultv1.SetupRequest{},
		&vaultv1.SetupResponse{},
		&vaultv1.UnlockRequest{},
		&vaultv1.UnlockWithRecoveryRequest{},
	} {
		desc := msg.ProtoReflect().Descriptor()
		for i := range desc.Fields().Len() {
			fd := desc.Fields().Get(i)
			if fd.Name() == "status" {
				continue
			}
			opts, ok := fd.Options().(*descriptorpb.FieldOptions)
			if !ok || opts == nil || !opts.GetDebugRedact() {
				t.Fatalf("field %s.%s missing debug_redact = true", desc.Name(), fd.Name())
			}
		}
	}
}

func startLocalVaultHostAndSubscribe(t *testing.T) (vaultv1connect.VaultServiceClient, *connect.ServerStreamForClient[hostv1.SubscribeResponse]) {
	t.Helper()
	v := startVendorHost(t, false)
	_, httpClient := newTestClient(t, v.sockPath)
	host := hostv1connect.NewHostServiceClient(httpClient, testBaseURL)
	if _, err := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		t.Fatal(err)
	}
	stream, err := host.Subscribe(streamCtx(t), connect.NewRequest(&hostv1.SubscribeRequest{Topic: app.TopicVault}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().GetSnapshot() == nil {
		t.Fatalf("vault topic missing initial snapshot: %v", stream.Err())
	}
	return vaultv1connect.NewVaultServiceClient(httpClient, testBaseURL), stream
}

func exerciseVaultHostAPI(t *testing.T, vc vaultv1connect.VaultServiceClient) (string, string) {
	t.Helper()
	stRes, err := vc.GetStatus(t.Context(), connect.NewRequest(&vaultv1.GetStatusRequest{}))
	if err != nil || stRes.Msg.GetStatus().GetState() != "not_configured" {
		t.Fatalf("initial GetStatus = %v, err=%v", stRes, err)
	}
	setupRes, err := vc.Setup(t.Context(), connect.NewRequest(&vaultv1.SetupRequest{Passphrase: []byte(testHostVaultPass)}))
	if err != nil || setupRes.Msg.GetStatus().GetState() != "unlocked" || len(setupRes.Msg.GetRecoveryKey()) == 0 {
		t.Fatalf("Setup = %v, err=%v", setupRes, err)
	}
	if _, rerr := vc.Setup(t.Context(), connect.NewRequest(&vaultv1.SetupRequest{Passphrase: []byte("another-passphrase")})); rerr == nil {
		t.Fatal("repeat Setup succeeded")
	} else {
		assertConnectCode(t, rerr, connect.CodeAlreadyExists, errcode.VaultAlreadyConfigured)
	}
	if _, lerr := vc.Lock(t.Context(), connect.NewRequest(&vaultv1.LockRequest{})); lerr != nil {
		t.Fatal(lerr)
	}
	_, badErr := vc.Unlock(t.Context(), connect.NewRequest(&vaultv1.UnlockRequest{Passphrase: []byte(testHostVaultWrong)}))
	assertConnectCode(t, badErr, connect.CodePermissionDenied, errcode.VaultInvalidPassphrase)

	recPlain := string(setupRes.Msg.GetRecoveryKey())
	if _, rerr := vc.UnlockWithRecovery(t.Context(), connect.NewRequest(&vaultv1.UnlockWithRecoveryRequest{RecoveryKey: []byte(recPlain)})); rerr != nil {
		t.Fatal(rerr)
	}
	return recPlain, badErr.Error()
}

func collectVaultHostStreamEvents(t *testing.T, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse]) string {
	t.Helper()
	var events strings.Builder
	events.Write(stream.Msg().GetSnapshot().GetState())
	for range 3 {
		if !stream.Receive() {
			break
		}
		if ev := stream.Msg().GetEvent(); ev != nil {
			var decoded map[string]any
			if jerr := json.Unmarshal(ev.GetPayload(), &decoded); jerr != nil {
				t.Fatalf("unmarshal vault event: %v", jerr)
			}
			events.Write(ev.GetPayload())
		}
	}
	return events.String()
}

func TestVaultHandlerDirectlyClearsRequestBytes(t *testing.T) {
	t.Parallel()
	h := &vaultHandler{backend: newFakeBackend()}
	req := connect.NewRequest(&vaultv1.UnlockRequest{Passphrase: []byte("direct-secret")})
	if _, err := h.Unlock(t.Context(), req); err == nil {
		t.Fatal("expected unauthenticated Unlock to fail")
	}
	if !bytes.Equal(req.Msg.Passphrase, make([]byte, len(req.Msg.Passphrase))) {
		t.Fatal("Unlock handler did not clear request passphrase bytes on auth error")
	}
}
