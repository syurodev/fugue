package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
)

const (
	testModelGPT54      = "gpt-5.4"
	testResumeUUID      = "019a8000-0000-7000-8000-000000000001"
	defaultTestCodexBin = "/usr/bin/codex"
)

func TestCodexSessionArgvDenylistNeverAppears(t *testing.T) {
	t.Parallel()

	want := []string{defaultTestCodexBin, "app-server"}
	specs := []vendor.StartSpec{
		{},
		{Model: testModelGPT54},
		{Effort: "high"},
		{ResumeSessionID: testResumeUUID},
		{Model: testModelGPT54, Effort: "medium", ResumeSessionID: testResumeUUID},
	}
	for i := range specs {
		assertAllowedSpecArgv(t, &specs[i], want)
	}

	for _, denied := range DenylistedFlags() {
		assertDeniedFlagRejected(t, denied)
	}
}

func assertAllowedSpecArgv(t *testing.T, spec *vendor.StartSpec, want []string) {
	t.Helper()
	argv, err := BuildArgv(defaultTestCodexBin, spec)
	if err != nil {
		t.Fatalf("BuildArgv(%+v): %v", spec, err)
	}
	if diff := cmp.Diff(want, argv); diff != "" {
		t.Fatalf("argv mismatch (-want +got):\n%s", diff)
	}
	for _, arg := range argv {
		if IsDenylistedFlag(arg) {
			t.Fatalf("denylisted flag %q appeared in argv %v", arg, argv)
		}
	}
}

func assertDeniedFlagRejected(t *testing.T, denied string) {
	t.Helper()
	if _, err := BuildArgv(defaultTestCodexBin, &vendor.StartSpec{Model: denied}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("model=%q: expected invalid_argument, got %v", denied, err)
	}
	if _, err := BuildArgv(defaultTestCodexBin, &vendor.StartSpec{Effort: denied}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("effort=%q: expected invalid_argument, got %v", denied, err)
	}
	if _, err := BuildArgv(defaultTestCodexBin, &vendor.StartSpec{ResumeSessionID: denied}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("resume=%q: expected invalid_argument, got %v", denied, err)
	}
}

func TestCodexSessionEnvIsAllowlistedWithoutHostSecrets(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	hostEnv := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/testuser",
		"USER=testuser",
		"LOGNAME=testuser",
		"LANG=en_US.UTF-8",
		"TERM=xterm-256color",
		"FUGUE_SECRET_KEY=super-secret-vault-token-12345",
		"FUGUE_VAULT_KEY=super-secret-vault-passphrase",
		"OPENAI_API_KEY=sk-proj-live-secret-never-leak",
		"ANTHROPIC_API_KEY=sk-ant-live-secret-never-leak",
		"GITHUB_TOKEN=ghp_secretTokenDoNotPassToProcess",
	}

	spec := vendor.StartSpec{
		Cwd:          t.TempDir(),
		Home:         vendor.AccountHome{ConfigDir: fakeCheckEnvConfigDir},
		HostEnv:      hostEnv,
		SessionID:    "session-env-allowlist",
		SessionToken: redact.NewSecret("tok_session_1"),
	}
	env := SessionEnv(&spec)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "HOME", "USER", "LOGNAME", "LANG", "TERM", "CODEX_HOME", vendor.SessionTokenEnv:
		default:
			t.Fatalf("unexpected variable %q in SessionEnv", key)
		}
	}
	if envValue(env, vendor.SessionTokenEnv) != "tok_session_1" {
		t.Fatal("expected FUGUE_SESSION_TOKEN in SessionEnv")
	}

	adapter := NewAdapter(hostEnv, WithBinary(fakeCodexBinary(t)))
	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeTestSession(t, sess)
}

func TestCodexSessionStartSendSteerInterrupt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	adapter := NewAdapter(nil, WithBinary(fakeCodexBinary(t)))
	sess, err := adapter.Start(ctx, &vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-start-send-steer-interrupt",
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeTestSession(t, sess)

	codexSess := assertSessionAccessors(t, ctx, sess)

	turn, err := sess.Send(ctx, vendor.Input{Text: "hello codex"})
	if err != nil || turn.ID == "" {
		t.Fatalf("Send = (%+v, %v)", turn, err)
	}
	if _, bErr := sess.Send(ctx, vendor.Input{Text: "second"}); !errors.Is(bErr, vendor.ErrBusy) {
		t.Fatalf("concurrent Send err = %v, want ErrBusy", bErr)
	}
	if sErr := sess.Steer(ctx, turn, vendor.Input{Text: "adjust focus"}); sErr != nil {
		t.Fatalf("Steer failed: %v", sErr)
	}
	if sErr := sess.Steer(ctx, vendor.TurnRef{ID: "wrong-turn"}, vendor.Input{Text: "wrong"}); !errors.Is(sErr, vendor.ErrTurnNotRunning) {
		t.Fatalf("mismatched Steer err = %v, want ErrTurnNotRunning", sErr)
	}
	if iErr := sess.Interrupt(ctx, turn); iErr != nil {
		t.Fatalf("Interrupt failed: %v", iErr)
	}
	assertSteeredAndInterruptedObservations(t, codexSess.Observations())
}

func assertSessionAccessors(t *testing.T, ctx context.Context, sess vendor.Session) *Session {
	t.Helper()
	codexSess, ok := sess.(*Session)
	if !ok {
		t.Fatalf("expected *Session, got %T", sess)
	}
	if codexSess.PID() <= 0 || codexSess.Identity().PID <= 0 || len(codexSess.SlashCommands()) != 2 {
		t.Fatalf("session accessors: pid=%d id=%+v cmds=%v", codexSess.PID(), codexSess.Identity(), codexSess.SlashCommands())
	}
	if _, tErr := codexSess.TreeEmpty(); tErr != nil {
		t.Fatalf("TreeEmpty: %v", tErr)
	}
	_ = codexSess.StderrRing()
	if rErr := codexSess.ReadRateLimits(ctx); rErr != nil {
		t.Fatalf("ReadRateLimits: %v", rErr)
	}
	return codexSess
}

func assertSteeredAndInterruptedObservations(t *testing.T, obsCh <-chan Observation) {
	t.Helper()
	var sawSteerDelta, sawInterruptedEnd bool
	for obs := range obsCh {
		if obs.Partial != nil && obs.Partial.Fragment == "STEERED" {
			sawSteerDelta = true
		}
		if obs.TurnEnd != nil && obs.TurnEnd.Outcome == OutcomeInterrupted {
			sawInterruptedEnd = true
			break
		}
	}
	if !sawSteerDelta || !sawInterruptedEnd {
		t.Fatalf("sawSteerDelta=%v sawInterruptedEnd=%v, want both true", sawSteerDelta, sawInterruptedEnd)
	}
}

func TestCodexStopKillsASetsidChildTree(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	pidFile := filepath.Join(t.TempDir(), "setsid.pid")
	clk := clock.NewFake(testEpoch())
	adapter := NewAdapter(nil, WithClock(clk), WithBinary(fakeCodexBinary(t)))
	sess, err := adapter.Start(ctx, &vendor.StartSpec{
		Cwd:       t.TempDir(),
		Home:      vendor.AccountHome{ConfigDir: fakeSetsidChildConfigDir + ":" + pidFile},
		SessionID: "session-setsid-stop",
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	codexSess, ok := sess.(*Session)
	if !ok {
		t.Fatalf("expected *Session, got %T", sess)
	}

	grandchildPID := waitForSetsidPIDFile(t, pidFile)
	t.Cleanup(func() {
		if kErr := syscall.Kill(grandchildPID, syscall.SIGKILL); kErr != nil && !errors.Is(kErr, syscall.ESRCH) {
			t.Logf("cleanup kill %d: %v", grandchildPID, kErr)
		}
	})

	testutil.Poll(t, "the session to own the grandchild", func() bool {
		clk.Advance(100 * time.Millisecond)
		owned, oErr := codexSess.OwnsPID(grandchildPID)
		return oErr == nil && owned
	})

	closeErrCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.codex_setsid_close", func(taskCtx context.Context) error {
		_, cErr := sess.Close(taskCtx, vendor.CloseUserRequest)
		closeErrCh <- cErr
		return nil
	})
	if err := driveFakeClock(ctx, clk, closeErrCh); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	testutil.Poll(t, "the setsid child to be killed on Close", func() bool {
		return errors.Is(syscall.Kill(grandchildPID, 0), syscall.ESRCH)
	})
}

func waitForSetsidPIDFile(t *testing.T, pidFile string) int {
	t.Helper()
	pid := 0
	testutil.Poll(t, "the setsid child pid file", func() bool {
		raw, err := os.ReadFile(filepath.Clean(pidFile))
		if err != nil {
			return false
		}
		n, pErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		pid = n
		return pErr == nil && n > 0
	})
	return pid
}

func sleepReal(d time.Duration) {
	tm := testutil.OSClock().NewTimer(d)
	<-tm.C()
	tm.Stop()
}

func TestCodexSessionInterruptEscalatesAndHandshakeTimeoutStops(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	// 1. Handshake timeout stops the process and returns ErrHandshakeTimeout.
	clk := clock.NewFake(testEpoch())
	adapter := NewAdapter(nil, WithClock(clk), WithBinary(fakeCodexBinary(t)))
	startErrCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.codex_handshake_timeout", func(taskCtx context.Context) error {
		_, err := adapter.Start(taskCtx, &vendor.StartSpec{
			Cwd:       t.TempDir(),
			Home:      vendor.AccountHome{ConfigDir: fakeNoInitConfigDir},
			SessionID: "session-handshake-timeout",
		})
		startErrCh <- err
		return nil
	})
	startErr := driveFakeClock(ctx, clk, startErrCh)
	if !errors.Is(startErr, vendor.ErrHandshakeTimeout) {
		t.Fatalf("expected ErrHandshakeTimeout, got %v", startErr)
	}

	// 2. Frame too large closes the session with process.ErrFrameTooLarge.
	sessFrame, err := NewAdapter(nil, WithBinary(fakeCodexBinary(t))).Start(ctx, &vendor.StartSpec{
		Cwd:       t.TempDir(),
		Home:      vendor.AccountHome{ConfigDir: fakeFrameTooLargeConfigDir},
		SessionID: "session-frame-too-large",
	})
	if !errors.Is(err, process.ErrFrameTooLarge) {
		if sessFrame != nil {
			closeTestSession(t, sessFrame)
		}
		t.Fatalf("expected ErrFrameTooLarge on oversized handshake frame, got %v", err)
	}
}

func TestCodexSessionResolveAnswersOnceAndDeduplicatesRetriedRequest(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	rpcDir := t.TempDir()
	const rpcLogName = "resolve-rpcs.jsonl"
	rpcLog := filepath.Join(rpcDir, rpcLogName)
	adapter := NewAdapter(nil, WithBinary(fakeCodexBinary(t)))
	sess, err := adapter.Start(ctx, &vendor.StartSpec{
		Cwd:       t.TempDir(),
		Home:      vendor.AccountHome{ConfigDir: fakeRecordRPCsConfigDir + ":" + rpcLog},
		SessionID: "session-resolve-once",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			closeTestSession(t, sess)
		}
	}()

	codexSess, ok := sess.(*Session)
	if !ok {
		t.Fatalf("expected *Session, got %T", sess)
	}
	assertDeduplicatedInitialRequest(t, codexSess)
	assertAdditionalServerRequestResolutions(t, codexSess)
	closeTestSession(t, sess)
	closed = true
	assertResolvedRPCLog(t, rpcDir, rpcLogName)
}

func assertDeduplicatedInitialRequest(t *testing.T, codexSess *Session) {
	t.Helper()
	ctx := t.Context()
	frame := Frame{
		Kind:        FrameServerRequest,
		ID:          json.RawMessage(`0`),
		CanonicalID: "0",
		Method:      MethodCommandApproval,
		Params:      json.RawMessage(`{"threadId":"th-1","turnId":"t-1","itemId":"call-1","command":"ls"}`),
	}
	if !codexSess.registerServerRequest(&frame) {
		t.Fatal("first registerServerRequest should succeed")
	}
	if codexSess.registerServerRequest(&frame) {
		t.Fatal("retried server request with same ID must not register twice")
	}
	if err := codexSess.Resolve(ctx, vendor.ApprovalRef{ID: "0"}, vendor.Decision{Allow: false, Message: DecisionDecline}); err != nil {
		t.Fatalf("first Resolve failed: %v", err)
	}
	if err := codexSess.Resolve(ctx, vendor.ApprovalRef{ID: "0"}, vendor.Decision{Allow: true, Message: DecisionAccept}); errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("second Resolve err = %v, want failed_precondition", err)
	}
}

func assertResolvedRPCLog(t *testing.T, rpcDir, rpcLogName string) {
	t.Helper()
	root, oErr := os.OpenRoot(rpcDir)
	if oErr != nil {
		t.Fatalf("OpenRoot(%s): %v", rpcDir, oErr)
	}
	defer func() {
		if cErr := root.Close(); cErr != nil {
			t.Errorf("close root: %v", cErr)
		}
	}()
	rawRPCLog, rErr := root.ReadFile(rpcLogName)
	if rErr != nil {
		t.Fatalf("read rpcLog: %v", rErr)
	}
	logText := string(rawRPCLog)
	for _, wantSubstr := range []string{
		`"id":4,"result":{"answers":{"q1":{"answers":["yes"]}}}`,
		`"id":5,"result":{"answers":{}}`,
		`"id":8,"result":{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}}`,
		`"id":9,"error":{"code":-32000,"message":"Codex asked for secret input, which Fugue cannot relay yet"}`,
	} {
		if !strings.Contains(logText, wantSubstr) {
			t.Fatalf("rpcLog missing %s\ngot:\n%s", wantSubstr, logText)
		}
	}
}

type serverRequestResolutionCase struct {
	id, method, params string
	decision           vendor.Decision
}

func assertAdditionalServerRequestResolutions(t *testing.T, codexSess *Session) {
	t.Helper()
	ctx := t.Context()
	cases := []serverRequestResolutionCase{
		{"1", MethodFileChangeApproval, `{"threadId":"th-1","turnId":"t-1","itemId":"fc-1"}`, vendor.Decision{Allow: true}},
		{"2", MethodPermissionsApproval, `{"threadId":"th-1","turnId":"t-1","itemId":"p-1","cwd":"/tmp","permissions":{"network":true}}`, vendor.Decision{Allow: true, Message: PermissionScopeSession}},
		{"3", MethodPermissionsApproval, `{"threadId":"th-1","turnId":"t-1","itemId":"p-2","cwd":"/tmp","permissions":{"network":true}}`, vendor.Decision{Allow: false, Message: DecisionDecline}},
		{"4", MethodRequestUserInput, `{"threadId":"th-1","turnId":"t-1","itemId":"q-1","questions":[{"id":"q1","header":"H","question":"Q?"},{"id":"q2","header":"H2","question":"Q2?"}]}`, vendor.Decision{Allow: true, Message: "answered", Answers: map[string][]string{"q1": {"yes"}}}},
		{"5", MethodRequestUserInput, `{"threadId":"th-1","turnId":"t-1","itemId":"q-2","questions":[{"id":"q2","header":"H","question":"Q?"}]}`, vendor.Decision{Allow: false, Message: "denied"}},
		{"6", MethodCommandApproval, `{"threadId":"th-1","turnId":"t-1","itemId":"c-6"}`, vendor.Decision{Error: errcode.New(errcode.FailedPrecondition, "relay rejected")}},
		{"8", MethodCommandApproval, `{"threadId":"th-1","turnId":"t-1","itemId":"c-8","proposedExecpolicyAmendment":["git","status"]}`, vendor.Decision{Allow: true, Message: DecisionAcceptWithExecpolicyAmendment}},
	}
	for _, tc := range cases {
		frame := Frame{Kind: FrameServerRequest, ID: json.RawMessage(tc.id), CanonicalID: tc.id, Method: tc.method, Params: json.RawMessage(tc.params)}
		if !codexSess.registerServerRequest(&frame) {
			t.Fatalf("registerServerRequest(%s) returned false", tc.id)
		}
		if err := codexSess.Resolve(ctx, vendor.ApprovalRef{ID: tc.id}, tc.decision); err != nil {
			t.Fatalf("Resolve(%s) failed: %v", tc.id, err)
		}
	}
	secretFrame := Frame{
		Kind: FrameServerRequest, ID: json.RawMessage(`9`), CanonicalID: "9", Method: MethodRequestUserInput,
		Params: json.RawMessage(`{"threadId":"th-1","turnId":"t-1","itemId":"q-sec","questions":[{"id":"s1","header":"H","question":"SECRET-LOG-CANARY","isSecret":true}]}`),
	}
	if !codexSess.registerServerRequest(&secretFrame) {
		t.Fatal("registerServerRequest(9) returned false")
	}
	secObs := codexSess.normalizer.NormalizeFrame(&secretFrame)
	codexSess.rejectUnrelayedServerRequest(secretFrame.CanonicalID, unrelayedServerRequestMessage(&secObs))

	// Register one more pending request so Close exercises rejectPendingApprovalsLocked.
	pendingFrame := Frame{Kind: FrameServerRequest, ID: json.RawMessage(`7`), CanonicalID: "7", Method: MethodCommandApproval, Params: json.RawMessage(`{"threadId":"th-1","turnId":"t-1","itemId":"c-7"}`)}
	codexSess.registerServerRequest(&pendingFrame)
}

func TestCodexSessionClosedAndBuildArgvGuards(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	for _, tc := range []struct {
		bin  string
		spec vendor.StartSpec
	}{
		{bin: "", spec: vendor.StartSpec{}},
		{bin: defaultTestCodexBin, spec: vendor.StartSpec{Model: "bad model with spaces"}},
		{bin: defaultTestCodexBin, spec: vendor.StartSpec{Effort: "extreme"}},
		{bin: defaultTestCodexBin, spec: vendor.StartSpec{ResumeSessionID: "not-a-uuid"}},
	} {
		if _, err := BuildArgv(tc.bin, &tc.spec); errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Fatalf("BuildArgv(%q, %+v) err = %v, want InvalidArgument", tc.bin, tc.spec, err)
		}
	}

	adapter := NewAdapter(nil, WithBinary(fakeCodexBinary(t)))
	sess, err := adapter.Start(ctx, &vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-closed-guards",
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	codexSess, ok := sess.(*Session)
	if !ok {
		t.Fatalf("expected *Session, got %T", sess)
	}
	if _, cErr := sess.Close(ctx, vendor.CloseUserRequest); cErr != nil {
		t.Fatalf("Close failed: %v", cErr)
	}
	// Second Close is idempotent.
	if _, cErr := sess.Close(ctx, vendor.CloseUserRequest); cErr != nil {
		t.Fatalf("second Close failed: %v", cErr)
	}
	if _, sErr := sess.Send(ctx, vendor.Input{Text: "after close"}); !errors.Is(sErr, vendor.ErrSessionClosed) {
		t.Fatalf("Send after close = %v, want ErrSessionClosed", sErr)
	}
	if sErr := sess.Steer(ctx, vendor.TurnRef{ID: "t-1"}, vendor.Input{Text: "after close"}); !errors.Is(sErr, vendor.ErrSessionClosed) {
		t.Fatalf("Steer after close = %v, want ErrSessionClosed", sErr)
	}
	if iErr := sess.Interrupt(ctx, vendor.TurnRef{ID: "t-1"}); !errors.Is(iErr, vendor.ErrSessionClosed) {
		t.Fatalf("Interrupt after close = %v, want ErrSessionClosed", iErr)
	}
	if rErr := codexSess.ReadRateLimits(ctx); !errors.Is(rErr, vendor.ErrSessionClosed) {
		t.Fatalf("ReadRateLimits after close = %v, want ErrSessionClosed", rErr)
	}
}

func driveFakeClock(ctx context.Context, clk *clock.Fake, errCh <-chan error) error {
	for {
		select {
		case err := <-errCh:
			return err
		case <-ctx.Done():
			return errcode.Wrap(errcode.Canceled, "drive fake clock canceled", ctx.Err())
		default:
		}
		if clk.Pending() > 0 {
			clk.Advance(100 * time.Millisecond)
			// Yield real wall time between fake ticks so subprocess signal handlers settle under -race.
			sleepReal(5 * time.Millisecond)
		} else {
			runtime.Gosched()
		}
	}
}
