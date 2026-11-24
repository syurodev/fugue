package hostapi

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	accountsv1 "github.com/syurodev/fugue/pkg/pb/fugue/accounts/v1"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
)

func TestListAccountsRefreshesLoginAndStreamsTheQuotaTopic(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, true)
	stream, err := v.host.Subscribe(streamCtx(t), connect.NewRequest(&hostv1.SubscribeRequest{Topic: app.TopicQuota}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().GetSnapshot() == nil {
		t.Fatalf("the quota topic did not open with a snapshot: %v", stream.Err())
	}
	resp, err := v.accounts.ListAccounts(t.Context(), connect.NewRequest(&accountsv1.ListAccountsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	assertListedAccounts(t, resp.Msg.Accounts)
	assertQuotaStreamEventGate(t, stream)
}

func assertQuotaStreamEventGate(t *testing.T, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse]) {
	t.Helper()
	// The refresh changed claude's login, so the topic carries that change.
	for stream.Receive() {
		ev := stream.Msg().GetEvent()
		if ev == nil {
			continue
		}
		if ev.Type != app.EventQuotaAccountChanged {
			t.Fatalf("event type = %q", ev.Type)
		}
		var changed app.AccountView
		if err := json.Unmarshal(ev.Payload, &changed); err != nil {
			t.Fatalf("unmarshal quota event: %v", err)
		}
		if changed.QuotaGate != app.QuotaGateThreshold {
			t.Fatalf("quota event quota_gate = %v, want %v", changed.QuotaGate, app.QuotaGateThreshold)
		}
		return
	}
	t.Fatalf("no quota event after the login refresh: %v", stream.Err())
}

func assertListedAccounts(t *testing.T, accounts []*accountsv1.Account) {
	t.Helper()
	byVendor := map[vendorsv1.Vendor]*accountsv1.Account{}
	for _, a := range accounts {
		byVendor[a.Vendor] = a
	}
	claude := byVendor[vendorsv1.Vendor_VENDOR_CLAUDE]
	if len(accounts) != 3 || claude == nil || claude.Login != "logged_in" || claude.State != "ok" || !claude.QuotaKnown || claude.QuotaGate != app.QuotaGateThreshold {
		t.Fatalf("accounts = %v, want claude logged in, ok, and quota_gate=%v", accounts, app.QuotaGateThreshold)
	}
	if agy := byVendor[vendorsv1.Vendor_VENDOR_AGY]; agy == nil || agy.QuotaKnown || len(agy.Windows) != 0 {
		t.Fatalf("agy account = %v, want quota unknown", agy)
	}
}

func TestAccountsNonLocalPrincipalIsDeniedOnRPCAndTopic(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, true)
	v.srv.mu.Lock()
	for _, st := range v.srv.conns {
		st.mu.Lock()
		st.kind = app.ClientKind("session")
		st.mu.Unlock()
	}
	v.srv.mu.Unlock()
	_, err := v.accounts.ListAccounts(t.Context(), connect.NewRequest(&accountsv1.ListAccountsRequest{}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	stream, subErr := v.host.Subscribe(streamCtx(t), connect.NewRequest(&hostv1.SubscribeRequest{Topic: app.TopicQuota}))
	if subErr != nil {
		t.Fatal(subErr)
	}
	if stream.Receive() {
		t.Fatal("a non-local principal subscribed to the quota topic")
	}
	assertConnectCode(t, stream.Err(), connect.CodePermissionDenied, errcode.PermissionDenied)
}

func TestSubmitLoginCodeValidationAndPrincipal(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, true)
	_, err := v.accounts.SubmitLoginCode(t.Context(), connect.NewRequest(&accountsv1.SubmitLoginCodeRequest{
		CommandId: "cmd-1",
		LoginId:   "login-1",
		Code:      "",
	}))
	assertConnectCode(t, err, connect.CodeInvalidArgument, errcode.InvalidArgument)

	_, err = v.accounts.SubmitLoginCode(t.Context(), connect.NewRequest(&accountsv1.SubmitLoginCodeRequest{
		CommandId: "cmd-2",
		LoginId:   "login-1",
		Code:      "missing-login#state",
	}))
	assertConnectCode(t, err, connect.CodeFailedPrecondition, errcode.FailedPrecondition)

	v.srv.mu.Lock()
	for _, st := range v.srv.conns {
		st.mu.Lock()
		st.kind = app.ClientKind("session")
		st.mu.Unlock()
	}
	v.srv.mu.Unlock()
	_, err = v.accounts.SubmitLoginCode(t.Context(), connect.NewRequest(&accountsv1.SubmitLoginCodeRequest{
		CommandId: "cmd-3",
		LoginId:   "login-1",
		Code:      "code#state",
	}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
}
