package faux

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/syurodev/fugue/internal/modeladapter"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// Script emits Events in order. When Cut is true, CutAfter closes the stream
// before the event at that zero-based index. A cut after start models a lost
// stream with no terminal evidence.
type Script struct {
	Events   []modeladapter.Event
	CutAfter int
	Cut      bool
}

// Stats is a snapshot of provider call evidence.
type Stats struct {
	Calls       int64
	Completed   int64
	Failed      int64
	Interrupted int64
	Canceled    int64
	Active      int64
}

// Provider plays scripts once each in call order. Calls beyond the script list
// close without a terminal event, making accidental extra model calls visible.
type Provider struct {
	mu      sync.Mutex
	scripts []Script
	next    int
	calls   atomic.Int64
	done    atomic.Int64
	failed  atomic.Int64
	cut     atomic.Int64
	cancel  atomic.Int64
	active  atomic.Int64
}

// New returns a provider that plays the supplied scripts once each.
func New(scripts ...Script) *Provider {
	copyOfScripts := make([]Script, len(scripts))
	for i, script := range scripts {
		copyOfScripts[i] = Script{Events: cloneEvents(script.Events), CutAfter: script.CutAfter, Cut: script.Cut}
	}
	return &Provider{scripts: copyOfScripts}
}

// Stream starts one scripted model call and closes the result channel at its
// terminal outcome, cut point, or cancellation.
func (p *Provider) Stream(ctx context.Context, _ *modeladapter.Request) <-chan modeladapter.Event {
	result := make(chan modeladapter.Event)
	p.calls.Add(1)
	p.active.Add(1)
	script, ok := p.takeScript()
	supervise.Go(ctx, "faux-model-stream", func(taskCtx context.Context) error {
		defer close(result)
		defer p.active.Add(-1)
		p.play(taskCtx, result, script, ok)
		return nil
	})
	return result
}

func (p *Provider) play(ctx context.Context, result chan<- modeladapter.Event, script Script, ok bool) {
	select {
	case <-ctx.Done():
		p.cancel.Add(1)
		return
	default:
	}
	if !ok {
		p.cut.Add(1)
		return
	}
	for i := range script.Events {
		if script.Cut && i == script.CutAfter {
			p.cut.Add(1)
			return
		}
		event := script.Events[i]
		event.Failure = cloneFailure(event.Failure)
		select {
		case result <- event:
		case <-ctx.Done():
			p.cancel.Add(1)
			return
		}
		if p.finish(event.Kind) {
			return
		}
	}
	p.cut.Add(1)
}

func cloneEvents(events []modeladapter.Event) []modeladapter.Event {
	cloned := slices.Clone(events)
	for i := range cloned {
		cloned[i].Failure = cloneFailure(cloned[i].Failure)
	}
	return cloned
}

func cloneFailure(failure *modeladapter.Failure) *modeladapter.Failure {
	if failure == nil {
		return nil
	}
	cloned := *failure
	return &cloned
}

func (p *Provider) finish(kind modeladapter.EventKind) bool {
	if kind == modeladapter.EventDone {
		p.done.Add(1)
		return true
	}
	if kind == modeladapter.EventError {
		p.failed.Add(1)
		return true
	}
	return false
}

// Snapshot returns cumulative calls and outcomes, including currently active streams.
func (p *Provider) Snapshot() Stats {
	return Stats{
		Calls: p.calls.Load(), Completed: p.done.Load(), Failed: p.failed.Load(),
		Interrupted: p.cut.Load(), Canceled: p.cancel.Load(), Active: p.active.Load(),
	}
}

func (p *Provider) takeScript() (Script, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.next >= len(p.scripts) {
		return Script{}, false
	}
	script := p.scripts[p.next]
	p.next++
	return script, true
}
