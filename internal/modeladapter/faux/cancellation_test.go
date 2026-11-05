package faux

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/modeladapter"
)

// Cancel while the send select is being evaluated, with the send also ready.
// Both outcomes must classify cancellation and prevent another content send.
type cancelOnSelectContext struct {
	done  chan struct{}
	calls atomic.Int32
}

func (c *cancelOnSelectContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelOnSelectContext) Value(_ any) any             { return nil }
func (c *cancelOnSelectContext) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

func (c *cancelOnSelectContext) Done() <-chan struct{} {
	if c.calls.Add(1) == 2 {
		close(c.done)
	}
	return c.done
}

func TestCancellationDuringReadySendIsNotInterruption(t *testing.T) {
	t.Parallel()
	for range 64 {
		ctx := &cancelOnSelectContext{done: make(chan struct{})}
		script := Script{Events: []modeladapter.Event{
			{Kind: modeladapter.EventStart},
			{Kind: modeladapter.EventTextDelta, Delta: "content"},
			{Kind: modeladapter.EventTextDelta, Delta: "more content"},
		}}
		result := make(chan modeladapter.Event, len(script.Events))
		provider := New()
		provider.play(ctx, result, script, true)
		if stats := provider.Snapshot(); stats != (Stats{Canceled: 1}) {
			t.Fatalf("canceled ready send misclassified: %+v", stats)
		}
		if len(result) > 1 {
			t.Fatalf("canceled stream sent %d events; at most the in-flight send may finish", len(result))
		}
	}
}

func TestSentTerminalOwnsOutcomeAfterCancellation(t *testing.T) {
	t.Parallel()
	for _, kind := range []modeladapter.EventKind{modeladapter.EventDone, modeladapter.EventError} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			provider := New(Script{Events: []modeladapter.Event{{Kind: kind}}})
			stream := provider.Stream(ctx, nil)
			if event, ok := <-stream; !ok || event.Kind != kind {
				t.Fatalf("terminal event = %+v, open=%v", event, ok)
			}
			cancel()
			for range stream {
			}
			want := Stats{Calls: 1}
			if kind == modeladapter.EventDone {
				want.Completed = 1
			} else {
				want.Failed = 1
			}
			if stats := provider.Snapshot(); stats != want {
				t.Fatalf("sent terminal outcome changed after cancellation: %+v, want %+v", stats, want)
			}
		})
	}
}
