package coordinator

import (
	"fmt"
	"sync"

	"github.com/syurodev/fugue/internal/platform/ids"
)

type slotStatus int

const (
	slotReserved slotStatus = iota + 1
	slotHandedOff
)

// slots is a leaf capacity primitive. The session calls it only from its actor
// goroutine; the mutex stays because slots is also a self-contained type that
// must not corrupt its books when a test or a later caller reaches it from
// another goroutine, and it is never held across a wait.
type slots struct {
	mu        sync.Mutex
	capacity  int
	refs      ids.Generator
	active    map[string]slotStatus
	handoffCh chan *Work
}

func newSlots(capacity int, refs ids.Generator) *slots {
	if capacity <= 0 {
		capacity = 1
	}
	return &slots{
		mu:        sync.Mutex{},
		capacity:  capacity,
		refs:      refs,
		active:    make(map[string]slotStatus, capacity),
		handoffCh: make(chan *Work, capacity),
	}
}

// reserve takes one unit of capacity and returns its reference, which comes from
// the ID generator so that no two reservations ever share one, whatever the
// incarnation. It returns ErrNoCapacity when every slot is taken.
func (s *slots) reserve() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.active) >= s.capacity {
		return "", ErrNoCapacity
	}
	ref, err := s.refs.NewID()
	if err != nil {
		return "", fmt.Errorf("new reservation ref: %w", err)
	}
	s.active[ref] = slotReserved
	return ref, nil
}

func (s *slots) release(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	status, exists := s.active[ref]
	if !exists || status != slotReserved {
		return ErrHandoffFailed
	}
	delete(s.active, ref)
	return nil
}

func (s *slots) handoff(ref string, w *Work) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	status, exists := s.active[ref]
	if !exists || status != slotReserved {
		return ErrHandoffFailed
	}
	s.active[ref] = slotHandedOff
	select {
	case s.handoffCh <- w:
		return nil
	default:
		s.active[ref] = slotReserved
		return ErrHandoffFailed
	}
}

func (s *slots) complete(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	status, exists := s.active[ref]
	if !exists || status != slotHandedOff {
		return ErrHandoffFailed
	}
	delete(s.active, ref)
	return nil
}
