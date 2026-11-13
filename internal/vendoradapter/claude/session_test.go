package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

const stepSigint = "sigint"

func testEpoch() time.Time {
	return time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
}

func waitAndAdvance(t *testing.T, clk *clock.Fake, minPending int, d time.Duration) {
	t.Helper()
	if err := clk.BlockUntilPending(t.Context(), minPending); err != nil {
		t.Fatalf("fewer than %d timers armed on the fake clock: %v", minPending, err)
	}
	clk.Advance(d)
}

func driveUntilErr(ctx context.Context, clk *clock.Fake, errCh <-chan error) error {
	for {
		select {
		case err := <-errCh:
			return err
		case <-ctx.Done():
			return fmt.Errorf("context done: %w", ctx.Err())
		default:
		}
		if clk.Pending() > 0 {
			clk.Advance(10 * time.Millisecond)
		} else {
			runtime.Gosched()
		}
	}
}

func asClaudeSession(t *testing.T, s vendor.Session) *Session {
	t.Helper()
	cs, ok := s.(*Session)
	if !ok {
		t.Fatalf("expected *Session, got %T", s)
	}
	return cs
}

func closeSession(t *testing.T, ctx context.Context, s vendor.Session) {
	t.Helper()
	if _, err := s.Close(ctx, vendor.CloseUserRequest); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func drainUntilTurnEnd(obsCh <-chan Observation) (bool, bool) {
	var sawAssistant, sawTurnEnd bool
	for obs := range obsCh {
		if len(obs.Records) > 0 {
			for i := range obs.Records {
				if obs.Records[i].Kind == "assistant_message" {
					sawAssistant = true
				}
			}
		}
		if obs.TurnEnd != nil {
			sawTurnEnd = true
			break
		}
	}
	return sawAssistant, sawTurnEnd
}

func TestSessionStartAndSendEchoTurn(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-test-echo",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	turn, err := sess.Send(ctx, vendor.Input{Text: "hello fake claude"})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if turn.ID == "" {
		t.Fatal("expected non-empty turn ID")
	}

	sawAssistant, sawTurnEnd := drainUntilTurnEnd(asClaudeSession(t, sess).Observations())
	if !sawAssistant {
		t.Error("expected to observe assistant message")
	}
	if !sawTurnEnd {
		t.Error("expected to observe TurnEnd")
	}

	proof, err := sess.Close(ctx, vendor.CloseUserRequest)
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if proof.Status == "" {
		t.Fatal("expected valid StopProof")
	}
}

func TestSessionSendWhileRunningReturnsErrBusy(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-test-busy",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	s := asClaudeSession(t, sess)
	// Simulate active turn state
	s.mu.Lock()
	s.state = stateRunning
	s.currentTurn = "active-turn-1"
	s.mu.Unlock()

	_, err = sess.Send(ctx, vendor.Input{Text: "concurrent message"})
	if !errors.Is(err, vendor.ErrBusy) {
		t.Fatalf("expected ErrBusy when turn is active, got %v", err)
	}
}

func TestSessionSteerMidTurn(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-test-steer",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	turn, err := sess.Send(ctx, vendor.Input{Text: "please steer-me"})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Steer with correct turn ID succeeds
	err = sess.Steer(ctx, turn, vendor.Input{Text: "please adjust focus"})
	if err != nil {
		t.Fatalf("Steer failed: %v", err)
	}

	// Steer with mismatched turn ID fails typed
	err = sess.Steer(ctx, vendor.TurnRef{ID: "mismatched-turn"}, vendor.Input{Text: "wrong turn"})
	if !errors.Is(err, vendor.ErrTurnNotRunning) {
		t.Fatalf("expected ErrTurnNotRunning for mismatched turn ID, got %v", err)
	}
}

func TestSessionSteerWhenNotRunningFailsTyped(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-test-steer-idle",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	// In ready state with no turn running, Steer fails
	err = sess.Steer(ctx, vendor.TurnRef{ID: "idle-turn"}, vendor.Input{Text: "steer while idle"})
	if !errors.Is(err, vendor.ErrTurnNotRunning) {
		t.Fatalf("expected ErrTurnNotRunning when session is idle, got %v", err)
	}
}

func TestSessionInterruptSuccess(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-test-interrupt-success",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	turn, err := sess.Send(ctx, vendor.Input{Text: "please interrupt-me"})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// In fake CLI, sending control message interrupt emits result and closes turn
	err = sess.Interrupt(ctx, turn)
	if err != nil {
		t.Fatalf("Interrupt failed: %v", err)
	}
}

func TestSessionInterruptEscalatesToStopWhenCLIHangs(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeClaudeExe(t)

	stepCh := make(chan string, 10)
	adapter := NewAdapter(nil,
		WithClock(clk),
		WithOnEscalation(func(step string) { stepCh <- step }),
		WithBinary(exe),
	)
	spec := vendor.StartSpec{
		Cwd: t.TempDir(),
		Home: vendor.AccountHome{
			ConfigDir: fakeIgnoreInterruptConfigDir,
		},
		SessionID: "session-test-interrupt-escalates",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	turn, err := sess.Send(ctx, vendor.Input{Text: "please interrupt-me and hang"})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	interruptErrCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.interrupt", func(taskCtx context.Context) error {
		interruptErrCh <- sess.Interrupt(taskCtx, turn)
		return nil
	})

	// Advance clock past 10s interrupt wait on Session.Interrupt (minPending: tracker + interrupt timer)
	waitAndAdvance(t, clk, 2, 10*time.Second)

	// Stop escalation steps in F9.2 stop order
	if step := <-stepCh; step != controlInterrupt {
		t.Fatalf("expected interrupt step, got %q", step)
	}
	waitAndAdvance(t, clk, 1, 10*time.Second)

	if step := <-stepCh; step != stepSigint {
		t.Fatalf("expected sigint step, got %q", step)
	}

	intErr := driveUntilErr(ctx, clk, interruptErrCh)
	if errcode.CodeOf(intErr) != errcode.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded on interrupt escalation, got %v", intErr)
	}

	// Session is now closed
	_, err = sess.Send(ctx, vendor.Input{Text: "after escalation"})
	if !errors.Is(err, vendor.ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed after escalation, got %v", err)
	}
}

func TestSessionHandshakeTimeoutStopsProcess(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeClaudeExe(t)

	stepCh := make(chan string, 10)
	adapter := NewAdapter(nil,
		WithClock(clk),
		WithOnEscalation(func(step string) { stepCh <- step }),
		WithBinary(exe),
	)
	spec := vendor.StartSpec{
		Cwd: t.TempDir(),
		Home: vendor.AccountHome{
			ConfigDir: fakeNoInitConfigDir,
		},
		SessionID: "session-test-handshake-timeout",
	}

	startErrCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.start_handshake_timeout", func(taskCtx context.Context) error {
		_, err := adapter.Start(taskCtx, &spec)
		startErrCh <- err
		return nil
	})

	// Advance clock past 30s handshake timeout (minPending: tracker + handshake timer)
	waitAndAdvance(t, clk, 2, 30*time.Second)

	// Allow stop escalation
	if step := <-stepCh; step != controlInterrupt {
		t.Fatalf("expected interrupt step, got %q", step)
	}
	waitAndAdvance(t, clk, 1, 10*time.Second)

	if step := <-stepCh; step != stepSigint {
		t.Fatalf("expected sigint step, got %q", step)
	}

	startErr := driveUntilErr(ctx, clk, startErrCh)
	if !errors.Is(startErr, vendor.ErrHandshakeTimeout) {
		t.Fatalf("expected ErrHandshakeTimeout, got %v", startErr)
	}
}

func TestSessionFrameTooLargeFailsAndCloses(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd: t.TempDir(),
		Home: vendor.AccountHome{
			ConfigDir: fakeFrameTooLargeConfigDir,
		},
		SessionID: "session-test-frame-too-large",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	s := asClaudeSession(t, sess)
	// Wait for reader to encounter oversized line and terminate
	<-s.readerDoneCh

	// Subsequent Send fails with ErrFrameTooLarge
	_, err = sess.Send(ctx, vendor.Input{Text: "after frame too large"})
	if !errors.Is(err, process.ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestSessionResolveReturnsErrUnsupported(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-test-resolve",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	err = sess.Resolve(ctx, vendor.ApprovalRef{ID: "appr-1"}, vendor.Decision{Allow: true})
	if !errors.Is(err, vendor.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported from Resolve, got %v", err)
	}
}

func TestSessionCloseIdempotentAndStopsProcess(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-test-close-idempotent",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	proof1, err1 := sess.Close(ctx, vendor.CloseUserRequest)
	if err1 != nil {
		t.Fatalf("first Close failed: %v", err1)
	}

	proof2, err2 := sess.Close(ctx, vendor.CloseUserRequest)
	if err2 != nil {
		t.Fatalf("second Close failed: %v", err2)
	}

	if proof1 != proof2 {
		t.Fatalf("expected identical StopProof from Close, got %v vs %v", proof1, proof2)
	}

	// Methods after Close return ErrSessionClosed
	if _, err := sess.Send(ctx, vendor.Input{Text: "closed"}); !errors.Is(err, vendor.ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed from Send after close, got %v", err)
	}
	if err := sess.Steer(ctx, vendor.TurnRef{ID: "t"}, vendor.Input{Text: "closed"}); !errors.Is(err, vendor.ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed from Steer after close, got %v", err)
	}
	if err := sess.Interrupt(ctx, vendor.TurnRef{ID: "t"}); !errors.Is(err, vendor.ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed from Interrupt after close, got %v", err)
	}
	if err := sess.Resolve(ctx, vendor.ApprovalRef{ID: "a"}, vendor.Decision{}); !errors.Is(err, vendor.ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed from Resolve after close, got %v", err)
	}
}

func TestSessionUntestedVersionWarnsButRuns(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd: t.TempDir(),
		Home: vendor.AccountHome{
			ConfigDir: fakeUntestedVersionConfigDir,
		},
		SessionID: "session-test-untested-version",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start with untested version should succeed with warning, got: %v", err)
	}
	defer closeSession(t, ctx, sess)
}

func TestSessionStartRelativeBinaryFailsTyped(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	adapter := NewAdapter(nil, WithBinary("relative/path/claude"))

	_, err := adapter.Start(ctx, &vendor.StartSpec{})
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument for relative BinaryPath, got %v", err)
	}
}

func TestSessionStartBinaryNotFoundFailsTyped(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	adapter := &Adapter{
		Binary: filepath.Join(t.TempDir(), "nonexistent-claude-bin"),
	}

	_, err := adapter.Start(ctx, &vendor.StartSpec{})
	if errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("expected NotFound for nonexistent binary, got %v", err)
	}
}

func waitForRunningState(s *Session) {
	for {
		s.mu.Lock()
		running := s.state == stateRunning
		s.mu.Unlock()
		if running {
			return
		}
		runtime.Gosched()
	}
}

func TestSessionCloseUnblocksWhenSendBlocked(t *testing.T) {
	t.Parallel()
	testExe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}

	clk := clock.NewFake(testEpoch())
	adapter := NewAdapter(nil, WithClock(clk), WithBinary(testExe))
	ctx := t.Context()
	sess, err := adapter.Start(ctx, &vendor.StartSpec{
		SessionID: "session-blocked-write",
		Home: vendor.AccountHome{
			ConfigDir: fakeNeverReadStdinConfigDir,
		},
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	largeText := strings.Repeat("A", 1024*1024)
	sendErrCh := make(chan error, 1)
	supervise.Go(ctx, "test.large_send", func(taskCtx context.Context) error {
		_, sErr := sess.Send(taskCtx, vendor.Input{Text: largeText})
		sendErrCh <- sErr
		return nil
	})

	waitForRunningState(asClaudeSession(t, sess))

	closeDone := make(chan error, 1)
	supervise.Go(ctx, "test.close_session", func(taskCtx context.Context) error {
		_, cErr := sess.Close(taskCtx, vendor.CloseUserRequest)
		closeDone <- cErr
		return nil
	})

	if err := driveUntilErr(ctx, clk, closeDone); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	select {
	case sErr := <-sendErrCh:
		if sErr == nil {
			t.Fatal("expected Send to fail with typed error, got nil")
		}
		if !errors.Is(sErr, vendor.ErrSessionClosed) {
			t.Fatalf("expected ErrSessionClosed, got %v", sErr)
		}
	case <-ctx.Done():
		t.Fatal("Send did not return after Close")
	}
}

func TestSessionNormalizerRace(t *testing.T) {
	t.Parallel()
	testExe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}

	adapter := NewAdapter(nil, WithBinary(testExe))
	ctx := t.Context()
	sess, err := adapter.Start(ctx, &vendor.StartSpec{
		SessionID: "session-race",
		Home: vendor.AccountHome{
			ConfigDir: fakeContinuousOutputConfigDir,
		},
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	claudeSess := asClaudeSession(t, sess)
	supervise.Go(ctx, "test.drain_obs", func(taskCtx context.Context) error {
		for range claudeSess.Observations() {
		}
		return nil
	})

	for range 20 {
		turn, sErr := sess.Send(ctx, vendor.Input{Text: "ping"})
		if sErr != nil {
			t.Fatalf("Send failed: %v", sErr)
		}
		if err := sess.Interrupt(ctx, turn); err != nil && !errors.Is(err, vendor.ErrTurnNotRunning) {
			t.Fatalf("Interrupt failed: %v", err)
		}
		runtime.Gosched()
	}
}

func TestSessionInterruptContextCanceledDoesNotCloseSession(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-test-interrupt-canceled",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	turn, err := sess.Send(ctx, vendor.Input{Text: "please interrupt-me"})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Create a context that is canceled immediately
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()

	intErr := sess.Interrupt(cancelCtx, turn)
	if !errors.Is(intErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", intErr)
	}

	// Session is NOT closed: verify state is not stateClosed
	claudeSess := asClaudeSession(t, sess)
	claudeSess.mu.Lock()
	state := claudeSess.state
	claudeSess.mu.Unlock()
	if state == stateClosed {
		t.Fatal("session must not be closed when interrupt ctx is canceled")
	}

	// After result arrives from the fake CLI for the interrupt, session becomes ready
	_, sawTurnEnd := drainUntilTurnEnd(claudeSess.Observations())
	if !sawTurnEnd {
		t.Fatal("expected TurnEnd from interrupted turn")
	}

	// Subsequent Send works normally
	turn2, err := sess.Send(ctx, vendor.Input{Text: "second prompt"})
	if err != nil {
		t.Fatalf("subsequent Send failed: %v", err)
	}
	if turn2.ID == "" {
		t.Fatal("expected non-empty turn ID")
	}
}
