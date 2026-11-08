package transcript

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// Delta carries the full current display state for one item. Clients discard a
// delta whose BaseRevision is lower than the durable revision they hold.
type (
	Delta struct {
		ItemID       string
		BaseRevision uint64
		View         View
	}
	// DeltaSink receives only ephemeral, latest-for-key item views.
	DeltaSink   interface{ Publish(context.Context, *Delta) }
	pendingView struct {
		baseRevision uint64
		kind         ViewKind
		content      string
	}
	// DeltaCoalescer publishes at most 30 item views per second for one session.
	DeltaCoalescer struct {
		mu        sync.Mutex
		pending   map[string]pendingView
		completed map[string]uint64
		order     []string
		closing   bool
		wake      chan struct{}
		task      *supervise.Task
		once      sync.Once
	}
)

// NewDeltaCoalescer starts the supervised publisher for one session.
func NewDeltaCoalescer(ctx context.Context, c clock.Clock, sink DeltaSink, redactor Redactor) *DeltaCoalescer {
	d := &DeltaCoalescer{pending: make(map[string]pendingView), completed: make(map[string]uint64), wake: make(chan struct{}, 1)}
	d.task = supervise.Go(ctx, "transcript-deltas", func(ctx context.Context) error { return d.run(ctx, c, sink, redactor) })
	return d
}

// Update replaces an item's pending full content; completion uses a separate durable path.
func (d *DeltaCoalescer) Update(id string, baseRevision uint64, kind ViewKind, content string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing || baseRevision < d.completed[id] {
		return
	}
	if _, ok := d.pending[id]; !ok {
		d.order = append(d.order, id)
	}
	d.pending[id] = pendingView{baseRevision: baseRevision, kind: kind, content: content}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Complete discards pending state before the caller publishes a durable event.
func (d *DeltaCoalescer) Complete(id string, revision uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if revision > d.completed[id] {
		d.completed[id] = revision
	}
	delete(d.pending, id)
	d.order = slices.DeleteFunc(d.order, func(key string) bool { return key == id })
}

// Close flushes pending views at the same rate and waits for the publisher.
func (d *DeltaCoalescer) Close() error {
	d.once.Do(func() {
		d.mu.Lock()
		d.closing = true
		d.mu.Unlock()
		select {
		case d.wake <- struct{}{}:
		default:
		}
	})
	if err := d.task.Wait(); err != nil {
		return fmt.Errorf("close transcript coalescer: %w", err)
	}
	return nil
}

func (d *DeltaCoalescer) next() (string, pendingView, bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.order) == 0 {
		return "", pendingView{}, false, d.closing
	}
	id := d.order[0]
	d.order = d.order[1:]
	v := d.pending[id]
	delete(d.pending, id)
	return id, v, true, d.closing
}

func (d *DeltaCoalescer) hasPending() (bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.order) > 0, d.closing
}

func waitDelta(ctx context.Context, c clock.Clock, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := c.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("transcript coalescer stopped: %w", ctx.Err())
	case <-timer.C():
		return nil
	}
}

func (d *DeltaCoalescer) run(ctx context.Context, c clock.Clock, sink DeltaSink, redactor Redactor) error {
	var last time.Time
	first := true
	for {
		pending, closing := d.hasPending()
		if !pending {
			if closing {
				return nil
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("transcript coalescer stopped: %w", ctx.Err())
			case <-d.wake:
				continue
			}
		}
		delay := time.Duration(0)
		if !first {
			delay = last.Add(time.Second/30 + 1).Sub(c.Now())
		}
		if err := waitDelta(ctx, c, delay); err != nil {
			return err
		}
		id, v, ok, _ := d.next()
		if !ok {
			continue
		}
		sink.Publish(ctx, &Delta{ItemID: id, BaseRevision: v.baseRevision, View: BuildView(v.kind, v.content, redactor)})
		last = c.Now()
		first = false
	}
}
