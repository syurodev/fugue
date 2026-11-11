package claude

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
)

func drainSteerFixtureObservations(obsCh <-chan Observation) (int, *TurnEnd, bool) {
	var turnEndCount int
	var lastTurnEnd *TurnEnd
	var sawPONG bool
	for obs := range obsCh {
		for i := range obs.Records {
			rec := &obs.Records[i]
			if rec.Kind == transcript.AssistantMessage {
				if content, ok := rec.Content.(transcript.AssistantMessageContent); ok && strings.Contains(content.Text, "PONG") {
					sawPONG = true
				}
			}
		}
		if obs.TurnEnd != nil {
			turnEndCount++
			lastTurnEnd = obs.TurnEnd
			break
		}
	}
	return turnEndCount, lastTurnEnd, sawPONG
}

func getSessionState(s *Session) sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func waitForRunningTurn(s *Session) vendor.TurnRef {
	for {
		s.mu.Lock()
		running := s.state == stateRunning
		currTurn := s.currentTurn
		s.mu.Unlock()
		if running && currTurn != "" {
			return vendor.TurnRef{ID: currTurn}
		}
		runtime.Gosched()
	}
}

func TestSessionSteerThenSubsequentSendWorks(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-test-steer-subsequent",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	claudeSess := asClaudeSession(t, sess)

	// Turn 1: Send steerable prompt
	turn1, err := sess.Send(ctx, vendor.Input{Text: "please steer-me"})
	if err != nil {
		t.Fatalf("Send turn1 failed: %v", err)
	}

	// Steer while running
	err = sess.Steer(ctx, turn1, vendor.Input{Text: "please adjust focus"})
	if err != nil {
		t.Fatalf("Steer failed: %v", err)
	}

	// Wait for Turn 1 to complete
	_, sawTurnEnd1 := drainUntilTurnEnd(claudeSess.Observations())
	if !sawTurnEnd1 {
		t.Fatal("expected TurnEnd for turn 1")
	}

	// Turn 2: Subsequent Send works normally
	turn2, err := sess.Send(ctx, vendor.Input{Text: "subsequent turn"})
	if err != nil {
		t.Fatalf("subsequent Send failed: %v", err)
	}
	if turn2.ID == "" || turn2.ID == turn1.ID {
		t.Fatalf("expected distinct turn ID, got %q vs %q", turn2.ID, turn1.ID)
	}

	_, sawTurnEnd2 := drainUntilTurnEnd(claudeSess.Observations())
	if !sawTurnEnd2 {
		t.Fatal("expected TurnEnd for turn 2")
	}
}

func TestSteerFixtureReplayExactOneTurnEndAndReady(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	fixtureAbs, err := filepath.Abs(filepath.Join("testdata", "fixtures", "claude-2.1.319-session3-steer.jsonl"))
	if err != nil {
		t.Fatalf("resolve fixture path: %v", err)
	}

	adapter := NewAdapter(nil, WithBinary(fakeClaudeExe(t)))
	spec := vendor.StartSpec{
		Cwd: t.TempDir(),
		Home: vendor.AccountHome{
			ConfigDir: fakeReplaySteerFixtureConfigDir + ":" + fixtureAbs,
		},
		SessionID: "session-test-steer-fixture-replay",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	turn, err := sess.Send(ctx, vendor.Input{Text: "Read the file notes.txt with the Read tool, then answer what it warns about."})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if err := sess.Steer(ctx, turn, vendor.Input{Text: "Also include the exact uppercase word PONG in your response."}); err != nil {
		t.Fatalf("Steer failed: %v", err)
	}

	claudeSess := asClaudeSession(t, sess)
	turnEndCount, lastTurnEnd, sawPONG := drainSteerFixtureObservations(claudeSess.Observations())

	if turnEndCount != 1 {
		t.Fatalf("expected exactly 1 TurnEnd in steer fixture replay, got %d", turnEndCount)
	}
	if lastTurnEnd.Outcome != OutcomeCompleted {
		t.Fatalf("expected OutcomeCompleted, got %q", lastTurnEnd.Outcome)
	}
	if !sawPONG {
		t.Fatal("expected assistant message containing PONG")
	}
	if state := getSessionState(claudeSess); state != stateReady {
		t.Fatalf("expected session state to be stateReady, got %d", state)
	}
}

func TestSessionInterruptEscalatesWhenWriteIsWedged(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	adapter := NewAdapter(nil, WithBinary(fakeClaudeExe(t)), WithClock(clk))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		Home:      vendor.AccountHome{ConfigDir: fakeNeverReadStdinConfigDir},
		SessionID: "session-test-interrupt-wedged",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	claudeSess := asClaudeSession(t, sess)
	sendErrCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.send_wedged", func(taskCtx context.Context) error {
		_, sendErr := sess.Send(ctx, vendor.Input{Text: strings.Repeat("A", 1024*1024)})
		sendErrCh <- sendErr
		return nil
	})

	turn := waitForRunningTurn(claudeSess)

	initialPending := clk.Pending()
	intErrCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.interrupt_wedged", func(taskCtx context.Context) error {
		intErrCh <- sess.Interrupt(ctx, turn)
		return nil
	})

	waitAndAdvance(clk, initialPending+1, 10*time.Second)

	intErr := driveUntilErr(ctx, clk, intErrCh)
	if errcode.CodeOf(intErr) != errcode.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded from Interrupt, got %v", intErr)
	}

	if state := getSessionState(claudeSess); state != stateClosed {
		t.Fatalf("expected stateClosed, got %d", state)
	}

	if sendErr := <-sendErrCh; !errors.Is(sendErr, vendor.ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed for wedged Send, got %v", sendErr)
	}
}

func TestProtocolInterruptSkipsWhenWriteBusy(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	adapter := NewAdapter(nil, WithBinary(exe))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		SessionID: "session-test-protocol-interrupt-busy",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	claudeSess := asClaudeSession(t, sess)

	// Simulate writeSem being busy (e.g. held by Send/Steer)
	claudeSess.writeSem <- struct{}{}

	intFn := makeProtocolInterrupt(claudeSess)
	// Must not block and return nil (skip protocol interrupt)
	if err := intFn(ctx); err != nil {
		t.Fatalf("expected nil when write is busy, got %v", err)
	}

	// Release semaphore
	<-claudeSess.writeSem

	// When writeSem is free, makeProtocolInterrupt succeeds
	if err := intFn(ctx); err != nil {
		t.Fatalf("expected nil when write is free, got %v", err)
	}
}

func TestFrameTooLargeAfterStartContextCanceledStillUsesStopOrder(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeClaudeExe(t)

	var escalationSteps []string
	var escMu sync.Mutex

	adapter := NewAdapter(nil,
		WithBinary(exe),
		WithOnEscalation(func(step string) {
			escMu.Lock()
			escalationSteps = append(escalationSteps, step)
			escMu.Unlock()
		}),
	)

	startCtx, cancelStart := context.WithCancel(ctx)
	spec := vendor.StartSpec{
		Cwd: t.TempDir(),
		Home: vendor.AccountHome{
			ConfigDir: fakeFrameTooLargeConfigDir,
		},
		SessionID: "session-test-frame-too-large-start-canceled",
	}

	sess, err := adapter.Start(startCtx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Cancel the context passed to Start immediately after Start succeeds
	cancelStart()

	claudeSess := asClaudeSession(t, sess)
	// Wait for reader to hit frame too large and handleReaderError to stop the process
	<-claudeSess.readerDoneCh

	escMu.Lock()
	steps := slices.Clone(escalationSteps)
	escMu.Unlock()

	if len(steps) == 0 {
		t.Fatal("expected escalation steps to be recorded")
	}
	if steps[0] != controlInterrupt && steps[0] != stepSigint {
		t.Fatalf("expected stop order to start with interrupt or sigint, got %v", steps)
	}

	proof, closeErr := sess.Close(ctx, vendor.CloseHostStop)
	if closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}
	if proof.Reason == process.ReasonContextCanceled {
		t.Fatalf("StopProof must not be ReasonContextCanceled when Start ctx was canceled, got %q", proof.Reason)
	}
}

func recordNoticeIfAbsorbed(rec *transcript.Item, turn2ID string, beforeItems *[]transcript.Item) bool {
	meta, ok := rec.Metadata.(transcript.NoticeMetadata)
	if !ok {
		return false
	}
	if meta.Code == "late_steer" {
		*beforeItems = append(*beforeItems, *rec)
	}
	return meta.Code == "absorbed_into_vendor_turn" && rec.TurnID == turn2ID
}

func drainAbsorbedObservations(obsCh <-chan Observation, turn2ID string) (int, *TurnEnd, bool, []transcript.Item) {
	var turnEndCount int
	var lastTurnEnd *TurnEnd
	var sawNotice bool
	var beforeItems []transcript.Item

	for obs := range obsCh {
		for i := range obs.Records {
			if obs.Records[i].Kind == transcript.Notice && recordNoticeIfAbsorbed(&obs.Records[i], turn2ID, &beforeItems) {
				sawNotice = true
			}
		}
		if obs.TurnEnd != nil {
			turnEndCount++
			lastTurnEnd = obs.TurnEnd
			if obs.TurnEnd.TurnID == turn2ID {
				break
			}
		}
	}
	return turnEndCount, lastTurnEnd, sawNotice, beforeItems
}

func assertAbsorbedResults(
	t *testing.T,
	turn2ID string,
	turnEndCount int,
	lastTurnEnd *TurnEnd,
	sawNotice bool,
	beforeItems []transcript.Item,
	sess *Session,
) {
	t.Helper()
	if turnEndCount != 1 || lastTurnEnd == nil || lastTurnEnd.TurnID != turn2ID {
		t.Fatalf("expected 1 TurnEnd for %s, got count %d, end: %+v", turn2ID, turnEndCount, lastTurnEnd)
	}
	if !sawNotice {
		t.Fatal("expected absorbed_into_vendor_turn notice")
	}
	if len(beforeItems) == 0 {
		t.Fatal("expected items before echo to be recorded")
	}
	for i := range beforeItems {
		item := &beforeItems[i]
		if !strings.HasPrefix(item.TurnID, "vendor-") {
			t.Fatalf("expected vendor turn ID, got %q", item.TurnID)
		}
	}
	if state := getSessionState(sess); state != stateReady {
		t.Fatalf("expected stateReady, got %d", state)
	}
}

func TestSendAbsorbedIntoVendorTurnEndsFugueTurn(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	fixtureAbs, err := filepath.Abs(filepath.Join("testdata", "fixtures", "claude-2.1.319-session4-late-steer.jsonl"))
	if err != nil {
		t.Fatalf("resolve fixture path: %v", err)
	}

	adapter := NewAdapter(nil, WithBinary(fakeClaudeExe(t)))
	spec := vendor.StartSpec{
		Cwd: t.TempDir(),
		Home: vendor.AccountHome{
			ConfigDir: fakeReplayLateSteerAbsorbedConfigDir + ":" + fixtureAbs,
		},
		SessionID: "session-test-send-absorbed",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	turn1, err := sess.Send(ctx, vendor.Input{Text: "Reply with exactly: PING"})
	if err != nil {
		t.Fatalf("Send turn1 failed: %v", err)
	}

	if steerErr := sess.Steer(ctx, turn1, vendor.Input{Text: "Reply with exactly: PONG"}); steerErr != nil {
		t.Fatalf("Steer failed: %v", steerErr)
	}

	claudeSess := asClaudeSession(t, sess)
	obsCh := claudeSess.Observations()
	for obs := range obsCh {
		if obs.TurnEnd != nil && obs.TurnEnd.TurnID == turn1.ID {
			break
		}
	}

	turn2, err := sess.Send(ctx, vendor.Input{Text: "send-t2"})
	if err != nil {
		t.Fatalf("Send turn2 failed: %v", err)
	}

	turnEndCount, lastTurnEnd, sawNotice, beforeItems := drainAbsorbedObservations(obsCh, turn2.ID)
	assertAbsorbedResults(t, turn2.ID, turnEndCount, lastTurnEnd, sawNotice, beforeItems, claudeSess)
}

func TestInterruptWithStaleTurnWhileWriteWedgedDoesNotCloseSession(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	adapter := NewAdapter(nil, WithBinary(fakeClaudeExe(t)), WithClock(clk))
	spec := vendor.StartSpec{
		Cwd:       t.TempDir(),
		Home:      vendor.AccountHome{ConfigDir: fakeNeverReadStdinConfigDir},
		SessionID: "session-test-interrupt-stale-wedged",
	}

	sess, err := adapter.Start(ctx, &spec)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer closeSession(t, ctx, sess)

	claudeSess := asClaudeSession(t, sess)
	sendErrCh := make(chan error, 1)
	_ = supervise.Go(ctx, "test.send_wedged", func(taskCtx context.Context) error {
		_, sendErr := sess.Send(ctx, vendor.Input{Text: strings.Repeat("A", 1024*1024)})
		sendErrCh <- sendErr
		return nil
	})

	_ = waitForRunningTurn(claudeSess)

	staleTurn := vendor.TurnRef{ID: "stale-turn-id"}
	intErr := sess.Interrupt(ctx, staleTurn)
	if !errors.Is(intErr, vendor.ErrTurnNotRunning) {
		t.Fatalf("expected ErrTurnNotRunning immediately, got %v", intErr)
	}

	if state := getSessionState(claudeSess); state != stateRunning {
		t.Fatalf("expected session to remain stateRunning, got %d", state)
	}

	if _, closeErr := sess.Close(ctx, vendor.CloseHostStop); closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}
	sendErr := <-sendErrCh
	if !errors.Is(sendErr, vendor.ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed for wedged send, got %v", sendErr)
	}
}
