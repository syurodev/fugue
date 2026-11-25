package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/pkg/client"
)

const (
	testInc1    = "inc-acc-1"
	testInc2    = "inc-acc-2"
	testResetAt = "2026-11-20T18:00:00Z"
	testPrompt  = "Visit https://auth.openai.com/codex/device and enter CODE-42"
)

var errScriptedAccounts = errors.New("scripted accounts error")

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type fakeAccountsClient struct {
	mu          sync.Mutex
	inc         string
	accounts    []client.Account
	listErr     error
	startErr    error
	cancelErr   error
	submitErr   error
	startCalls  []string
	cancelCalls []string
	submitCalls []string
}

func (f *fakeAccountsClient) HostStatus(context.Context) (client.HostStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return client.HostStatus{Incarnation: f.inc}, nil
}

func (f *fakeAccountsClient) ListAccounts(context.Context) ([]client.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accounts, f.listErr
}

func (f *fakeAccountsClient) StartLogin(_ context.Context, vendor string) (client.StartLoginResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls = append(f.startCalls, vendor)
	if f.startErr != nil {
		return client.StartLoginResult{}, f.startErr
	}
	return client.StartLoginResult{LoginID: "login-" + vendor}, nil
}

func (f *fakeAccountsClient) CancelLogin(_ context.Context, vendor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls = append(f.cancelCalls, vendor)
	return f.cancelErr
}

func (f *fakeAccountsClient) SubmitLoginCode(_ context.Context, commandID, loginID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitCalls = append(f.submitCalls, commandID+":"+loginID)
	return f.submitErr
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func awaitAccountsResult(t *testing.T, w *Worker) Result {
	t.Helper()
	select {
	case res := <-w.Results():
		return res
	case <-t.Context().Done():
		t.Fatalf("context ended waiting for accounts result: %v", t.Context().Err())
		return Result{}
	}
}

func TestAccountsModelAppliesQuotaAndLoginTopics(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if m.HasSnapshot() || len(m.Accounts()) != 0 {
		t.Fatalf("fresh model should be empty without guessed states: %+v", m.Accounts())
	}
	assertInitialAccountsSnapshot(t, m)
	assertQuotaAndLoginEvents(t, m)
	assertIncarnationResetAndReconnect(t, m)
	assertWorkerExecutesOffThread(t)
}

func assertInitialAccountsSnapshot(t *testing.T, m *Model) {
	t.Helper()
	snapAccounts := []client.Account{
		{
			Vendor: client.VendorClaude, State: client.AccountStateOK, Login: client.AccountLoginLoggedIn, QuotaKnown: true,
			QuotaGate: 0.78,
			Windows:   []client.QuotaWindow{{Name: "five_hour", Utilization: 0.35, ResetsAt: testResetAt, Status: client.QuotaStatusAllowed}},
		},
		{Vendor: client.VendorCodex, State: client.AccountStateNeedsLogin, Login: client.AccountLoginLoggedOut, QuotaKnown: true, QuotaGate: 0},
		{Vendor: client.VendorAGY, State: "weird_future_state", Login: client.AccountLoginUnknown, QuotaKnown: false, QuotaGate: -0.5},
	}
	m.ApplyListAccounts(testInc1, snapAccounts)
	if !m.HasSnapshot() || m.Incarnation() != testInc1 {
		t.Fatalf("after ApplyListAccounts: hasSnapshot=%v inc=%q", m.HasSnapshot(), m.Incarnation())
	}
	if claudeAcc, ok := m.Account(client.VendorClaude); !ok || claudeAcc.QuotaGate != 0.78 {
		t.Fatalf("claude quota_gate = %+v, want 0.78", claudeAcc)
	}
	if agyAcc, ok := m.Account(client.VendorAGY); !ok || agyAcc.State != AccountStateUnknown || agyAcc.QuotaKnown || agyAcc.QuotaGate != 0 {
		t.Fatalf("unknown state must stay unknown without guessing and clamp negative gate: %+v", agyAcc)
	}
	if sanitizeQuotaGate(math.NaN()) != 0 || sanitizeQuotaGate(1.5) != 1 {
		t.Fatal("sanitizeQuotaGate did not bound NaN and >1")
	}
}

func assertQuotaAndLoginEvents(t *testing.T, m *Model) {
	t.Helper()
	pausedCodex := client.Account{
		Vendor: client.VendorCodex, State: client.AccountStatePaused, PausedUntil: testResetAt,
		Login: client.AccountLoginLoggedIn, QuotaKnown: true, QuotaGate: 0.80,
		Windows: []client.QuotaWindow{{Name: "300m", Utilization: 1.0, ResetsAt: testResetAt, Status: client.QuotaStatusRejected}},
	}
	if err := m.ApplyQuotaEvent(client.EventQuotaAccountChanged, mustJSON(t, pausedCodex)); err != nil {
		t.Fatal(err)
	}
	if got, ok := m.Account(client.VendorCodex); !ok || got.QuotaGate != 0.80 {
		t.Fatalf("codex quota_gate after event = %+v, want 0.80", got)
	}
	assertLoginEventSequence(t, m)
}

func assertLoginEventSequence(t *testing.T, m *Model) {
	t.Helper()
	if err := m.ApplyLoginEvent(client.EventLoginProgress, mustJSON(t, client.LoginProgress{
		Vendor: client.VendorCodex, LoginID: "l-1", State: client.LoginStateWaiting, Prompt: testPrompt,
	})); err != nil {
		t.Fatal(err)
	}
	if !m.IsLoginRunning(client.VendorCodex) {
		t.Fatal("codex login should be running in waiting state")
	}
	if got, ok := m.Login(client.VendorCodex); !ok || got.Prompt != testPrompt {
		t.Fatalf("login entry = (%+v, %v), want verbatim prompt", got, ok)
	}

	if err := m.ApplyLoginEvent(client.EventLoginProgress, mustJSON(t, client.LoginProgress{
		Vendor: client.VendorClaude, LoginID: "l-2", State: client.LoginStateNeedsTerminal,
		Prompt: "https://claude.ai/oauth/authorize?code=1",
	})); err != nil {
		t.Fatal(err)
	}
	if m.IsLoginRunning(client.VendorClaude) {
		t.Fatal("needs_terminal is terminal, should not report running")
	}

	// On reconnect to the same incarnation, a login snapshot drops stale
	// running entries not in the snapshot but keeps terminal entries.
	if err := m.ApplyLoginSnapshot(testInc1, mustJSON(t, map[string]any{"logins": []client.LoginProgress{}})); err != nil {
		t.Fatal(err)
	}
	if m.IsLoginRunning(client.VendorCodex) {
		t.Fatal("stale running login should clear when empty login snapshot arrives")
	}
	if claudeLogin, ok := m.Login(client.VendorClaude); !ok || claudeLogin.State != client.LoginStateNeedsTerminal {
		t.Fatalf("terminal login entry should remain until dismissed: %+v", claudeLogin)
	}
}

func assertIncarnationResetAndReconnect(t *testing.T, m *Model) {
	t.Helper()
	newSnap := []client.Account{{Vendor: client.VendorClaude, State: client.AccountStateOK, Login: client.AccountLoginLoggedIn, QuotaKnown: true}}
	if err := m.ApplyQuotaSnapshot(testInc2, mustJSON(t, map[string]any{"accounts": newSnap})); err != nil {
		t.Fatal(err)
	}
	if m.Incarnation() != testInc2 {
		t.Fatalf("incarnation = %q, want %q", m.Incarnation(), testInc2)
	}
	if _, ok := m.Login(client.VendorClaude); ok {
		t.Fatal("old incarnation login state should clear on new host incarnation")
	}
	if got := m.Accounts(); len(got) != 1 || got[0].Vendor != client.VendorClaude || got[0].State != client.AccountStateOK || !slices.Equal(got[0].Windows, newSnap[0].Windows) {
		t.Fatalf("reconciled accounts = %+v, want %+v", got, newSnap)
	}
}

func assertWorkerExecutesOffThread(t *testing.T) {
	t.Helper()
	wNil := NewWorker(func() Client { return nil }, nil)
	wNil.Start(t.Context())
	defer wNil.Stop()
	wNil.Do(&Request{Kind: OpListAccounts})
	if res := awaitAccountsResult(t, wNil); !errors.Is(res.Err, client.ErrDisconnected) {
		t.Fatalf("nil client err = %v, want ErrDisconnected", res.Err)
	}

	fc := &fakeAccountsClient{
		inc:      testInc2,
		accounts: []client.Account{{Vendor: client.VendorCodex, State: client.AccountStateOK, Login: client.AccountLoginLoggedIn, QuotaKnown: true}},
	}
	var notified atomic.Int32
	w := NewWorker(func() Client { return fc }, func() { notified.Add(1) })
	w.Start(t.Context())
	defer w.Stop()

	w.Do(&Request{Kind: OpListAccounts})
	res := awaitAccountsResult(t, w)
	if res.Err != nil || res.Incarnation != testInc2 || len(res.Accounts) != 1 {
		t.Fatalf("OpListAccounts result = %+v", res)
	}

	w.Do(&Request{Kind: OpStartLogin, Vendor: client.VendorCodex})
	res = awaitAccountsResult(t, w)
	if res.Err != nil || res.StartLogin.LoginID != "login-codex" {
		t.Fatalf("OpStartLogin result = %+v", res)
	}

	w.Do(&Request{Kind: OpCancelLogin, Vendor: client.VendorCodex})
	res = awaitAccountsResult(t, w)
	if res.Err != nil || res.Vendor != client.VendorCodex {
		t.Fatalf("OpCancelLogin result = %+v", res)
	}
	if notified.Load() != 3 {
		t.Fatalf("onResult called %d times, want 3", notified.Load())
	}

	assertUnsupportedStartLogin(t)
}

func assertUnsupportedStartLogin(t *testing.T) {
	t.Helper()
	m := NewModel()
	m.BeginStartLogin(client.VendorAGY)
	m.ApplyStartLoginResult(&Result{Kind: OpStartLogin, Vendor: client.VendorAGY, Code: "failed_precondition", Err: errScriptedAccounts})
	if e, ok := m.Login(client.VendorAGY); !ok || e.State != LoginStateUnsupported || e.Active() {
		t.Fatalf("failed_precondition login entry = %+v, want unsupported", e)
	}
}

func TestSubmitLoginCodeModelRetryReusesCommandIDAndWorker(t *testing.T) {
	t.Parallel()
	assertSubmitLoginCodeModelRetry(t)
	assertSubmitLoginCodeWorker(t)
}

func assertSubmitLoginCodeModelRetry(t *testing.T) {
	t.Helper()
	m := NewModel()
	m.BeginStartLogin(client.VendorClaude)
	m.ApplyStartLoginResult(&Result{
		Kind:       OpStartLogin,
		Vendor:     client.VendorClaude,
		StartLogin: client.StartLoginResult{LoginID: "login-claude-1"},
	})

	cmd1 := m.BeginSubmitLoginCode(client.VendorClaude, "cmd-1")
	if cmd1 != "cmd-1" {
		t.Fatalf("first cmdID = %q, want cmd-1", cmd1)
	}
	m.ApplySubmitLoginCodeResult(&Result{Kind: OpSubmitLoginCode, Vendor: client.VendorClaude, CommandID: cmd1, Err: errScriptedAccounts})
	if e, _ := m.Login(client.VendorClaude); e.CodeError == "" || e.CodeSent {
		t.Fatalf("after failed submit = %+v, want CodeError and !CodeSent", e)
	}

	retryCmd := m.BeginSubmitLoginCode(client.VendorClaude, "cmd-2")
	if retryCmd != "cmd-1" {
		t.Fatalf("retry cmdID = %q, want cmd-1", retryCmd)
	}
	m.ApplySubmitLoginCodeResult(&Result{Kind: OpSubmitLoginCode, Vendor: client.VendorClaude, CommandID: retryCmd, Code: "unavailable", Err: errScriptedAccounts})
	if retryUnavailable := m.BeginSubmitLoginCode(client.VendorClaude, "cmd-unavailable"); retryUnavailable != "cmd-1" {
		t.Fatalf("retry after unavailable cmdID = %q, want cmd-1", retryUnavailable)
	}
	m.ApplySubmitLoginCodeResult(&Result{Kind: OpSubmitLoginCode, Vendor: client.VendorClaude, CommandID: retryCmd, Code: "invalid_argument", Err: errScriptedAccounts})
	freshAfterInvalid := m.BeginSubmitLoginCode(client.VendorClaude, "cmd-invalid")
	if freshAfterInvalid != "cmd-invalid" {
		t.Fatalf("after invalid_argument cmdID = %q, want cmd-invalid", freshAfterInvalid)
	}
	m.ApplySubmitLoginCodeResult(&Result{Kind: OpSubmitLoginCode, Vendor: client.VendorClaude, CommandID: freshAfterInvalid})
	if e, _ := m.Login(client.VendorClaude); !e.CodeSent || e.CodeError != "" || e.PendingCodeCommandID != "" {
		t.Fatalf("after successful submit = %+v, want CodeSent and cleared pending command_id", e)
	}

	if nextCmd := m.BeginSubmitLoginCode(client.VendorClaude, "cmd-3"); nextCmd != "cmd-3" {
		t.Fatalf("next submit cmdID = %q, want cmd-3", nextCmd)
	}
}

func assertSubmitLoginCodeWorker(t *testing.T) {
	t.Helper()
	fc := &fakeAccountsClient{inc: testInc1}
	w := NewWorker(func() Client { return fc }, nil)
	w.Start(t.Context())
	defer w.Stop()
	w.Do(&Request{Kind: OpSubmitLoginCode, Vendor: client.VendorClaude, CommandID: "cmd-w", LoginID: "login-claude-1", Code: "secret#state"})
	res := awaitAccountsResult(t, w)
	if res.Err != nil || res.CommandID != "cmd-w" {
		t.Fatalf("worker OpSubmitLoginCode = %+v", res)
	}
	fc.mu.Lock()
	calls := slices.Clone(fc.submitCalls)
	fc.mu.Unlock()
	if len(calls) != 1 || calls[0] != "cmd-w:login-claude-1" {
		t.Fatalf("submitCalls = %+v, want [cmd-w:login-claude-1]", calls)
	}
}
