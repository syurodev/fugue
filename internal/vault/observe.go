package vault

import "slices"

// Snapshot is the state-only, owner-versioned view of the lifecycle. The
// incarnation changes whenever a Service is created; the revision increases by
// one for every state change within it. Repeating an operation that leaves the
// state unchanged does not change the revision. A snapshot never carries a key,
// a password or a payload.
type Snapshot struct {
	Incarnation string
	Revision    uint64
	State       State
}

// subscriberBuffer is deliberately small: a subscriber that falls behind is
// told to resync instead of making the vault queue history.
const subscriberBuffer = 1

// Subscription delivers state changes to an asynchronous observer. The vault
// never waits for it: when its buffer is full the subscription is marked
// lagging, drops further events and the observer must call Resync.
type Subscription struct {
	owner   *Service
	id      uint64
	events  chan Snapshot
	lagging bool
	closed  bool
}

// Events is closed by Close. Events queued before Lagging became true are
// stale after a Resync.
func (sub *Subscription) Events() <-chan Snapshot { return sub.events }

// Lagging reports that events were dropped and the observer must Resync.
func (sub *Subscription) Lagging() bool {
	sub.owner.mu.RLock()
	defer sub.owner.mu.RUnlock()
	return sub.lagging
}

// Resync reads the current snapshot, discards queued events and resumes
// delivery from that revision.
func (sub *Subscription) Resync() Snapshot {
	s := sub.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	sub.drainLocked()
	sub.lagging = false
	return s.snapshotLocked()
}

// drainLocked discards the queued events without ever blocking on an empty buffer.
func (sub *Subscription) drainLocked() {
	for {
		select {
		case <-sub.events:
		default:
			return
		}
	}
}

// Close ends delivery and closes Events. It is safe to call more than once.
func (sub *Subscription) Close() {
	s := sub.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub.closed {
		return
	}
	sub.closed = true
	delete(s.subscribers, sub.id)
	close(sub.events)
}

// Observe returns the current snapshot and a subscription to later changes. The
// subscription is registered and the snapshot read in one critical section, so
// no transition falls between the two.
func (s *Service) Observe() (Snapshot, *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	sub := &Subscription{owner: s, id: s.nextID, events: make(chan Snapshot, subscriberBuffer), lagging: false, closed: false}
	s.subscribers[sub.id] = sub
	return s.snapshotLocked(), sub
}

// Snapshot returns the current snapshot.
func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

// RegisterLockGuard installs a synchronous guard and calls it at once with the
// current snapshot. After that it runs for every state change, in registration
// order, outside the vault's key mutex but serialized by the lifecycle mutex.
// When the vault locks, the guard receives the locked snapshot before the data
// key is zeroed, and from that moment no sealing or opening succeeds, so a
// consumer that drops its cached keys in the guard is done when Lock returns.
//
// A guard must be quick, must not block or perform I/O, and must not call the
// Service. The returned function removes the guard; it waits for a callback in
// flight and must not be called from a guard.
func (s *Service) RegisterLockGuard(guard func(Snapshot)) func() {
	if guard == nil {
		return func() {}
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.guards[id] = guard
	snapshot := s.snapshotLocked()
	s.mu.Unlock()
	guard(snapshot)
	return func() {
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.guards, id)
	}
}

func (s *Service) snapshotLocked() Snapshot {
	return Snapshot{Incarnation: s.incarnation, Revision: s.revision, State: s.state}
}

// transitionLocked moves to state and tells the subscribers. It returns the
// guards the caller must run after releasing the mutex. A move to the current
// state changes nothing and returns no guards.
func (s *Service) transitionLocked(state State) ([]func(Snapshot), Snapshot) {
	if s.state == state {
		return nil, s.snapshotLocked()
	}
	s.revision++
	s.state = state
	snapshot := s.snapshotLocked()
	for _, sub := range s.subscribers {
		if sub.lagging {
			continue
		}
		select {
		case sub.events <- snapshot:
		default:
			sub.lagging = true
		}
	}
	ids := make([]uint64, 0, len(s.guards))
	for id := range s.guards {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	guards := make([]func(Snapshot), 0, len(ids))
	for _, id := range ids {
		guards = append(guards, s.guards[id])
	}
	return guards, snapshot
}

func runGuards(guards []func(Snapshot), snapshot Snapshot) {
	for _, guard := range guards {
		guard(snapshot)
	}
}
