package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/syurodev/fugue/internal/vendor"
)

// AvailableVendors returns the sorted list of registered vendor adapter names.
func (m *VendorSessions) AvailableVendors() []string {
	if len(m.deps.Adapters) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.deps.Adapters))
	for name := range m.deps.Adapters {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// registerTurnWaiter registers a one-shot channel notified when sessionID's turn
// completes, fails, or dies.
func (m *VendorSessions) registerTurnWaiter(sessionID string) <-chan error {
	ch := make(chan error, 1)
	m.waiterMu.Lock()
	m.turnWaiters[sessionID] = ch
	m.waiterMu.Unlock()
	return ch
}

func (m *VendorSessions) unregisterTurnWaiter(sessionID string) {
	m.waiterMu.Lock()
	delete(m.turnWaiters, sessionID)
	m.waiterMu.Unlock()
}

func (m *VendorSessions) resolveTurnWaiter(sessionID string, err error) {
	m.waiterMu.Lock()
	ch, ok := m.turnWaiters[sessionID]
	if ok {
		delete(m.turnWaiters, sessionID)
	}
	m.waiterMu.Unlock()
	if ok {
		ch <- err
		close(ch)
	}
}

// AwaitTurnSend registers a turn waiter, sends input, and waits for the turn
// to finish or ctx to be cancelled.
func (m *VendorSessions) AwaitTurnSend(ctx context.Context, sessionID string, input vendor.Input) error {
	ch := m.registerTurnWaiter(sessionID)
	if err := m.Send(ctx, sessionID, input); err != nil {
		m.unregisterTurnWaiter(sessionID)
		return err
	}
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		m.unregisterTurnWaiter(sessionID)
		return fmt.Errorf("await vendor turn: %w", ctx.Err())
	}
}
