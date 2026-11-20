package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	defaultPromptPacingMinSeconds = 10
	defaultPromptPacingMaxSeconds = 15
	maxPacedQueuePerVendor        = 64
)

// ErrPacingQueueFull reports that a vendor's bounded fugue-prompt queue is full.
var ErrPacingQueueFull = errcode.New(errcode.FailedPrecondition, "vendor prompt pacing queue is full")

type pacedSendItem struct {
	sessionID string
	actor     *vendorSessionActor
	input     vendor.Input
	cancelCh  <-chan struct{}
	onDone    func(error)
}

type vendorPacerQueue struct {
	vendorName       string
	items            []pacedSendItem
	pendingBySession map[string]int
	nextAllowed      time.Time
	workerRunning    bool
}

type vendorPacer struct {
	clk        clock.Clock
	spawn      func(string, func(context.Context) error) *supervise.Task
	readConfig HostConfigReader
	jitter     func(time.Duration) time.Duration

	mu      sync.Mutex
	closed  bool
	drainCh chan struct{}
	queues  map[string]*vendorPacerQueue
}

func newVendorPacer(
	clk clock.Clock,
	spawn func(string, func(context.Context) error) *supervise.Task,
	readConfig HostConfigReader,
	jitter func(time.Duration) time.Duration,
) *vendorPacer {
	if jitter == nil {
		jitter = cryptoJitter
	}
	return &vendorPacer{
		clk:        clk,
		spawn:      spawn,
		readConfig: readConfig,
		jitter:     jitter,
		drainCh:    make(chan struct{}),
		queues:     make(map[string]*vendorPacerQueue),
	}
}

func cryptoJitter(span time.Duration) time.Duration {
	if span <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(span)+1))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}

// Enqueue adds a fugue-origin prompt to the per-vendor bounded FIFO queue.
func (p *vendorPacer) Enqueue(actor *vendorSessionActor, input vendor.Input, cancelCh <-chan struct{}, onDone func(error)) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrHostStopping
	}
	vq := p.queues[actor.info.Vendor]
	if vq == nil {
		vq = &vendorPacerQueue{
			vendorName:       actor.info.Vendor,
			pendingBySession: make(map[string]int),
		}
		p.queues[actor.info.Vendor] = vq
	}
	if len(vq.items) >= maxPacedQueuePerVendor {
		p.mu.Unlock()
		return ErrPacingQueueFull
	}
	vq.items = append(vq.items, pacedSendItem{
		sessionID: actor.info.ID,
		actor:     actor,
		input:     input,
		cancelCh:  cancelCh,
		onDone:    onDone,
	})
	vq.pendingBySession[actor.info.ID]++
	actor.touch()
	startWorker := !vq.workerRunning
	if startWorker {
		vq.workerRunning = true
	}
	p.mu.Unlock()
	if startWorker {
		p.spawn("vendor-pacer-"+vq.vendorName, func(ctx context.Context) error {
			p.runVendor(ctx, vq)
			return nil
		})
	}
	return nil
}

// HasPendingForSession reports whether sessionID has any queued or waiting paced prompt.
func (p *vendorPacer) HasPendingForSession(vendorName, sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	vq := p.queues[vendorName]
	if vq == nil {
		return false
	}
	return vq.pendingBySession[sessionID] > 0
}

// Close drains every pending paced prompt on host shutdown/drain.
func (p *vendorPacer) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.drainCh)
	var cancelled []pacedSendItem
	for _, vq := range p.queues {
		for i := range vq.items {
			it := vq.items[i]
			p.decSessionLocked(vq, it.sessionID)
			cancelled = append(cancelled, it)
		}
		vq.items = nil
	}
	p.mu.Unlock()
	for i := range cancelled {
		if cancelled[i].onDone != nil {
			cancelled[i].onDone(ErrHostStopping)
		}
	}
}

func (p *vendorPacer) runVendor(ctx context.Context, vq *vendorPacerQueue) {
	for {
		item, waitUntil, ok := p.nextItem(ctx, vq)
		if !ok {
			return
		}
		sendErr := p.deliverItem(ctx, vq, &item, waitUntil)
		if item.onDone != nil {
			item.onDone(sendErr)
		}
	}
}

func (p *vendorPacer) nextItem(ctx context.Context, vq *vendorPacerQueue) (pacedSendItem, time.Time, bool) {
	p.mu.Lock()
	if p.closed || ctx.Err() != nil {
		drained := p.drainQueueLocked(vq)
		vq.workerRunning = false
		p.mu.Unlock()
		for i := range drained {
			if drained[i].onDone != nil {
				drained[i].onDone(ErrHostStopping)
			}
		}
		return pacedSendItem{}, time.Time{}, false
	}
	if len(vq.items) == 0 {
		vq.workerRunning = false
		p.mu.Unlock()
		return pacedSendItem{}, time.Time{}, false
	}
	item := vq.items[0]
	vq.items = vq.items[1:]
	waitUntil := vq.nextAllowed
	p.mu.Unlock()
	return item, waitUntil, true
}

func (p *vendorPacer) deliverItem(ctx context.Context, vq *vendorPacerQueue, item *pacedSendItem, waitUntil time.Time) error {
	defer p.finishSessionItem(vq, item.sessionID)
	if err := p.waitItemTurn(ctx, item, waitUntil); err != nil {
		return err
	}
	if p.isClosed() || ctx.Err() != nil {
		return ErrHostStopping
	}
	item.actor.touch()
	err := item.actor.call(ctx, actorCommand{kind: commandSend, input: item.input})
	if err == nil {
		interval := p.pacingInterval(ctx)
		if interval > 0 {
			p.mu.Lock()
			vq.nextAllowed = p.clk.Now().Add(interval)
			p.mu.Unlock()
		}
	}
	return err
}

func (p *vendorPacer) waitItemTurn(ctx context.Context, item *pacedSendItem, waitUntil time.Time) error {
	if err := p.itemPrecheck(ctx, item); err != nil {
		return err
	}
	now := p.clk.Now()
	if waitUntil.IsZero() || !now.Before(waitUntil) {
		return nil
	}
	timer := p.clk.NewTimer(waitUntil.Sub(now))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ErrHostStopping
	case <-p.drainCh:
		return ErrHostStopping
	case <-item.actor.done:
		return vendor.ErrSessionClosed
	case <-item.cancelCh:
		return context.Canceled
	case <-timer.C():
		return p.itemPrecheck(ctx, item)
	}
}

func (p *vendorPacer) itemPrecheck(ctx context.Context, item *pacedSendItem) error {
	if p.isClosed() || ctx.Err() != nil {
		return ErrHostStopping
	}
	select {
	case <-item.actor.done:
		return vendor.ErrSessionClosed
	case <-item.cancelCh:
		return context.Canceled
	default:
		return nil
	}
}

func (p *vendorPacer) finishSessionItem(vq *vendorPacerQueue, sessionID string) {
	p.mu.Lock()
	p.decSessionLocked(vq, sessionID)
	p.mu.Unlock()
}

func (p *vendorPacer) decSessionLocked(vq *vendorPacerQueue, sessionID string) {
	if n := vq.pendingBySession[sessionID]; n <= 1 {
		delete(vq.pendingBySession, sessionID)
	} else {
		vq.pendingBySession[sessionID] = n - 1
	}
}

func (p *vendorPacer) drainQueueLocked(vq *vendorPacerQueue) []pacedSendItem {
	if len(vq.items) == 0 {
		return nil
	}
	out := make([]pacedSendItem, len(vq.items))
	copy(out, vq.items)
	for i := range vq.items {
		p.decSessionLocked(vq, vq.items[i].sessionID)
	}
	vq.items = nil
	return out
}

func (p *vendorPacer) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *vendorPacer) pacingInterval(ctx context.Context) time.Duration {
	minS, maxS := defaultPromptPacingMinSeconds, defaultPromptPacingMaxSeconds
	if p.readConfig != nil {
		if cfg, err := p.readConfig(ctx); err == nil {
			minS = cfg.PromptPacingMinSeconds
			maxS = cfg.PromptPacingMaxSeconds
		}
	}
	if maxS < minS {
		maxS = minS
	}
	if maxS <= 0 {
		return 0
	}
	minD := time.Duration(minS) * time.Second
	span := time.Duration(maxS-minS) * time.Second
	offset := p.jitter(span)
	if offset < 0 {
		offset = 0
	} else if offset > span {
		offset = span
	}
	return minD + offset
}

// SendAsync enqueues a Fugue-origin prompt for per-vendor paced delivery and
// invokes onDone once the prompt finishes sending, fails, or is cancelled.
func (m *VendorSessions) SendAsync(sessionID string, input vendor.Input, onDone func(error)) error {
	if err := vendor.ValidateInput(input); err != nil {
		return fmt.Errorf("validate vendor input: %w", err)
	}
	actor := m.actor(sessionID)
	if actor == nil {
		return vendorstore.ErrSessionNotFound
	}
	if input.Origin != vendor.OriginFugue {
		m.spawn("vendor-send-"+sessionID, func(ctx context.Context) error {
			err := actor.call(ctx, actorCommand{kind: commandSend, input: input})
			if onDone != nil {
				onDone(err)
			}
			return nil
		})
		return nil
	}
	return m.pacer.Enqueue(actor, input, nil, onDone)
}

func (m *VendorSessions) sendPacedSync(ctx context.Context, actor *vendorSessionActor, input vendor.Input) error {
	done := make(chan error, 1)
	if err := m.pacer.Enqueue(actor, input, ctx.Done(), func(err error) {
		done <- err
	}); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("wait for paced vendor send: %w", ctx.Err())
	}
}

// CancelPendingSends cancels every prompt waiting in the per-vendor pacing queues.
func (m *VendorSessions) CancelPendingSends() {
	if m.pacer != nil {
		m.pacer.Close()
	}
}
