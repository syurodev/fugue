package client_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/pkg/client"
	accountsv1 "github.com/syurodev/fugue/pkg/pb/fugue/accounts/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/accounts/v1/accountsv1connect"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
)

const (
	testAccInc         = "inc-acc-1"
	testAccLoginID     = "login-codex-1"
	testAccResetTime   = "2026-11-20T18:00:00Z"
	testAccUpdatedTime = "2026-11-20T15:00:00Z"
	testAccPromptRaw   = "Visit https://auth.openai.com/codex/device and enter CODE-9421\n[untrusted](http://evil.invalid)"
)

var errClientAccScripted = errors.New("scripted accounts error")

type accountsFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	accountsv1connect.UnimplementedAccountServiceHandler

	mu        sync.Mutex
	accounts  []*accountsv1.Account
	started   []vendorsv1.Vendor
	cancelled []vendorsv1.Vendor
	topics    map[string][]*hostv1.SubscribeResponse
}

func (f *accountsFake) Hello(_ context.Context, _ *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{HostVersion: "v1", ApiVersion: "v1"}), nil
}

func (f *accountsFake) Subscribe(_ context.Context, req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
	f.mu.Lock()
	msgs := append([]*hostv1.SubscribeResponse(nil), f.topics[req.Msg.GetTopic()]...)
	f.mu.Unlock()
	if len(msgs) == 0 {
		return connect.NewError(connect.CodeNotFound, errClientAccScripted)
	}
	for _, msg := range msgs {
		if err := stream.Send(msg); err != nil {
			return fmt.Errorf("send accounts topic message: %w", err)
		}
	}
	return nil
}

// TestAccountsQuotaGateComesFromTheHost verifies that Account.quota_gate
// travels from the Host API proto and topic JSON payloads into client.Account
// (and 0 means no gate).
func TestAccountsQuotaGateComesFromTheHost(t *testing.T) {
	t.Parallel()
	const customGate = 0.78
	want, protoAccs := seedAccountsFixtures()
	want[0].QuotaGate = customGate
	want[1].QuotaGate = 0
	protoAccs[0].QuotaGate = customGate
	protoAccs[1].QuotaGate = 0

	fake := &accountsFake{
		accounts: protoAccs,
		topics: map[string][]*hostv1.SubscribeResponse{
			client.TopicQuota: {
				{Kind: &hostv1.SubscribeResponse_Snapshot{Snapshot: &hostv1.SnapshotEvent{
					Sequence: 1, IncarnationId: testAccInc,
					State: mustJSONBytes(t, map[string]any{"accounts": want}),
				}}},
				{Kind: &hostv1.SubscribeResponse_Event{Event: &hostv1.HostEvent{
					Sequence: 2, Type: client.EventQuotaAccountChanged, Payload: mustJSONBytes(t, want[0]),
				}}},
				{Kind: &hostv1.SubscribeResponse_End{End: &hostv1.StreamEnd{}}},
			},
		},
	}
	c := serveAccountsFake(t, fake)

	got, err := c.ListAccounts(t.Context())
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("ListAccounts quota_gate mismatch (-want +got):\n%s", diff)
	}
	stream, err := c.Subscribe(t.Context(), client.TopicQuota, nil)
	if err != nil {
		t.Fatalf("Subscribe quota: %v", err)
	}
	defer func() {
		if cerr := stream.Close(); cerr != nil {
			t.Errorf("close quota stream: %v", cerr)
		}
	}()
	snapMsg := recvMsg(t, stream)
	snap, err := client.DecodeQuotaSnapshot(snapMsg.Snapshot.State)
	if err != nil {
		t.Fatalf("DecodeQuotaSnapshot: %v", err)
	}
	if diff := cmp.Diff(want, snap); diff != "" {
		t.Fatalf("quota snapshot mismatch (-want +got):\n%s", diff)
	}
	evMsg := recvMsg(t, stream)
	changed, err := client.DecodeQuotaAccountChanged(evMsg.Event.Payload)
	if err != nil || changed.QuotaGate != customGate {
		t.Fatalf("DecodeQuotaAccountChanged = (%+v, %v), want QuotaGate=%v", changed, err, customGate)
	}
}

func (f *accountsFake) ListAccounts(_ context.Context, _ *connect.Request[accountsv1.ListAccountsRequest]) (*connect.Response[accountsv1.ListAccountsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&accountsv1.ListAccountsResponse{Accounts: f.accounts}), nil
}

func (f *accountsFake) StartLogin(_ context.Context, req *connect.Request[accountsv1.StartLoginRequest]) (*connect.Response[accountsv1.StartLoginResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := req.Msg.GetVendor()
	f.started = append(f.started, v)
	if v == vendorsv1.Vendor_VENDOR_AGY {
		return nil, scriptedAccountsError(connect.CodeFailedPrecondition, "failed_precondition")
	}
	return connect.NewResponse(&accountsv1.StartLoginResponse{LoginId: testAccLoginID}), nil
}

func (f *accountsFake) CancelLogin(_ context.Context, req *connect.Request[accountsv1.CancelLoginRequest]) (*connect.Response[accountsv1.CancelLoginResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := req.Msg.GetVendor()
	f.cancelled = append(f.cancelled, v)
	if v == vendorsv1.Vendor_VENDOR_CLAUDE {
		return nil, scriptedAccountsError(connect.CodeNotFound, "not_found")
	}
	return connect.NewResponse(&accountsv1.CancelLoginResponse{}), nil
}

func scriptedAccountsError(cc connect.Code, code string) error {
	cerr := connect.NewError(cc, errClientAccScripted)
	detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: code})
	if err != nil {
		panic(err)
	}
	cerr.AddDetail(detail)
	return cerr
}

func serveAccountsFake(t *testing.T, fake *accountsFake) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
	mux.Handle(accountsv1connect.NewAccountServiceHandler(fake))
	socket := filepath.Join(shortDir(t), "accounts.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("dial scripted accounts host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func seedAccountsFixtures() (want []client.Account, protoAccs []*accountsv1.Account) {
	want = []client.Account{
		{
			Vendor: client.VendorClaude, State: client.AccountStateOK, Login: client.AccountLoginLoggedIn,
			QuotaKnown: true, UpdatedAt: testAccUpdatedTime,
			Windows: []client.QuotaWindow{
				{Name: "five_hour", Utilization: 0.42, ResetsAt: testAccResetTime, Status: client.QuotaStatusAllowed},
				{Name: "seven_day", Utilization: 0.90, ResetsAt: testAccResetTime, Status: client.QuotaStatusAllowedWarning},
			},
		},
		{
			Vendor: client.VendorCodex, State: client.AccountStatePaused, PausedUntil: testAccResetTime,
			Login: client.AccountLoginLoggedIn, QuotaKnown: true, UpdatedAt: testAccUpdatedTime,
			Windows: []client.QuotaWindow{
				{Name: "300m", Utilization: 1.0, ResetsAt: testAccResetTime, Status: client.QuotaStatusRejected},
			},
		},
		{
			Vendor: client.VendorAGY, State: client.AccountStateNeedsLogin,
			Login: client.AccountLoginUnknown, QuotaKnown: false, Windows: []client.QuotaWindow{},
		},
	}
	protoAccs = []*accountsv1.Account{
		{
			Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, State: want[0].State, Login: want[0].Login,
			QuotaKnown: true, UpdatedAt: testAccUpdatedTime,
			Windows: []*accountsv1.QuotaWindow{
				{Name: "five_hour", Utilization: 0.42, ResetsAt: testAccResetTime, Status: client.QuotaStatusAllowed},
				{Name: "seven_day", Utilization: 0.90, ResetsAt: testAccResetTime, Status: client.QuotaStatusAllowedWarning},
			},
		},
		{
			Vendor: vendorsv1.Vendor_VENDOR_CODEX, State: want[1].State, PausedUntil: testAccResetTime,
			Login: want[1].Login, QuotaKnown: true, UpdatedAt: testAccUpdatedTime,
			Windows: []*accountsv1.QuotaWindow{
				{Name: "300m", Utilization: 1.0, ResetsAt: testAccResetTime, Status: client.QuotaStatusRejected},
			},
		},
		{
			Vendor: vendorsv1.Vendor_VENDOR_AGY, State: want[2].State, Login: want[2].Login, QuotaKnown: false,
		},
	}
	return want, protoAccs
}

func seedAccountsFake(t *testing.T) (*accountsFake, []client.Account) {
	t.Helper()
	want, protoAccs := seedAccountsFixtures()
	pausedUpdate := want[1]
	pausedUpdate.State = client.AccountStateOK
	pausedUpdate.PausedUntil = ""

	loginStart := client.LoginProgress{Vendor: client.VendorCodex, LoginID: testAccLoginID, State: client.LoginStateStarted}
	loginWait := client.LoginProgress{Vendor: client.VendorCodex, LoginID: testAccLoginID, State: client.LoginStateWaiting, Prompt: testAccPromptRaw}

	fake := &accountsFake{
		accounts: protoAccs,
		topics: map[string][]*hostv1.SubscribeResponse{
			client.TopicQuota: {
				{Kind: &hostv1.SubscribeResponse_Snapshot{Snapshot: &hostv1.SnapshotEvent{
					Sequence: 1, IncarnationId: testAccInc,
					State: mustJSONBytes(t, map[string]any{"accounts": want}),
				}}},
				{Kind: &hostv1.SubscribeResponse_Event{Event: &hostv1.HostEvent{
					Sequence: 2, Type: client.EventQuotaAccountChanged, Payload: mustJSONBytes(t, pausedUpdate),
				}}},
				{Kind: &hostv1.SubscribeResponse_End{End: &hostv1.StreamEnd{}}},
			},
			client.TopicLogin: {
				{Kind: &hostv1.SubscribeResponse_Snapshot{Snapshot: &hostv1.SnapshotEvent{
					Sequence: 1, IncarnationId: testAccInc,
					State: mustJSONBytes(t, map[string]any{"logins": []client.LoginProgress{loginStart}}),
				}}},
				{Kind: &hostv1.SubscribeResponse_Event{Event: &hostv1.HostEvent{
					Sequence: 2, Type: client.EventLoginProgress, Payload: mustJSONBytes(t, loginWait),
				}}},
				{Kind: &hostv1.SubscribeResponse_End{End: &hostv1.StreamEnd{}}},
			},
		},
	}
	return fake, want
}

func TestAccountsClientMethodsOverRealSocket(t *testing.T) {
	t.Parallel()
	fake, wantAccounts := seedAccountsFake(t)
	c := serveAccountsFake(t, fake)

	gotAccounts, err := c.ListAccounts(t.Context())
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if diff := cmp.Diff(wantAccounts, gotAccounts); diff != "" {
		t.Fatalf("ListAccounts mismatch (-want +got):\n%s", diff)
	}

	res, err := c.StartLogin(t.Context(), client.VendorCodex)
	if err != nil || res.LoginID != testAccLoginID {
		t.Fatalf("StartLogin(codex) = (%+v, %v)", res, err)
	}
	if _, agyErr := c.StartLogin(t.Context(), client.VendorAGY); agyErr == nil {
		t.Fatal("StartLogin(agy) succeeded, want typed error")
	} else if code, ok := client.AccountsCodeOf(agyErr); !ok || code != "failed_precondition" {
		t.Fatalf("StartLogin(agy) code = (%q, %v), want failed_precondition", code, ok)
	}

	if cancelErr := c.CancelLogin(t.Context(), client.VendorCodex); cancelErr != nil {
		t.Fatalf("CancelLogin(codex): %v", cancelErr)
	}
	if claudeErr := c.CancelLogin(t.Context(), client.VendorClaude); claudeErr == nil {
		t.Fatal("CancelLogin(claude) succeeded, want typed not_found")
	} else if code, ok := client.AccountsCodeOf(claudeErr); !ok || code != "not_found" {
		t.Fatalf("CancelLogin(claude) code = (%q, %v), want not_found", code, ok)
	}

	assertQuotaTopicStream(t, c, wantAccounts)
	assertLoginTopicStream(t, c)
}

func assertQuotaTopicStream(t *testing.T, c *client.Client, want []client.Account) {
	t.Helper()
	stream, err := c.Subscribe(t.Context(), client.TopicQuota, nil)
	if err != nil {
		t.Fatalf("Subscribe quota: %v", err)
	}
	defer func() {
		if cerr := stream.Close(); cerr != nil {
			t.Errorf("close quota stream: %v", cerr)
		}
	}()

	snapMsg := recvMsg(t, stream)
	snap, err := client.DecodeQuotaSnapshot(snapMsg.Snapshot.State)
	if err != nil || snapMsg.Snapshot.Incarnation != testAccInc {
		t.Fatalf("quota snapshot = %+v, err=%v", snapMsg.Snapshot, err)
	}
	if diff := cmp.Diff(want, snap); diff != "" {
		t.Fatalf("decoded quota snapshot (-want +got):\n%s", diff)
	}

	evMsg := recvMsg(t, stream)
	changed, err := client.DecodeQuotaAccountChanged(evMsg.Event.Payload)
	if err != nil || evMsg.Event.Type != client.EventQuotaAccountChanged {
		t.Fatalf("quota event = %+v, err=%v", evMsg.Event, err)
	}
	if changed.Vendor != client.VendorCodex || changed.State != client.AccountStateOK || changed.PausedUntil != "" {
		t.Fatalf("decoded quota account changed = %+v", changed)
	}
	if _, endErr := stream.Recv(); !errors.Is(endErr, client.ErrStreamEnded) {
		t.Fatalf("quota final Recv = %v, want ErrStreamEnded", endErr)
	}
}

func assertLoginTopicStream(t *testing.T, c *client.Client) {
	t.Helper()
	stream, err := c.Subscribe(t.Context(), client.TopicLogin, nil)
	if err != nil {
		t.Fatalf("Subscribe login: %v", err)
	}
	defer func() {
		if cerr := stream.Close(); cerr != nil {
			t.Errorf("close login stream: %v", cerr)
		}
	}()

	snapMsg := recvMsg(t, stream)
	logins, err := client.DecodeLoginSnapshot(snapMsg.Snapshot.State)
	if err != nil || len(logins) != 1 || logins[0].State != client.LoginStateStarted {
		t.Fatalf("login snapshot = %+v, err=%v", logins, err)
	}

	evMsg := recvMsg(t, stream)
	prog, err := client.DecodeLoginProgress(evMsg.Event.Payload)
	if err != nil || evMsg.Event.Type != client.EventLoginProgress {
		t.Fatalf("login event = %+v, err=%v", evMsg.Event, err)
	}
	if prog.Vendor != client.VendorCodex || prog.State != client.LoginStateWaiting || prog.Prompt != testAccPromptRaw {
		t.Fatalf("decoded login progress = %+v", prog)
	}
	if _, endErr := stream.Recv(); !errors.Is(endErr, client.ErrStreamEnded) {
		t.Fatalf("login final Recv = %v, want ErrStreamEnded", endErr)
	}
}
