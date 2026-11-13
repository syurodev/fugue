package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

const (
	testOpSession = "session-1"
	testOpClose   = "close"
	testOpSend    = "send"
)

func newTestVendorOps(t *testing.T, clk clock.Clock) *vendorOps {
	t.Helper()
	return newVendorOps(clk, func(name string, fn func(context.Context) error) *supervise.Task {
		task := supervise.Go(t.Context(), name, fn)
		t.Cleanup(func() {
			if err := task.Wait(); err != nil {
				t.Errorf("vendor command task: %v", err)
			}
		})
		return task
	})
}

// A close whose caller stops waiting keeps running; the retry with the same
// command_id attaches to it and the stop runs exactly once (Q-56 A).
func TestRetrySameCommandIDAttachesToInFlightClose(t *testing.T) {
	t.Parallel()
	ops := newTestVendorOps(t, clock.NewFake(time.Unix(0, 0)))
	release := make(chan struct{})
	var runs atomic.Int32
	closeOp := func(opCtx context.Context) (VendorSessionView, error) {
		runs.Add(1)
		select {
		case <-release:
		case <-opCtx.Done():
			t.Error("the close operation inherited the caller's cancellation")
		}
		return VendorSessionView{ID: testOpSession, State: testStateClosed}, nil
	}
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if _, done, err := ops.run(expired, "close-1", []string{testOpClose, testOpSession}, time.Minute, closeOp); err != nil || done {
		t.Fatalf("first close = done %v, err %v; want pending", done, err)
	}
	close(release)
	view, done, err := ops.run(t.Context(), "close-1", []string{testOpClose, testOpSession}, time.Minute, closeOp)
	if err != nil || !done || view.State != testStateClosed {
		t.Fatalf("retried close = %+v, done %v, err %v", view, done, err)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("close ran %d times, want once", got)
	}
	_, _, err = ops.run(t.Context(), "close-1", []string{testOpClose, "session-2"}, time.Minute, closeOp)
	if errcode.CodeOf(err) != errcode.VendorCommandConflict {
		t.Fatalf("reused command_id for another session = %v, want command conflict", err)
	}
}

func TestVendorCommandResultsExpireAfterRetention(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	ops := newTestVendorOps(t, clk)
	var runs atomic.Int32
	op := func(context.Context) (VendorSessionView, error) {
		runs.Add(1)
		return VendorSessionView{}, nil
	}
	for range 2 {
		if _, _, err := ops.run(t.Context(), "send-1", []string{testOpSend}, time.Minute, op); err != nil {
			t.Fatal(err)
		}
	}
	clk.Advance(defaultDedupTTL + time.Second)
	if _, _, err := ops.run(t.Context(), "send-1", []string{testOpSend}, time.Minute, op); err != nil {
		t.Fatal(err)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("operation ran %d times, want 2 (once, then again after retention)", got)
	}
	if _, _, err := ops.run(t.Context(), "", []string{testOpSend}, time.Minute, op); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("empty command_id = %v, want invalid_argument", err)
	}
}
