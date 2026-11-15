package agy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
)

func newTestAdapter(t *testing.T, clk clock.Clock) *Adapter {
	t.Helper()
	return NewAdapter(
		[]string{"PATH=/usr/bin:/bin", "HOME=" + fakeHome, "LANG=C"},
		WithBinary(fakeAgyBinary(t)),
		WithClock(clk),
		WithIDs(ids.NewSequence("agy")),
	)
}

func startFakeSession(t *testing.T, configDir, resumeID string) (*Session, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(testEpoch())
	adapter := newTestAdapter(t, clk)
	sess, err := adapter.Start(t.Context(), &vendor.StartSpec{
		SessionID:       "fugue-sess-1",
		Cwd:             t.TempDir(),
		Home:            vendor.AccountHome{ConfigDir: configDir},
		HostEnv:         adapter.HostEnv,
		ResumeSessionID: resumeID,
	})
	if err != nil {
		t.Fatalf("Start(%q): %v", configDir, err)
	}
	typed, ok := sess.(*Session)
	if !ok {
		t.Fatalf("unexpected session type %T", sess)
	}
	return typed, clk
}

const (
	testAgyBin   = "/usr/bin/agy"
	testAgyModel = "gemini-3.8-flash"
)

func closeTestSession(t *testing.T, sess *Session) {
	t.Helper()
	if _, err := sess.Close(t.Context(), vendor.CloseHostStop); err != nil {
		t.Errorf("close agy session: %v", err)
	}
}

func TestAgyHandshakeMatchesThePinnedCapabilities(t *testing.T) {
	t.Parallel()
	sess, _ := startFakeSession(t, "", "")
	defer closeTestSession(t, sess)

	assertHandshakeSessionState(t, sess)

	// Unsupported protocolVersion must fail initialize cleanly.
	clk := clock.NewFake(testEpoch())
	badAdapter := newTestAdapter(t, clk)
	if _, err := badAdapter.Start(t.Context(), &vendor.StartSpec{
		SessionID: "bad-proto",
		Cwd:       t.TempDir(),
		Home:      vendor.AccountHome{ConfigDir: fakeBadProtoConfigDir},
		HostEnv:   badAdapter.HostEnv,
	}); errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("bad protocolVersion err = %v, want internal", err)
	}
}

func assertHandshakeSessionState(t *testing.T, sess *Session) {
	t.Helper()
	want := HandshakeCapabilities{
		ProtocolVersion:       ProtocolVersion,
		LoadSession:           true,
		Resume:                true,
		Close:                 true,
		List:                  true,
		AdditionalDirectories: true,
		ImagePrompt:           true,
		EmbeddedContextPrompt: true,
	}
	if caps := sess.HandshakeCapabilities(); caps != want {
		t.Fatalf("HandshakeCapabilities = %+v, want %+v", caps, want)
	}
	if sess.Models() == nil || sess.Models().CurrentModelID != testAgyModel {
		t.Fatalf("Models = %+v, want %s from session/new", sess.Models(), testAgyModel)
	}
	if sess.Modes() == nil || sess.Modes().CurrentModeID != "default" {
		t.Fatalf("Modes = %+v, want default mode from session/new", sess.Modes())
	}
	if len(sess.ConfigOptions()) == 0 {
		t.Fatal("expected ConfigOptions from session/new")
	}
}

func TestAgySessionStartPromptCancelLoad(t *testing.T) {
	t.Parallel()
	verifyPromptAndEndTurn(t)
	verifyCancelSession(t)
	verifyLoadSession(t)
}

func verifyPromptAndEndTurn(t *testing.T) {
	t.Helper()
	sess, _ := startFakeSession(t, "", "")
	turn, err := sess.Send(t.Context(), vendor.Input{Text: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	awaitPromptPartialAndEnd(t, sess, turn.ID)
	if steerErr := sess.Steer(t.Context(), turn, vendor.Input{Text: "steer"}); !errors.Is(steerErr, vendor.ErrUnsupported) {
		t.Fatalf("Steer = %v, want ErrUnsupported", steerErr)
	}
	if resolveErr := sess.Resolve(t.Context(), vendor.ApprovalRef{ID: "p"}, vendor.Decision{Allow: true}); !errors.Is(resolveErr, vendor.ErrUnsupported) {
		t.Fatalf("Resolve = %v, want ErrUnsupported", resolveErr)
	}
	if _, closeErr := sess.Close(t.Context(), vendor.CloseHostStop); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
}

func awaitPromptPartialAndEnd(t *testing.T, sess *Session, turnID string) {
	t.Helper()
	sawPartial, sawEnd := false, false
	for obs := range sess.Observations() {
		if obs.Partial != nil && obs.Partial.Fragment == "PONG" {
			sawPartial = true
		}
		if obs.TurnEnd != nil && obs.TurnEnd.Outcome == OutcomeCompleted && obs.TurnEnd.TurnID == turnID {
			sawEnd = true
			break
		}
	}
	if !sawPartial || !sawEnd {
		t.Fatalf("sawPartial=%v sawEnd=%v, want both", sawPartial, sawEnd)
	}
}

func verifyCancelSession(t *testing.T) {
	t.Helper()
	cancelSess, _ := startFakeSession(t, fakeCancelWaitConfigDir, "")
	defer closeTestSession(t, cancelSess)
	cancelTurn, err := cancelSess.Send(t.Context(), vendor.Input{Text: "sleep"})
	if err != nil {
		t.Fatalf("cancel Send: %v", err)
	}
	if err := cancelSess.Interrupt(t.Context(), cancelTurn); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	for obs := range cancelSess.Observations() {
		if obs.TurnEnd != nil && obs.TurnEnd.Outcome == OutcomeInterrupted {
			return
		}
	}
	t.Fatal("expected OutcomeInterrupted after session/cancel")
}

func verifyLoadSession(t *testing.T) {
	t.Helper()
	rpcLog := filepath.Join(t.TempDir(), "rpcs.jsonl")
	resumeUUID := "2a8b7877-ac14-403d-a8f7-7063f8164df9"
	loadSess, _ := startFakeSession(t, fakeRecordRPCsConfigDir+":"+rpcLog, resumeUUID)
	defer closeTestSession(t, loadSess)
	rawRPCs := string(readFile(t, rpcLog))
	if !strings.Contains(rawRPCs, `"method":"session/load"`) || !strings.Contains(rawRPCs, resumeUUID) {
		t.Fatalf("expected session/load RPC with %s in %s", resumeUUID, rawRPCs)
	}
}

func TestAgyPermissionRequestIsAnErrorUntilRelayed(t *testing.T) {
	t.Parallel()
	rpcLog := filepath.Join(t.TempDir(), "perm-rpcs.jsonl")
	sess, _ := startFakeSession(t, fakePermissionConfigDir+":"+rpcLog, "")
	if _, err := sess.Send(t.Context(), vendor.Input{Text: "run command"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	awaitPermUnsupportedNotice(t, sess)
	if _, err := sess.Close(t.Context(), vendor.CloseHostStop); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertPermErrorWirePayloads(t, string(readFile(t, rpcLog)))
}

func awaitPermUnsupportedNotice(t *testing.T, sess *Session) {
	t.Helper()
	for obs := range sess.Observations() {
		if obs.ServerRequest != nil {
			t.Fatalf("F9.8a must not emit ServerRequest, got %+v", obs.ServerRequest)
		}
		for i := range obs.Records {
			if obs.Records[i].Kind == transcript.Notice && obs.Records[i].RawRef == "acp:"+MethodSessionRequestPermission {
				return
			}
		}
	}
	t.Fatal("expected unsupported_event notice for session/request_permission")
}

func assertPermErrorWirePayloads(t *testing.T, rawRPCs string) {
	t.Helper()
	if !strings.Contains(rawRPCs, `"id":"perm-1"`) || !strings.Contains(rawRPCs, `"error"`) ||
		strings.Contains(rawRPCs, `"selected"`) || strings.Contains(rawRPCs, `"cancelled"`) {
		t.Fatalf("unexpected stdin wire messages for session/request_permission:\n%s", rawRPCs)
	}
	respBytes, err := NewCodec().EncodeResponseError(json.RawMessage(`"perm-1"`), RPCErrorMethodNotSupported, RPCErrorPermissionNotRelayed)
	if err != nil {
		t.Fatal(err)
	}
	respStr := string(respBytes)
	if !strings.Contains(respStr, `"error"`) || strings.Contains(respStr, `"result"`) {
		t.Fatalf("permission response = %s, want JSON-RPC error only", respStr)
	}
}

func TestAgySessionArgvDenylistNeverAppears(t *testing.T) {
	t.Parallel()
	argv, err := BuildArgv(testAgyBin, &vendor.StartSpec{Model: testAgyModel, Effort: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(argv, []string{testAgyBin, acpArg}) {
		t.Fatalf("argv = %v, want [%s %s]", argv, testAgyBin, acpArg)
	}
	for _, flag := range []string{"--dangerously-skip-permissions", "--sandbox", "--mode", "--add-dir"} {
		if !IsDenylistedFlag(flag) {
			t.Errorf("%q must be denylisted", flag)
		}
		if _, mErr := BuildArgv(testAgyBin, &vendor.StartSpec{Model: flag}); errcode.CodeOf(mErr) != errcode.InvalidArgument {
			t.Errorf("BuildArgv with denylisted model %q: err=%v, want invalid_argument", flag, mErr)
		}
		if vErr := ValidateArgv([]string{testAgyBin, acpArg, flag}); errcode.CodeOf(vErr) != errcode.InvalidArgument {
			t.Errorf("ValidateArgv(%q): err=%v, want invalid_argument", flag, vErr)
		}
	}
	if err := ValidateArgv([]string{testAgyBin, acpArg, "--unknown-vendor-flag"}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("unknown profile flag err = %v, want invalid_argument", err)
	}
	if err := ValidateArgv([]string{testAgyBin, acpArg, "--disable-slash-commands"}); err != nil {
		t.Fatalf("§7 isolation flag must be accepted: %v", err)
	}
}

func TestAgySessionEnvIsAllowlistedWithoutHostSecrets(t *testing.T) {
	t.Parallel()
	secret := redact.NewSecret("tok_agy_session_secret")
	env := SessionEnv(&vendor.StartSpec{
		HostEnv: []string{
			testPathEnv,
			"HOME=" + fakeHome,
			"USER=user",
			"FUGUE_VAULT_KEY=vault-secret",
			"OPENAI_API_KEY=sk-proj-abc",
			"ANTHROPIC_API_KEY=sk-ant-abc",
			"GEMINI_API_KEY=gem-key",
			"TERM=xterm-256color",
		},
		SessionToken: secret,
	})
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "HOME", "TERM", vendor.SessionTokenEnv:
		default:
			if strings.Contains(key, "VAULT") || strings.Contains(key, "KEY") {
				t.Errorf("secret %s leaked into session env", key)
			}
		}
	}
	if !slices.Contains(env, vendor.SessionTokenEnv+"=tok_agy_session_secret") {
		t.Fatal("expected session token in SessionEnv")
	}
	// Spawn the fake CLI process checking env inside child!
	sess, _ := startFakeSession(t, fakeCheckEnvConfigDir, "")
	if _, err := sess.Close(t.Context(), vendor.CloseHostStop); err != nil {
		t.Fatal(err)
	}
}

func TestAgyStopKillsTheProcessTree(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	sess, clk := startFakeSession(t, fakeSetsidChildConfigDir+":"+pidFile, "")
	pid := waitForPIDFile(t, pidFile)
	t.Cleanup(func() {
		if kErr := syscall.Kill(pid, syscall.SIGKILL); kErr != nil && !errors.Is(kErr, syscall.ESRCH) {
			t.Logf("cleanup kill %d: %v", pid, kErr)
		}
	})

	realClk := clock.Real{}
	deadline := realClk.Now().Add(2 * time.Second)
	for realClk.Now().Before(deadline) {
		clk.Advance(100 * time.Millisecond)
		owned, oErr := sess.OwnsPID(pid)
		if oErr == nil && owned {
			break
		}
		sleepReal(10 * time.Millisecond)
	}

	closeErrCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.agy_setsid_close", func(taskCtx context.Context) error {
		_, cErr := sess.Close(taskCtx, vendor.CloseHostStop)
		closeErrCh <- cErr
		return nil
	})
	if err := driveFakeClock(ctx, clk, closeErrCh); err != nil {
		t.Fatalf("Close: %v", err)
	}

	waitDeadline := realClk.Now().Add(2 * time.Second)
	for realClk.Now().Before(waitDeadline) {
		if kErr := syscall.Kill(pid, 0); errors.Is(kErr, syscall.ESRCH) {
			return
		}
		sleepReal(20 * time.Millisecond)
	}
	t.Fatalf("setsid grandchild %d survived session Close", pid)
}

func TestAgySessionHandshakeTimeoutAndOversizedFrame(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	// Handshake timeout.
	clk := clock.NewFake(testEpoch())
	adapter := newTestAdapter(t, clk)
	errCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.agy_handshake_timeout", func(taskCtx context.Context) error {
		_, sErr := adapter.Start(taskCtx, &vendor.StartSpec{
			SessionID: "timeout-1",
			Cwd:       t.TempDir(),
			Home:      vendor.AccountHome{ConfigDir: fakeNoInitConfigDir},
			HostEnv:   adapter.HostEnv,
		})
		errCh <- sErr
		return nil
	})
	if err := driveFakeClock(ctx, clk, errCh); !errors.Is(err, vendor.ErrHandshakeTimeout) {
		t.Fatalf("handshake timeout err = %v, want ErrHandshakeTimeout", err)
	}

	// Oversized stdio frame fails typed (ErrFrameTooLarge).
	clk2 := clock.NewFake(testEpoch())
	adapter2 := newTestAdapter(t, clk2)
	frameCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.agy_frame_too_large", func(taskCtx context.Context) error {
		_, fErr := adapter2.Start(taskCtx, &vendor.StartSpec{
			SessionID: "frame-1",
			Cwd:       t.TempDir(),
			Home:      vendor.AccountHome{ConfigDir: fakeFrameTooLargeConfigDir},
			HostEnv:   adapter2.HostEnv,
		})
		frameCh <- fErr
		return nil
	})
	if frameErr := driveFakeClock(ctx, clk2, frameCh); errcode.CodeOf(frameErr) != errcode.FrameTooLarge {
		t.Fatalf("frameErr = %v, want frame_too_large", frameErr)
	}
}

func TestAgySessionMetadataGuardsAndInterruptTimeout(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	adapter := newTestAdapter(t, clk)
	rawSess, err := adapter.Start(ctx, &vendor.StartSpec{
		SessionID: "meta-1",
		Cwd:       t.TempDir(),
		HostEnv:   adapter.HostEnv,
		Model:     testAgyModel,
		Effort:    "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := rawSess.(*Session)
	if !ok {
		t.Fatalf("unexpected session type %T", rawSess)
	}
	if empty, treeErr := sess.TreeEmpty(); treeErr != nil || empty {
		t.Fatalf("TreeEmpty = %v, %v; want non-empty running tree", empty, treeErr)
	}
	if sess.PID() <= 0 || sess.Identity().PID != sess.PID() || sess.StderrRing() == nil {
		t.Fatalf("unexpected identity/pid/ring: pid=%d id=%+v", sess.PID(), sess.Identity())
	}
	if iErr := sess.Interrupt(ctx, vendor.TurnRef{ID: "missing"}); !errors.Is(iErr, vendor.ErrTurnNotRunning) {
		t.Fatalf("idle Interrupt = %v, want ErrTurnNotRunning", iErr)
	}
	verifyClosedSessionGuards(t, ctx, sess)
	verifyInterruptTimeoutEscalation(t)
}

func verifyClosedSessionGuards(t *testing.T, ctx context.Context, sess *Session) {
	t.Helper()
	if _, cErr := sess.Close(ctx, vendor.CloseHostStop); cErr != nil {
		t.Fatal(cErr)
	}
	if _, sErr := sess.Send(ctx, vendor.Input{Text: "after close"}); !errors.Is(sErr, vendor.ErrSessionClosed) {
		t.Fatalf("closed Send = %v, want ErrSessionClosed", sErr)
	}
	if iErr := sess.Interrupt(ctx, vendor.TurnRef{ID: "any"}); !errors.Is(iErr, vendor.ErrSessionClosed) {
		t.Fatalf("closed Interrupt = %v, want ErrSessionClosed", iErr)
	}
	if _, cErr := sess.Close(ctx, vendor.CloseHostStop); cErr != nil {
		t.Fatalf("idempotent Close = %v", cErr)
	}
}

func verifyInterruptTimeoutEscalation(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	sess, clk := startFakeSession(t, fakeIgnoreCancelConfigDir, "")
	turn, err := sess.Send(ctx, vendor.Input{Text: "hang"})
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.agy_interrupt_timeout", func(taskCtx context.Context) error {
		errCh <- sess.Interrupt(taskCtx, turn)
		return nil
	})
	if iErr := driveFakeClock(ctx, clk, errCh); errcode.CodeOf(iErr) != errcode.DeadlineExceeded {
		t.Fatalf("escalated Interrupt = %v, want deadline_exceeded", iErr)
	}
}

func waitForPIDFile(t *testing.T, pidFile string) int {
	t.Helper()
	realClk := clock.Real{}
	deadline := realClk.Now().Add(2 * time.Second)
	for realClk.Now().Before(deadline) {
		if raw, err := os.ReadFile(filepath.Clean(pidFile)); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			if pid, pErr := strconv.Atoi(strings.TrimSpace(string(raw))); pErr == nil && pid > 0 {
				return pid
			}
		}
		sleepReal(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for pid file %s", pidFile)
	return 0
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
			clk.Advance(500 * time.Millisecond)
			sleepReal(5 * time.Millisecond)
		} else {
			runtime.Gosched()
		}
	}
}

func sleepReal(d time.Duration) {
	tm := clock.Real{}.NewTimer(d)
	<-tm.C()
	tm.Stop()
}
