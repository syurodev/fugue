package coordinator

import (
	"fmt"
	"sync"
)

type slotStatus int

const (
	slotReserved slotStatus = iota + 1
	slotHandedOff
)

type slots struct {
	mu        sync.Mutex
	capacity  int
	counter   uint64
	active    map[string]slotStatus
	handoffCh chan *Work
}

func newSlots(capacity int) *slots {
	if capacity <= 0 {
		capacity = 1
	}
	return &slots{
		mu:        sync.Mutex{},
		capacity:  capacity,
		counter:   0,
		active:    make(map[string]slotStatus, capacity),
		handoffCh: make(chan *Work, capacity),
	}
}

func (s *slots) reserve() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.active) >= s.capacity {
		return "", false
	}
	s.counter++
	ref := fmt.Sprintf("slot-%d", s.counter)
	s.active[ref] = slotReserved
	return ref, true
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
