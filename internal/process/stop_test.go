//go:build unix

package process

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

const (
	escalationInterrupt = "interrupt"
	escalationSigint    = "sigint"
	escalationSigterm   = "sigterm"
	escalationSigkill   = "sigkill"
)

func testEpoch() time.Time {
	return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
}

func advancePending(clk *clock.Fake, d time.Duration) {
	for clk.Pending() == 0 {
		runtime.Gosched()
	}
	clk.Advance(d)
}

func TestStopOrderImmediateInterrupt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)

	var proc *Process
	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, "protocol-interrupt"},
		Clock: clk,
		Interrupt: func(ctx context.Context) error {
			if _, werr := fmt.Fprintln(proc.stdin, "interrupt"); werr != nil {
				return fmt.Errorf("write interrupt: %w", werr)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	waitReady(t, proc.Stdout())

	proof, err := proc.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if proof.Status != StopProofUnknown {
		t.Fatalf("expected StopProofUnknown, got %v", proof.Status)
	}
	if clk.Pending() != 0 {
		t.Fatalf("expected 0 pending timers after immediate exit, got %d", clk.Pending())
	}
}

func TestStopOrderEscalatesToSigint(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	stepCh := make(chan string, 10)

	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, fakeVendorModeNormal},
		Clock: clk,
		Interrupt: func(ctx context.Context) error {
			return nil
		},
		OnEscalation: func(step string) {
			stepCh <- step
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	waitReady(t, proc.Stdout())

	stopDone := make(chan struct{})
	_ = supervise.Go(ctx, "test.stop_sigint", func(ctx context.Context) error {
		if _, serr := proc.Stop(ctx); serr != nil {
			t.Errorf("Stop: %v", serr)
		}
		close(stopDone)
		return nil
	})

	if step := <-stepCh; step != escalationInterrupt {
		t.Fatalf("expected interrupt step, got %q", step)
	}
	advancePending(clk, 10*time.Second)

	if step := <-stepCh; step != escalationSigint {
		t.Fatalf("expected sigint step, got %q", step)
	}

	select {
	case <-stopDone:
		// normal process exits on SIGINT
	case <-ctx.Done():
		t.Fatal("process failed to exit on sigint")
	}
}

func TestStopOrderEscalatesToSigterm(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	stepCh := make(chan string, 10)

	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, "ignore-sigint"},
		Clock: clk,
		Interrupt: func(ctx context.Context) error {
			return nil
		},
		OnEscalation: func(step string) {
			stepCh <- step
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	waitReady(t, proc.Stdout())

	stopDone := make(chan struct{})
	_ = supervise.Go(ctx, "test.stop_sigterm", func(ctx context.Context) error {
		if _, serr := proc.Stop(ctx); serr != nil {
			t.Errorf("Stop: %v", serr)
		}
		close(stopDone)
		return nil
	})

	if step := <-stepCh; step != escalationInterrupt {
		t.Fatalf("expected interrupt step, got %q", step)
	}
	advancePending(clk, 10*time.Second)

	if step := <-stepCh; step != escalationSigint {
		t.Fatalf("expected sigint step, got %q", step)
	}
	advancePending(clk, 5*time.Second)

	if step := <-stepCh; step != escalationSigterm {
		t.Fatalf("expected sigterm step, got %q", step)
	}

	select {
	case <-stopDone:
		// process exits on SIGTERM
	case <-ctx.Done():
		t.Fatal("process failed to exit on sigterm")
	}
}

func TestStopOrderEscalatesToSigkill(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	stepCh := make(chan string, 10)

	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, "ignore-sigterm"},
		Clock: clk,
		Interrupt: func(ctx context.Context) error {
			return nil
		},
		OnEscalation: func(step string) {
			stepCh <- step
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	waitReady(t, proc.Stdout())

	stopDone := make(chan struct{})
	_ = supervise.Go(ctx, "test.stop_sigkill", func(ctx context.Context) error {
		if _, serr := proc.Stop(ctx); serr != nil {
			t.Errorf("Stop: %v", serr)
		}
		close(stopDone)
		return nil
	})

	if step := <-stepCh; step != escalationInterrupt {
		t.Fatalf("expected interrupt step, got %q", step)
	}
	advancePending(clk, 10*time.Second)

	if step := <-stepCh; step != escalationSigint {
		t.Fatalf("expected sigint step, got %q", step)
	}
	advancePending(clk, 5*time.Second)

	if step := <-stepCh; step != escalationSigterm {
		t.Fatalf("expected sigterm step, got %q", step)
	}
	advancePending(clk, 5*time.Second)

	if step := <-stepCh; step != escalationSigkill {
		t.Fatalf("expected sigkill step, got %q", step)
	}

	select {
	case <-stopDone:
		// killed by SIGKILL
	case <-ctx.Done():
		t.Fatal("process failed to exit on sigkill")
	}
}

var errCustomInterruptFailure = errors.New("custom interrupt failure")

func TestStopKillsGroupMembersAfterLeaderExits(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	stepCh := make(chan string, 10)

	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, "group-child-survivor"},
		Clock: clk,
		OnEscalation: func(step string) {
			stepCh <- step
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	childPid := waitChildSurvivorReady(t, proc.Stdout())
	if err := syscall.Kill(childPid, 0); err != nil {
		t.Fatalf("child process %d not alive before stop: %v", childPid, err)
	}

	stopDone := make(chan struct{})
	var stopProof StopProof
	var stopErr error
	_ = supervise.Go(ctx, "test.stop_group_child", func(ctx context.Context) error {
		stopProof, stopErr = proc.Stop(ctx)
		close(stopDone)
		return nil
	})

	expectEscalationStep(t, stepCh, escalationSigint)
	<-proc.exitCh
	advancePending(clk, 5*time.Second)

	expectEscalationStep(t, stepCh, escalationSigterm)
	advancePending(clk, 5*time.Second)

	expectEscalationStep(t, stepCh, escalationSigkill)

	select {
	case <-stopDone:
	case <-ctx.Done():
		t.Fatal("Stop did not complete after sigkill")
	}

	if stopErr != nil {
		t.Fatalf("Stop error: %v", stopErr)
	}
	if stopProof.Reason != ReasonGroupEmpty {
		t.Fatalf("expected group_empty, got %q", stopProof.Reason)
	}

	if err := syscall.Kill(childPid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("expected child pid %d to be ESRCH, got %v", childPid, err)
	}
}

func waitChildSurvivorReady(t *testing.T, lr *LineReader) int {
	t.Helper()
	childLine, err := lr.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine child: %v", err)
	}
	childPidStr := strings.TrimPrefix(string(childLine), "CHILD ")
	childPid, err := strconv.Atoi(childPidStr)
	if err != nil || childPid <= 0 {
		t.Fatalf("invalid child pid from %q: %v", string(childLine), err)
	}
	waitReady(t, lr)
	return childPid
}

func expectEscalationStep(t *testing.T, ch <-chan string, expected string) {
	t.Helper()
	if step := <-ch; step != expected {
		t.Fatalf("expected %s step, got %q", expected, step)
	}
}

func TestStopOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	stepCh := make(chan string, 10)

	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, fakeVendorModeNormal},
		Clock: clk,
		OnEscalation: func(step string) {
			stepCh <- step
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitReady(t, proc.Stdout())

	cancelingCtx, cancel := context.WithCancel(ctx)
	cancel()

	proof, err := proc.Stop(cancelingCtx)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if proof.Reason != ReasonContextCanceled {
		t.Fatalf("expected context_canceled, got %q", proof.Reason)
	}
	expectEscalationStep(t, stepCh, escalationSigkill)
}

func TestStopInterruptErrorReturnedSeparately(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)

	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, fakeVendorModeNormal},
		Clock: clk,
		Interrupt: func(ctx context.Context) error {
			return errCustomInterruptFailure
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitReady(t, proc.Stdout())

	proof, err := proc.Stop(ctx)
	if err == nil {
		t.Fatal("expected error from failed interrupt")
	}
	if !errors.Is(err, errCustomInterruptFailure) {
		t.Fatalf("expected wrapped interrupt failure, got %v", err)
	}
	if proof.Reason != ReasonInterruptFailed {
		t.Fatalf("expected interrupt_failed fixed reason code, got %q", proof.Reason)
	}
}
