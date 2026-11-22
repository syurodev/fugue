package hostapi

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/platform/errcode"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	secretsv1 "github.com/syurodev/fugue/pkg/pb/fugue/secrets/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/secrets/v1/secretsv1connect"
	vaultv1 "github.com/syurodev/fugue/pkg/pb/fugue/vault/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vault/v1/vaultv1connect"
)

const testHostSecretName = "API_KEY"

func TestSecretsHostAPIDeniesNonAppAndSessionPrincipals(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "")

	for _, tc := range []struct {
		name   string
		client func(t *testing.T) secretsv1connect.SecretServiceClient
	}{
		{
			name: "unhelloed",
			client: func(t *testing.T) secretsv1connect.SecretServiceClient {
				t.Helper()
				_, hc := newTestClient(t, v.sockPath)
				return secretsv1connect.NewSecretServiceClient(hc, testBaseURL)
			},
		},
		{
			name: "cli principal",
			client: func(t *testing.T) secretsv1connect.SecretServiceClient {
				t.Helper()
				host, hc := newTestClient(t, v.sockPath)
				if _, err := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
					t.Fatalf("Hello cli: %v", err)
				}
				return secretsv1connect.NewSecretServiceClient(hc, testBaseURL)
			},
		},
		{
			name: "session principal",
			client: func(t *testing.T) secretsv1connect.SecretServiceClient {
				t.Helper()
				wsID := v.workspaceWithRoot(t, t.TempDir())
				_ = v.readySession(t, "secrets-deny-1", wsID, "")
				tokens := sessionTokens(t, 1)
				_, sessHTTP, err := attachSession(t, v.sockPath, tokens[0])
				if err != nil {
					t.Fatal(err)
				}
				return secretsv1connect.NewSecretServiceClient(sessHTTP, testBaseURL)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertAllSecretsRPCsDenied(t, tc.client(t))
		})
	}
}

func assertAllSecretsRPCsDenied(t *testing.T, c secretsv1connect.SecretServiceClient) {
	t.Helper()
	if _, err := c.ListSecrets(t.Context(), connect.NewRequest(&secretsv1.ListSecretsRequest{})); err == nil {
		t.Fatal("ListSecrets allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.PutSecret(t.Context(), connect.NewRequest(&secretsv1.PutSecretRequest{
		CommandId: "c1", ScopeKind: secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_GLOBAL, Name: "K", Value: []byte("12345678"),
	})); err == nil {
		t.Fatal("PutSecret allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.DeleteSecret(t.Context(), connect.NewRequest(&secretsv1.DeleteSecretRequest{
		CommandId: "c2", SecretId: "sec-1",
	})); err == nil {
		t.Fatal("DeleteSecret allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
}

func TestSecretsHostAPILockedVaultAndRoundTrip(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, false)
	host, hc := newTestClient(t, v.sockPath)
	if _, err := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		t.Fatalf("Hello app: %v", err)
	}
	c := secretsv1connect.NewSecretServiceClient(hc, testBaseURL)
	vc := vaultv1connect.NewVaultServiceClient(hc, testBaseURL)
	ctx := t.Context()

	assertSecretsLockedBehavior(t, c)
	if _, err := vc.Setup(ctx, connect.NewRequest(&vaultv1.SetupRequest{Passphrase: []byte(testHostVaultPass)})); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	secID := assertSecretsPutAndDedup(t, c)
	assertSecretsDeleteAndRelock(t, c, vc, secID)
}

func assertSecretsLockedBehavior(t *testing.T, c secretsv1connect.SecretServiceClient) {
	t.Helper()
	ctx := t.Context()
	if res, err := c.ListSecrets(ctx, connect.NewRequest(&secretsv1.ListSecretsRequest{})); err != nil || len(res.Msg.GetSecrets()) != 0 {
		t.Fatalf("ListSecrets while locked = (%+v, %v)", res, err)
	}
	if _, err := c.PutSecret(ctx, connect.NewRequest(&secretsv1.PutSecretRequest{
		CommandId: "put-locked", ScopeKind: secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_GLOBAL, Name: testHostSecretName, Value: []byte("secret-value-1"),
	})); err == nil {
		t.Fatal("PutSecret succeeded while vault locked")
	} else {
		assertConnectCode(t, err, connect.CodeFailedPrecondition, errcode.FailedPrecondition)
	}
}

func assertSecretsPutAndDedup(t *testing.T, c secretsv1connect.SecretServiceClient) string {
	t.Helper()
	ctx := t.Context()
	putReq := &secretsv1.PutSecretRequest{
		CommandId:   "put-1",
		ScopeKind:   secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_GLOBAL,
		Name:        testHostSecretName,
		Description: "Host API key",
		Value:       []byte("secret-value-1234"),
	}
	putRes, err := c.PutSecret(ctx, connect.NewRequest(putReq))
	if err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	secID := putRes.Msg.GetSecret().GetSecretId()
	if secID == "" || putRes.Msg.GetSecret().GetName() != testHostSecretName {
		t.Fatalf("unexpected PutSecret meta: %+v", putRes.Msg.GetSecret())
	}
	dedupRes, dedupErr := c.PutSecret(ctx, connect.NewRequest(putReq))
	if dedupErr != nil || dedupRes.Msg.GetSecret().GetSecretId() != secID {
		t.Fatalf("dedup PutSecret = (%+v, %v)", dedupRes, dedupErr)
	}
	if _, reuseErr := c.PutSecret(ctx, connect.NewRequest(&secretsv1.PutSecretRequest{
		CommandId: "put-1", ScopeKind: secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_GLOBAL, Name: "OTHER", Value: []byte("secret-value-1234"),
	})); reuseErr == nil {
		t.Fatal("PutSecret reused command_id with different args")
	}
	return secID
}

func assertSecretsDeleteAndRelock(t *testing.T, c secretsv1connect.SecretServiceClient, vc vaultv1connect.VaultServiceClient, secID string) {
	t.Helper()
	ctx := t.Context()
	delRes, delErr := c.DeleteSecret(ctx, connect.NewRequest(&secretsv1.DeleteSecretRequest{
		CommandId: "del-1", SecretId: secID,
	}))
	if delErr != nil || !delRes.Msg.GetDeleted() {
		t.Fatalf("DeleteSecret = (%+v, %v)", delRes, delErr)
	}
	if _, lockErr := vc.Lock(ctx, connect.NewRequest(&vaultv1.LockRequest{})); lockErr != nil {
		t.Fatalf("Lock: %v", lockErr)
	}
	if res, listErr := c.ListSecrets(ctx, connect.NewRequest(&secretsv1.ListSecretsRequest{})); listErr != nil || len(res.Msg.GetSecrets()) != 0 {
		t.Fatalf("ListSecrets after delete & lock = (%+v, %v)", res, listErr)
	}
}
