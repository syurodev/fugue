package app

import (
	"context"
	"sync"
)

// offerKind is what a subscriber's queue did with one offered event.
type offerKind int

const (
	offerDelivered offerKind = iota
	offerCoalesced
	offerDropped
	offerOverflow
	offerGone
)

// Subscription is one client's view of one topic: an optional pre-taken
// snapshot, pending catch-up events, then the live queue. The stream ends when
// the client's context is done, the host ends the streams for a drain, or the
// subscriber fell too far behind (resync_required). Next is the only consume
// path; the handler owns exactly one consume loop.
type Subscription struct {
	topic string
	hub   *EventHub

	mu      sync.Mutex
	pending []Event
	queue   []Event
	head    int
	notify  chan struct{}

	lastSent EventSeq
	snap     []byte
	snapSeq  EventSeq
	haveSnap bool

	overflow bool
	ended    bool
	closed   bool
	resync   bool
}

// deliver installs the subscribe-time state: catch-up events to yield before
// the live queue and the snapshot the handler sends first.
func (s *Subscription) deliver(pending []Event, snap []byte, snapSeq EventSeq, haveSnapshot bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = pending
	s.snap, s.snapSeq, s.haveSnap = snap, snapSeq, haveSnapshot
	s.lastSent = snapSeq
}

// Snapshot returns the pre-taken snapshot when this subscription got one; a
// catch-up subscription (valid cursor) reports ok=false and streams events
// from the cursor's sequence without a snapshot.
func (s *Subscription) Snapshot() (state []byte, seq EventSeq, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap, s.snapSeq, s.haveSnap
}

// Next returns the next event in strict sequence order, blocking until one is
// published, the stream ends or ctx is done. A ended stream reports
// ResyncRequired for the end reason; the caller closes the subscription.
func (s *Subscription) Next(ctx context.Context) (Event, bool) {
	for {
		s.mu.Lock()
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			if ev.Seq > s.lastSent {
				s.lastSent = ev.Seq
				s.mu.Unlock()
				return ev, true
			}
			s.mu.Unlock()
			continue
		}
		if s.head < len(s.queue) {
			ev := s.queue[s.head]
			s.queue[s.head] = Event{}
			s.head++
			if s.head == len(s.queue) {
				s.queue = s.queue[:0]
				s.head = 0
			}
			if ev.Seq > s.lastSent {
				s.lastSent = ev.Seq
				s.mu.Unlock()
				return ev, true
			}
			s.mu.Unlock()
			continue
		}
		if s.closed || s.ended {
			s.mu.Unlock()
			return Event{}, false
		}
		if s.overflow {
			s.resync = true
			s.mu.Unlock()
			return Event{}, false
		}
		s.mu.Unlock()
		select {
		case <-s.notify:
		case <-ctx.Done():
			s.Close()
			return Event{}, false
		}
	}
}

// ResyncRequired reports why the stream ended: true means the client must
// discard its state and resubscribe from a fresh snapshot.
func (s *Subscription) ResyncRequired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resync
}

// Close unsubscribes the subscription; idempotent, and called by Next when the
// context ends.
func (s *Subscription) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.hub.mu.Lock()
	if t, ok := s.hub.topics[s.topic]; ok {
		for i, sub := range t.subs {
			if sub == s {
				t.subs = append(t.subs[:i], t.subs[i+1:]...)
				break
			}
		}
	}
	s.hub.mu.Unlock()
	s.wake()
}

// markEnded ends the stream cleanly (a drain): queued events already delivered
// are followed by the end, nothing more is accepted.
func (s *Subscription) markEnded() {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
	s.wake()
}

// offer applies the publisher's backpressure policy to one event. It runs on
// the publisher's goroutine and never blocks: a full queue coalesces or drops
// a droppable delta and otherwise marks the subscriber overflown, ending its
// stream with resync_required instead of dropping a control event silently.
func (s *Subscription) offer(ev Event) offerKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed || s.ended:
		return offerGone
	case s.overflow:
		// The stream is already ending with resync_required; nothing further
		// counts as a drop this subscriber will see.
		return offerGone
	}
	if n := len(s.queue); n > s.head && ev.Droppable && s.queue[n-1].Droppable && s.queue[n-1].Key == ev.Key {
		s.queue[n-1] = ev
		return offerCoalesced
	}
	if len(s.queue)-s.head >= subscriberQueueCap {
		if ev.Droppable {
			return offerDropped
		}
		s.overflow = true
		s.wakeLocked()
		return offerOverflow
	}
	s.queue = append(s.queue, ev)
	s.wakeLocked()
	return offerDelivered
}

func (s *Subscription) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *Subscription) wakeLocked() { s.wake() }
