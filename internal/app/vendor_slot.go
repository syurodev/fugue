package app

import (
	"context"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

// ReserveSlot atomically reserves one scheduler slot across the machine-wide
// and per-account tiers before a task assignment is persisted.
func (m *VendorSessions) ReserveSlot(ctx context.Context, sessionID, vendorName, accountID string) (func(), error) {
	return m.reserveSlot(ctx, sessionID, vendorName, accountID)
}

func (m *VendorSessions) reserveSlot(ctx context.Context, sessionID, vendorName, accountID string) (func(), error) {
	m.slotMu.Lock()
	defer m.slotMu.Unlock()
	key := normalizeSlotKey(vendorName, accountID)
	if err := m.checkSlotEligibility(sessionID); err != nil {
		return nil, err
	}
	tiers, err := m.schedulerTiers(ctx)
	if err != nil {
		return nil, err
	}
	total, byAccount, err := m.slotUsage(ctx)
	if err != nil {
		return nil, err
	}
	if total >= tiers.MachineWide || byAccount[key] >= tiers.PerAccount {
		return nil, ErrVendorSessionCapacity
	}
	m.mu.Lock()
	if m.closed || m.actors[sessionID] != nil || m.hasReservationLocked(sessionID) {
		m.mu.Unlock()
		return nil, errcode.New(errcode.FailedPrecondition, "vendor session is already active or manager is closed")
	}
	m.reservations[sessionID] = key
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			delete(m.reservations, sessionID)
			m.mu.Unlock()
		})
	}, nil
}

func (m *VendorSessions) hasReservation(sessionID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.hasReservationLocked(sessionID)
}

func (m *VendorSessions) checkSlotEligibility(sessionID string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed || m.actors[sessionID] != nil || m.hasReservationLocked(sessionID) {
		return errcode.New(errcode.FailedPrecondition, "vendor session is already active or manager is closed")
	}
	return nil
}

func (m *VendorSessions) hasReservationLocked(sessionID string) bool {
	if m.reservations == nil {
		return false
	}
	_, ok := m.reservations[sessionID]
	return ok
}

func normalizeSlotKey(vendorName, accountID string) slotAccountKey {
	if accountID == "" {
		accountID = vendorDefaultAccount
	}
	return slotAccountKey{vendor: vendorName, account: accountID}
}

func (m *VendorSessions) schedulerTiers(ctx context.Context) (SchedulerTiers, error) {
	var cfg workspace.HostConfig
	if m.deps.HostConfig != nil {
		var err error
		cfg, err = m.deps.HostConfig(ctx)
		if err != nil {
			return SchedulerTiers{}, err
		}
	}
	return ComputeSchedulerTiers(m.deps.HostResources, &cfg)
}

func (m *VendorSessions) slotUsage(ctx context.Context) (int, map[slotAccountKey]int, error) {
	sessions, err := m.deps.Store.ListSessions(ctx)
	if err != nil {
		return 0, nil, safeActorError(err)
	}
	counted := make(map[string]struct{}, len(sessions))
	byAccount := make(map[slotAccountKey]int)
	total := 0
	for i := range sessions {
		s := &sessions[i]
		if s.State == vendorstore.StateClosed || s.State == vendorstore.StateDetached {
			continue
		}
		counted[s.ID] = struct{}{}
		total++
		byAccount[normalizeSlotKey(s.Vendor, s.AccountID)]++
	}
	m.mu.RLock()
	for id, key := range m.reservations {
		if _, seen := counted[id]; !seen {
			total++
			byAccount[key]++
		}
	}
	m.mu.RUnlock()
	return total, byAccount, nil
}
