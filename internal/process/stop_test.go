//go:build unix

package process

import (
	"context"
	"fmt"
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
	clk.Advance(10 * time.Second)

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
	clk.Advance(10 * time.Second)

	if step := <-stepCh; step != escalationSigint {
		t.Fatalf("expected sigint step, got %q", step)
	}
	clk.Advance(5 * time.Second)

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
	clk.Advance(10 * time.Second)

	if step := <-stepCh; step != escalationSigint {
		t.Fatalf("expected sigint step, got %q", step)
	}
	clk.Advance(5 * time.Second)

	if step := <-stepCh; step != escalationSigterm {
		t.Fatalf("expected sigterm step, got %q", step)
	}
	clk.Advance(5 * time.Second)

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
