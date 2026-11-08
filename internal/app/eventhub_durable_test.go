package app

// Durable-topic tests: a scripted committed log (fakeDurable) proves the
// catch-up, restart, resync and publish-order contracts of DurableTopic.

import (
	"context"
	"slices"
	"testing"
)

// startSubscribe runs hub.Subscribe on a goroutine for a source whose

// commitAsync commits n durable events on a goroutine; a failure fails the

// subscribeDurable subscribes to the durable test topic "d" with a cursor.

// fakeDurable is a scripted DurableTopic: an in-memory committed log whose

func newDurableHub(t *testing.T, fd *fakeDurable, incarnation string) *EventHub {
	t.Helper()
	hub := NewEventHub(incarnation)
	if err := hub.RegisterDurable("d", fd); err != nil {
		t.Fatal(err)
	}
	return hub
}

func TestDurableCursorReconnectsAtTheRightSequence(t *testing.T) {
	t.Parallel()
	fd := &fakeDurable{}
	hub := newDurableHub(t, fd, testIncarnation)
	first := fd.mustCommit(t, hub, "e1")
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "d"})
	if err != nil {
		t.Fatal(err)
	}
	_, snapSeq, ok := sub.Snapshot()
	if !ok || snapSeq != first.Seq {
		t.Fatalf("snapshot = (%v, %d), want true at %d", ok, snapSeq, first.Seq)
	}
	sub.Close()
	// The client reconnects with its cursor and receives exactly the events it
	// has not seen, no snapshot.
	for range 3 {
		fd.mustCommit(t, hub, "e2")
	}
	resub, err := hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI), Topic: "d",
		Cursor: &EventCursor{Topic: "d", Incarnation: testIncarnation, Sequence: first.Seq},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resub.Close)
	if _, _, ok := resub.Snapshot(); ok {
		t.Fatal("a valid durable cursor got a snapshot")
	}
	got := collectN(t, t.Context(), resub, 3)
	if got[0].Seq != first.Seq+1 || got[2].Seq != first.Seq+3 {
		t.Fatalf("reconnect delivered %d events from seq %d, want 3 from %d", len(got), got[0].Seq, first.Seq+1)
	}
}

func TestDurableTooOldCursorGetsSnapshot(t *testing.T) {
	t.Parallel()
	fd := &fakeDurable{}
	hub := newDurableHub(t, fd, testIncarnation)
	for range 10 {
		fd.mustCommit(t, hub, "e")
	}
	fd.mu.Lock()
	fd.purged = 8 // events before 8 are gone
	fd.mu.Unlock()
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI), Topic: "d",
		Cursor: &EventCursor{Topic: "d", Incarnation: testIncarnation, Sequence: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	_, seq, ok := sub.Snapshot()
	if !ok || seq != 10 {
		t.Fatalf("a purged cursor got (snapshot=%v, seq=%d), want a snapshot at 10", ok, seq)
	}
}

func TestDurableCursorSurvivesRestart(t *testing.T) {
	t.Parallel()
	fd := &fakeDurable{}
	hub1 := newDurableHub(t, fd, testIncarnation)
	first := fd.mustCommit(t, hub1, "e")
	sub, err := hub1.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "d"})
	if err != nil {
		t.Fatal(err)
	}
	sub.Close()
	// A new host process: a new incarnation and a new hub over the same store.
	hub2 := newDurableHub(t, fd, "inc-2")
	for range 2 {
		fd.mustCommit(t, hub2, "e")
	}
	// The durable cursor names the old incarnation on purpose: the sequence
	// lives in the store, so catch-up still works (host-api §6).
	resub, err := hub2.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI), Topic: "d",
		Cursor: &EventCursor{Topic: "d", Incarnation: testIncarnation, Sequence: first.Seq},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resub.Close)
	if _, _, ok := resub.Snapshot(); ok {
		t.Fatal("a durable cursor across a restart got a snapshot")
	}
	got := collectN(t, t.Context(), resub, 2)
	if got[0].Seq != first.Seq+1 {
		t.Fatalf("restart catch-up delivered %d events, want 2 after %d", len(got), first.Seq)
	}
}

func TestDurableSnapshotStreamConsistencyUnderConcurrentPublish(t *testing.T) {
	t.Parallel()
	fd := &fakeDurable{snapGate: make(chan struct{}), entered: make(chan struct{})}
	hub := newDurableHub(t, fd, testIncarnation)
	// Subscribe registers the queue and then blocks in the store's Snapshot;
	// publishes meanwhile queue up and are delivered after the snapshot.
	subs, errs := startSubscribe(t.Context(), hub, SubscribeParams{Principal: string(KindCLI), Topic: "d"})
	<-fd.entered
	<-commitAsync(t, fd, hub, "d", 100)
	close(fd.snapGate)
	var sub *Subscription
	select {
	case sub = <-subs:
	case err := <-errs:
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	_, snapSeq, ok := sub.Snapshot()
	if !ok || snapSeq != 0 {
		t.Fatalf("the blocked snapshot = (%v, %d), want true at 0", ok, snapSeq)
	}
	got := collectN(t, t.Context(), sub, 100)
	if got[0].Seq != 1 || got[99].Seq != 100 {
		t.Fatalf("the queued events after the snapshot run from %d to %d, want 1 to 100", got[0].Seq, got[99].Seq)
	}
	seqs := make([]EventSeq, 0, len(got))
	for _, ev := range got {
		seqs = append(seqs, ev.Seq)
	}
	slices.Sort(seqs)
	for i, seq := range seqs {
		if seq != uint64(i)+1 {
			t.Fatalf("sequence at %d = %d, want %d: the snapshot and the stream have a gap or a duplicate", i, seq, i+1)
		}
	}
}

func TestDurableSlowClientGetsResyncAndThePublisherNeverBlocks(t *testing.T) {
	t.Parallel()
	fd := &fakeDurable{}
	hub := newDurableHub(t, fd, testIncarnation)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "d"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	// More control events than the queue holds: the publisher returns on its
	// own, the buffered events are still delivered, and then the slow durable
	// subscriber resynchronizes instead of losing events silently.
	const n = subscriberQueueCap + 50
	for range n {
		fd.mustCommit(t, hub, "e")
	}
	got := 0
	for {
		ev, ok := sub.Next(t.Context())
		if !ok {
			break
		}
		got++
		if ev.Seq != uint64(got) {
			t.Fatalf("event %d has seq %d, want %d", got, ev.Seq, got)
		}
	}
	if got != subscriberQueueCap {
		t.Fatalf("the stream delivered %d buffered events, want %d", got, subscriberQueueCap)
	}
	if !sub.ResyncRequired() {
		t.Fatal("a durable overflow must end with resync_required")
	}
	if got := hub.Dropped(); got != 0 {
		t.Fatalf("durable events were dropped: %d", got)
	}
}

func TestDurableOutOfOrderPublishResyncsSubscribers(t *testing.T) {
	t.Parallel()
	fd := &fakeDurable{}
	hub := newDurableHub(t, fd, testIncarnation)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "d"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	fd.mustCommit(t, hub, "e1")
	// The owner publishes seq 2, then publishes seq 1 again: its publish
	// order has regressed below the topic's last published sequence.
	fd.mu.Lock()
	fd.seq++
	fd.events = append(fd.events, Event{Seq: fd.seq, Type: "e2", Payload: []byte("e2")})
	fd.state = []byte("e2")
	fd.mu.Unlock()
	if _, err := hub.Publish("d", Event{Seq: 2, Type: "e2", Payload: []byte("e2")}); err != nil {
		t.Fatal(err)
	}
	skipped, perr := hub.Publish("d", Event{Seq: 1, Type: "e1", Payload: []byte("e1")})
	if perr != nil {
		t.Fatalf("an out-of-order publish failed: %v", perr)
	}
	if skipped.Seq != 0 {
		t.Fatalf("the skipped event was fanned out: %+v", skipped)
	}
	// The live subscriber drains what it has, then ends with resync_required
	// so it catches up through Since instead of missing the gap forever.
	for {
		if _, ok := sub.Next(t.Context()); !ok {
			break
		}
	}
	if !sub.ResyncRequired() {
		t.Fatal("an out-of-order durable publish did not end the stream with resync_required")
	}
	// Since reads the committed log in commit order.
	resub := subscribeDurable(t, hub, "d", testIncarnation, 1)
	got := collectN(t, t.Context(), resub, 1)
	if got[0].Seq != 2 {
		t.Fatalf("catch-up delivered seq %d, want 2", got[0].Seq)
	}
}

func TestSlowSnapshotDoesNotBlockPublishAndKeepsNoGap(t *testing.T) {
	t.Parallel()
	hub := NewEventHub(testIncarnation)
	entered := make(chan struct{})
	gate := make(chan struct{})
	err := hub.RegisterEphemeral("t", func(context.Context) ([]byte, error) {
		close(entered)
		<-gate
		return []byte(`"s"`), nil
	}, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// Subscribe reads the sequence under the lock and then blocks in the
	// snapshot outside it.
	subs, errs := startSubscribe(t.Context(), hub, SubscribeParams{Principal: string(KindCLI), Topic: "t"})
	<-entered
	// Publishing while the snapshot is pending completes on its own, and the
	// events are queued for after the snapshot: no gap.
	<-commitDeltas(t, hub, "t", 10)
	close(gate)
	var sub *Subscription
	select {
	case sub = <-subs:
	case err := <-errs:
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	_, snapSeq, ok := sub.Snapshot()
	if !ok || snapSeq != 0 {
		t.Fatalf("the pending snapshot = (%v, %d), want true at 0", ok, snapSeq)
	}
	got := collectN(t, t.Context(), sub, 10)
	for i, ev := range got {
		if ev.Seq != uint64(i)+1 {
			t.Fatalf("event %d has seq %d, want %d", i, ev.Seq, i+1)
		}
	}
}
