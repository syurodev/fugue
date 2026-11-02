package vault

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/supervise"
)

// recv takes an event that is already queued. The vault publishes while it
// changes state, so once the operation has returned the event is there; a test
// never blocks on a vault that failed to publish.
func recv(t *testing.T, sub *Subscription) Snapshot {
	t.Helper()
	select {
	case snapshot, ok := <-sub.Events():
		if !ok {
			t.Fatal("events channel closed")
		}
		return snapshot
	default:
		t.Fatal("no event was queued")
		return Snapshot{}
	}
}

func TestObserveReturnsTheSnapshotAndEveryLaterTransitionInOrder(t *testing.T) {
	t.Parallel()
	s := newService(t)
	initial, sub := s.Observe()
	defer sub.Close()
	want := Snapshot{Incarnation: s.Snapshot().Incarnation, Revision: 0, State: StateLocked}
	if diff := cmp.Diff(want, initial); diff != "" {
		t.Fatalf("initial snapshot (-want +got):\n%s", diff)
	}

	if err := s.Unlock(t.Context(), secret(cheapPassword)); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, sub); got.Revision != 1 || got.State != StateUnlocked {
		t.Errorf("first event = %+v", got)
	}
	s.Lock()
	if got := recv(t, sub); got.Revision != 2 || got.State != StateLocked {
		t.Errorf("second event = %+v", got)
	}
	if err := s.UnlockWithRecovery(t.Context(), secret(cheapRecovery)); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, sub); got.Revision != 3 || got.State != StateUnlocked {
		t.Errorf("third event = %+v", got)
	}
}

func TestAnUnlockedEventNeverPrecedesAUsableKey(t *testing.T) {
	t.Parallel()
	s := newService(t)
	_, sub := s.Observe()
	defer sub.Close()
	task := supervise.Go(t.Context(), "unlock", func(ctx context.Context) error {
		return s.Unlock(ctx, secret(cheapPassword))
	})
	var event Snapshot
	select {
	case event = <-sub.Events():
	case <-task.Done():
		event = recv(t, sub) // the unlock finished first, so its event is queued
	}
	if event.State != StateUnlocked {
		t.Fatalf("event = %+v", event)
	}
	// A coordinator that waited for this event may now do gated work.
	if _, err := s.SealRecord(testBinding(), []byte("body")); err != nil {
		t.Errorf("sealing right after the unlocked event: %v", err)
	}
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestASlowSubscriberIsMarkedLaggingAndResyncs(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	_, sub := s.Observe()
	defer sub.Close()
	if sub.Lagging() {
		t.Fatal("a new subscription must not be lagging")
	}

	s.Lock() // fills the buffer of one
	if sub.Lagging() {
		t.Fatal("lagging after one event")
	}
	if err := s.Unlock(t.Context(), secret(cheapPassword)); err != nil { // overflows it
		t.Fatal(err)
	}
	if !sub.Lagging() {
		t.Fatal("the vault must mark a full subscriber lagging, not wait for it")
	}
	s.Lock() // dropped while lagging

	snapshot := sub.Resync()
	if snapshot.State != StateLocked || snapshot.Revision != 4 {
		t.Errorf("resync snapshot = %+v, want locked at revision 4", snapshot)
	}
	if sub.Lagging() || len(sub.Events()) != 0 {
		t.Error("resync must clear the lag and the stale events")
	}
	if err := s.Unlock(t.Context(), secret(cheapPassword)); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, sub); got.Revision != 5 || got.State != StateUnlocked {
		t.Errorf("event after resync = %+v", got)
	}
}

func TestClosingASubscriptionStopsDeliveryAndIsIdempotent(t *testing.T) {
	t.Parallel()
	s := newService(t)
	_, sub := s.Observe()
	sub.Close()
	sub.Close()
	if _, open := <-sub.Events(); open {
		t.Fatal("events channel still open after Close")
	}
	if err := s.Unlock(t.Context(), secret(cheapPassword)); err != nil {
		t.Fatal(err)
	}
	if len(s.subscribers) != 0 {
		t.Error("the closed subscription is still registered")
	}
}

func TestAGuardGetsTheCurrentSnapshotAtOnceAndEveryChange(t *testing.T) {
	t.Parallel()
	s := newService(t)
	var seen []Snapshot
	remove := s.RegisterLockGuard(func(snapshot Snapshot) { seen = append(seen, snapshot) })
	if len(seen) != 1 || seen[0].State != StateLocked {
		t.Fatalf("guard calls on registration = %+v", seen)
	}
	if err := s.Unlock(t.Context(), secret(cheapPassword)); err != nil {
		t.Fatal(err)
	}
	s.Lock()
	states := []State{seen[0].State, seen[1].State, seen[2].State}
	if diff := cmp.Diff([]State{StateLocked, StateUnlocked, StateLocked}, states); diff != "" {
		t.Errorf("guard states (-want +got):\n%s", diff)
	}
	remove()
	remove() // removing twice is harmless
	if err := s.Unlock(t.Context(), secret(cheapPassword)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Errorf("a removed guard was called again: %d calls", len(seen))
	}
}

func TestAGuardRunsBeforeTheKeyIsZeroedAndWhenNothingCanUseIt(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	var keyIntact, sealRefused bool
	s.RegisterLockGuard(func(snapshot Snapshot) {
		if snapshot.State != StateLocked {
			return
		}
		// Guards run on the locking goroutine, so reading the key here is not a race.
		keyIntact = s.dek != nil && s.dek[0] != 0
		_, err := s.SealRecord(testBinding(), []byte("body"))
		sealRefused = err != nil
	})
	s.Lock()
	if !keyIntact {
		t.Error("the key was already zeroed when the guard ran")
	}
	if !sealRefused {
		t.Error("the vault still sealed records while its guards were running")
	}
}

func TestGuardsRunInRegistrationOrder(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	var order []string
	s.RegisterLockGuard(func(Snapshot) { order = append(order, "first") })
	s.RegisterLockGuard(func(Snapshot) { order = append(order, "second") })
	s.RegisterLockGuard(func(Snapshot) { order = append(order, "third") })
	order = nil
	s.Lock()
	if diff := cmp.Diff([]string{"first", "second", "third"}, order); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
	}
}

func TestANilGuardIsIgnored(t *testing.T) {
	t.Parallel()
	s := newService(t)
	s.RegisterLockGuard(nil)()
	if len(s.guards) != 0 {
		t.Error("a nil guard was registered")
	}
}

// sealUntilRefused seals in a loop and counts the seals that began after
// locked was set. It announces whether its first seal worked on started and
// stops at the first refusal.
func sealUntilRefused(ctx context.Context, s *Service, locked *atomic.Bool, late *atomic.Int64, started chan<- bool) {
	announced := false
	for ctx.Err() == nil {
		wasLocked := locked.Load()
		_, err := s.SealRecord(testBinding(), []byte("body"))
		if !announced {
			announced = true
			started <- err == nil
		}
		if err != nil {
			return
		}
		if wasLocked {
			late.Add(1)
		}
	}
}

func TestNothingSucceedsAfterLockReturnsWhileWorkIsInFlight(t *testing.T) {
	t.Parallel()
	const sealers = 4
	s := unlocked(t)
	var locked atomic.Bool
	var late atomic.Int64
	started := make(chan bool, sealers)
	tasks := make([]*supervise.Task, 0, sealers)
	for i := range sealers {
		tasks = append(tasks, supervise.Go(t.Context(), "sealer-"+strconv.Itoa(i), func(ctx context.Context) error {
			sealUntilRefused(ctx, s, &locked, &late, started)
			return nil
		}))
	}
	for range sealers {
		// Every sealer has sealed once, so Lock lands among them.
		if !<-started {
			t.Fatal("a sealer was refused by an unlocked vault")
		}
	}
	s.Lock()
	locked.Store(true)
	for _, task := range tasks {
		if err := task.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if n := late.Load(); n != 0 {
		t.Errorf("%d seals began after Lock returned and still succeeded", n)
	}
}
