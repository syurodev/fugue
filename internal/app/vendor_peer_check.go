package app

import (
	"context"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
)

const defaultInflightSpawnWaitCap = 5 * time.Second

type peerTreeEmptyChecker interface {
	TreeEmpty() (bool, error)
}

type missingPeerOwner struct{}

func (missingPeerOwner) OwnsPID(int) (bool, error) {
	return false, errcode.New(errcode.Internal, "a vendor session cannot report its process tree")
}

// OwnsPeer reports whether pid belongs to the process tree of any live,
// starting, or retained ended/failed session whose tree is not yet verified
// empty (roadmap Q-41 A). When vendor spawns are in flight between adapter
// Start and OnSpawned registration, it waits (bounded by ctx and a short cap)
// for registrations to settle before evaluating ownership, and fails closed on
// timeout or cancellation.
func (m *VendorSessions) OwnsPeer(ctx context.Context, pid int) (bool, error) {
	if ctx == nil {
		return false, errcode.New(errcode.InvalidArgument, "peer check context is required")
	}
	pending, actors, retained, err := m.snapshotOwnersAfterInflight(ctx)
	if err != nil {
		return false, err
	}
	if m.deps.TestAfterPeerSnapshot != nil {
		m.deps.TestAfterPeerSnapshot()
	}
	for _, owner := range pending {
		if owned, err := checkPeerOwner(owner, pid); err != nil || owned {
			return owned, err
		}
	}
	for _, actor := range actors {
		if owned, err := checkLiveActorOwner(actor, pid); err != nil || owned {
			return owned, err
		}
	}
	return m.checkRetainedOwners(retained, pid)
}

func checkLiveActorOwner(actor *vendorSessionActor, pid int) (bool, error) {
	tree, ok := actor.session.(vendor.PeerOwner)
	if !ok {
		return false, errcode.New(errcode.Internal, "a vendor session cannot report its process tree")
	}
	return checkPeerOwner(tree, pid)
}

func (m *VendorSessions) checkRetainedOwners(retained []*retainedOwner, pid int) (bool, error) {
	var verifiedEmpty []*retainedOwner
	defer func() {
		m.pruneRetainedOwners(verifiedEmpty)
	}()
	for _, r := range retained {
		if owned, err := checkPeerOwner(r.owner, pid); err != nil || owned {
			return owned, err
		}
		if checker, ok := r.owner.(peerTreeEmptyChecker); ok {
			empty, err := checker.TreeEmpty()
			if err != nil {
				return false, errcode.Wrap(errcode.Internal, "check whether a retained vendor session tree is empty", err)
			}
			if empty {
				verifiedEmpty = append(verifiedEmpty, r)
			}
		}
	}
	return false, nil
}

func (m *VendorSessions) pruneRetainedOwners(empty []*retainedOwner) {
	if len(empty) == 0 {
		return
	}
	m.mu.Lock()
	for _, r := range empty {
		delete(m.retainedOwners, r)
	}
	m.mu.Unlock()
}

func (m *VendorSessions) beginInflightSpawn() func() {
	m.mu.Lock()
	if m.inflightSpawns == 0 {
		m.spawnSettled = make(chan struct{})
	}
	m.inflightSpawns++
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(m.endInflightSpawn)
	}
}

func (m *VendorSessions) endInflightSpawn() {
	m.mu.Lock()
	m.inflightSpawns--
	if m.inflightSpawns == 0 && m.spawnSettled != nil {
		close(m.spawnSettled)
		m.spawnSettled = nil
	}
	m.mu.Unlock()
}

func (m *VendorSessions) snapshotOwnersAfterInflight(ctx context.Context) ([]vendor.PeerOwner, []*vendorSessionActor, []*retainedOwner, error) {
	var timer clock.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		waitCh, pending, actors, retained := m.trySnapshotOwners()
		if waitCh == nil {
			return pending, actors, retained, nil
		}
		if timer == nil {
			timer = m.deps.Clock.NewTimer(defaultInflightSpawnWaitCap)
		}
		if m.deps.TestOnWaitInflightSpawn != nil {
			m.deps.TestOnWaitInflightSpawn()
		}
		if err := waitForSpawnSettle(ctx, waitCh, timer.C()); err != nil {
			return nil, nil, nil, err
		}
	}
}

func (m *VendorSessions) trySnapshotOwners() (<-chan struct{}, []vendor.PeerOwner, []*vendorSessionActor, []*retainedOwner) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.spawnSettled != nil {
		return m.spawnSettled, nil, nil, nil
	}
	actors := make([]*vendorSessionActor, 0, len(m.actors))
	for _, actor := range m.actors {
		actors = append(actors, actor)
	}
	pending := make([]vendor.PeerOwner, 0, len(m.pendingOwners))
	for p := range m.pendingOwners {
		pending = append(pending, p.owner)
	}
	retained := make([]*retainedOwner, 0, len(m.retainedOwners))
	for r := range m.retainedOwners {
		retained = append(retained, r)
	}
	return nil, pending, actors, retained
}

func waitForSpawnSettle(ctx context.Context, settled <-chan struct{}, deadline <-chan time.Time) error {
	select {
	case <-settled:
		return nil
	case <-deadline:
		return errcode.New(errcode.DeadlineExceeded, "timed out waiting for in-flight vendor spawn registration")
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "wait for in-flight vendor spawn registration", ctx.Err())
	}
}

func checkPeerOwner(owner vendor.PeerOwner, pid int) (bool, error) {
	owned, err := owner.OwnsPID(pid)
	if err != nil {
		return false, errcode.Wrap(errcode.Internal, "read a vendor session's process tree", err)
	}
	return owned, nil
}
