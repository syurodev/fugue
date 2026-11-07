package app

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// newTestHub returns a hub with one ephemeral topic "t" whose snapshot is a
// canned state; the ring is large enough that tests control eviction
// explicitly.
const (
	// testIncarnation is the incarnation the test hubs run in.
	testIncarnation = "inc-1"
	// testDelta is the event type of the droppable deltas in these tests.
	testDelta = "delta"
)

func newTestHub(t *testing.T) *EventHub {
	t.Helper()
	hub := NewEventHub(testIncarnation)
	err := hub.RegisterEphemeral("t", func(context.Context) ([]byte, error) {
		return []byte(`"state"`), nil
	}, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	return hub
}

// publishN publishes n control events on the test topic "t" and returns the
// last sequenced event.
func publishN(t *testing.T, hub *EventHub, n int) Event {
	t.Helper()
	var last Event
	for range n {
		ev, err := hub.Publish("t", Event{Type: "e", Payload: []byte("p")})
		if err != nil {
			t.Fatal(err)
		}
		last = ev
	}
	return last
}

// collect drains sub until its stream ends, returning the events in delivery
// order. Only for streams that end; a live stream needs collectN.
func collect(t *testing.T, ctx context.Context, sub *Subscription) []Event {
	t.Helper()
	var got []Event
	for {
		ev, ok := sub.Next(ctx)
		if !ok {
			return got
		}
		got = append(got, ev)
	}
}

// subscribeCursor subscribes with an explicit cursor and cleans up.
// subscribeCursor subscribes to the test topic "t" with an explicit cursor.
func subscribeCursor(t *testing.T, hub *EventHub, incarnation string, seq EventSeq) *Subscription {
	t.Helper()
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI), Topic: "t",
		Cursor: &EventCursor{Topic: "t", Incarnation: incarnation, Sequence: seq},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return sub
}

// collectN reads exactly n events from a live stream.
func collectN(t *testing.T, ctx context.Context, sub *Subscription, n int) []Event {
	t.Helper()
	got := make([]Event, 0, n)
	for i := range n {
		ev, ok := sub.Next(ctx)
		if !ok {
			t.Fatalf("the stream ended after %d of %d events", i, n)
		}
		got = append(got, ev)
	}
	return got
}

func TestSubscribeWithoutCursorGetsSnapshotThenEvents(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	last := publishN(t, hub, 3)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	state, seq, ok := sub.Snapshot()
	if !ok || string(state) != `"state"` || seq != last.Seq {
		t.Fatalf("snapshot = (%s, %d, %v), want (\"state\", %d, true)", state, seq, ok, last.Seq)
	}
	publishN(t, hub, 2)
	got := collectN(t, t.Context(), sub, 2)
	for i, ev := range got {
		if ev.Seq != last.Seq+uint64(i)+1 {
			t.Fatalf("event %d has seq %d, want %d", i, ev.Seq, last.Seq+uint64(i)+1)
		}
	}
}

func TestEphemeralCursorCatchUpFromTheBuffer(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	last := publishN(t, hub, 5)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI), Topic: "t",
		Cursor: &EventCursor{Topic: "t", Incarnation: testIncarnation, Sequence: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	if _, _, ok := sub.Snapshot(); ok {
		t.Fatal("a catch-up subscription got a snapshot")
	}
	got := collectN(t, t.Context(), sub, 3)
	wantSeqs := []EventSeq{3, 4, 5}
	for i, ev := range got {
		if ev.Seq != wantSeqs[i] || ev.Seq > last.Seq {
			t.Fatalf("event %d = seq %d, want %d", i, ev.Seq, wantSeqs[i])
		}
	}
}

func TestEphemeralCursorOlderThanBufferGetsSnapshot(t *testing.T) {
	t.Parallel()
	hub := NewEventHub(testIncarnation)
	err := hub.RegisterEphemeral("t", func(context.Context) ([]byte, error) { return []byte(`"state"`), nil }, 4)
	if err != nil {
		t.Fatal(err)
	}
	publishN(t, hub, 10)
	sub := subscribeCursor(t, hub, testIncarnation, 2)
	_, seq, ok := sub.Snapshot()
	if !ok || seq != 10 {
		t.Fatalf("a too-old cursor got (snapshot=%v, seq=%d), want a snapshot at 10", ok, seq)
	}
}

func TestEphemeralCursorIncarnationMismatchGetsSnapshot(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	publishN(t, hub, 3)
	sub := subscribeCursor(t, hub, "inc-old", 2)
	if _, _, ok := sub.Snapshot(); !ok {
		t.Fatal("a cursor from another incarnation did not get a snapshot")
	}
}

func TestEphemeralCursorInTheFutureGetsSnapshot(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	publishN(t, hub, 2)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI), Topic: "t",
		Cursor: &EventCursor{Topic: "t", Incarnation: testIncarnation, Sequence: 9},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	if _, _, ok := sub.Snapshot(); !ok {
		t.Fatal("a future cursor did not get a snapshot")
	}
}

func TestNoGapBetweenSnapshotAndStreamUnderConcurrentPublish(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	const firstHalf, secondHalf = 100, 100
	publishN(t, hub, firstHalf)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	_, snapSeq, ok := sub.Snapshot()
	if !ok || snapSeq != firstHalf {
		t.Fatalf("snapshot = (%v, %d), want true at %d", ok, snapSeq, firstHalf)
	}
	// Publishing continues while the stream is live: every later event is
	// delivered exactly once, in order, with no gap.
	done := make(chan struct{})
	_ = supervise.Go(t.Context(), "test.publish", func(context.Context) error {
		defer close(done)
		for range secondHalf {
			if _, err := hub.Publish("t", Event{Type: "e", Payload: []byte("p")}); err != nil {
				t.Error(err)
				return err
			}
		}
		return nil
	})
	got := make([]Event, 0, secondHalf)
	for i := range secondHalf {
		ev, ok := sub.Next(t.Context())
		if !ok {
			t.Fatalf("the stream ended after %d of %d events", i, secondHalf)
		}
		got = append(got, ev)
	}
	<-done
	for i, ev := range got {
		if want := snapSeq + uint64(i) + 1; ev.Seq != want {
			t.Fatalf("event %d has seq %d, want %d", i, ev.Seq, want)
		}
	}
}

func TestPublishNeverBlocksOnASlowSubscriber(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	// Distinct keys: nothing coalesces, the stalled queue keeps its cap and
	// the rest of the deltas is dropped. The publish loop must return on its
	// own — a blocked publish would hang this test, never time out waiting.
	const n = 3 * subscriberQueueCap
	done := make(chan struct{})
	_ = supervise.Go(t.Context(), "test.publish", func(context.Context) error {
		defer close(done)
		for i := range n {
			if _, err := hub.Publish("t", Event{Type: testDelta, Key: "item-" + string(rune(i)), Payload: []byte("v"), Droppable: true}); err != nil {
				t.Error(err)
				return err
			}
		}
		return nil
	})
	<-done
	if got := hub.Dropped(); got != uint64(n-subscriberQueueCap) {
		t.Fatalf("dropped = %d, want %d", got, n-subscriberQueueCap)
	}
	// A delta overflow never ends the stream: the reader still gets what the
	// queue kept, and no resync is asked for.
	ev, ok := sub.Next(t.Context())
	if !ok || ev.Seq != 1 || sub.ResyncRequired() {
		t.Fatalf("the slow reader got (seq=%d, ok=%v, resync=%v), want seq 1, ok, no resync", ev.Seq, ok, sub.ResyncRequired())
	}
}

func TestSlowSubscriberCoalescesDeltasOfTheSameKey(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	const n = 5 * subscriberQueueCap
	done := make(chan struct{})
	_ = supervise.Go(t.Context(), "test.publish", func(context.Context) error {
		defer close(done)
		for range n {
			if _, err := hub.Publish("t", Event{Type: testDelta, Key: "item-1", Payload: []byte("v"), Droppable: true}); err != nil {
				t.Error(err)
				return err
			}
		}
		return nil
	})
	<-done
	// Same-key deltas merge, so the stalled queue keeps one entry and nothing
	// is dropped; the newest delta is the one the reader sees.
	if got := hub.Dropped(); got != 0 {
		t.Fatalf("dropped = %d, want 0", got)
	}
	if got := hub.Coalesced(); got == 0 {
		t.Fatal("no delta was coalesced")
	}
	ev, ok := sub.Next(t.Context())
	if !ok || ev.Type != "delta" || ev.Seq != n {
		t.Fatalf("the reader got (seq=%d, ok=%v), want the newest delta at seq %d", ev.Seq, ok, n)
	}
}

func TestControlOverflowEndsWithResync(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	// Fill the queue exactly, then one more control event overflows it: the
	// buffered events are delivered, then the stream ends with resync.
	publishN(t, hub, subscriberQueueCap)
	publishN(t, hub, 1)
	for {
		if _, ok := sub.Next(t.Context()); !ok {
			break
		}
	}
	if !sub.ResyncRequired() {
		t.Fatal("a control overflow must end with resync_required")
	}
	// The publisher keeps publishing without blocking or dropping silently.
	publishN(t, hub, 5)
	if got := hub.Dropped(); got != 0 {
		t.Fatalf("control events were counted as dropped: %d", got)
	}
}

func TestUnknownTopicIsDenied(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	_, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "nope"})
	if errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("unknown topic = %v, want permission_denied", err)
	}
}

func TestUnknownPrincipalIsDenied(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	for _, principal := range []string{"", "nobody"} {
		_, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: principal, Topic: "t"})
		if errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("principal %q = %v, want permission_denied", principal, err)
		}
	}
}

func TestSessionPrincipalIsDenied(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	// The session principal of host-api §7 exists in the model but is never
	// allowed by the default; a future token transport must allow it
	// explicitly.
	_, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: "session", Topic: "t"})
	if errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("session principal = %v, want permission_denied", err)
	}
	if IsLocalPrincipal("session") {
		t.Fatal("session counts as a local principal")
	}
}

func TestAuthorizerHookOverridesTheDefault(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	hub.SetAuthorizer(func(principal, topic string) bool { return false })
	if _, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "t"}); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("a denying hook = %v, want permission_denied", err)
	}
	hub.SetAuthorizer(nil)
	if _, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "t"}); err != nil {
		t.Fatalf("nil restores the default: %v", err)
	}
}

func TestSubscribeDuringDrainIsUnavailable(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	hub.EndStreams()
	_, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "t"})
	if errcode.CodeOf(err) != errcode.Unavailable {
		t.Fatalf("subscribe after EndStreams = %v, want unavailable", err)
	}
}

func TestEndStreamsEndsOpenSubscriptions(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	publishN(t, hub, 2)
	hub.EndStreams()
	got := collect(t, t.Context(), sub)
	if len(got) != 2 {
		t.Fatalf("the drain delivered %d queued events before ending, want 2", len(got))
	}
	if sub.ResyncRequired() {
		t.Fatal("a drain end must not ask for a resync")
	}
}

// startSubscribe runs hub.Subscribe on a goroutine for a source whose
// Snapshot blocks; the result arrives once the test releases it.
func startSubscribe(ctx context.Context, hub *EventHub, p SubscribeParams) (<-chan *Subscription, <-chan error) {
	subs := make(chan *Subscription, 1)
	errs := make(chan error, 1)
	_ = supervise.Go(ctx, "test.subscribe", func(context.Context) error {
		sub, err := hub.Subscribe(ctx, p)
		if err != nil {
			errs <- err
			return nil
		}
		subs <- sub
		return nil
	})
	return subs, errs
}

// commitAsync commits n durable events on a goroutine; a failure fails the
// test through t.Error and stops the run early.
func commitAsync(t *testing.T, fd *fakeDurable, hub *EventHub, topic string, n int) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	_ = supervise.Go(t.Context(), "test.commit", func(context.Context) error {
		defer close(done)
		for range n {
			if _, err := fd.commit(hub, topic, "e"); err != nil {
				t.Error(err)
				return err
			}
		}
		return nil
	})
	return done
}

// fakeDurable is a scripted DurableTopic: an in-memory committed log whose
// Snapshot can block on a gate.
type fakeDurable struct {
	mu       sync.Mutex
	state    []byte
	seq      EventSeq
	events   []Event
	purged   EventSeq      // Since(seq) with seq < purged reports ErrEventSeqTooOld
	snapGate chan struct{} // non-nil: Snapshot blocks until closed
	entered  chan struct{} // non-nil: closed when Snapshot has read the state
}

func (f *fakeDurable) Snapshot(_ context.Context) ([]byte, EventSeq, error) {
	// A slow read returns the state it started with: capture first, then
	// block, the way a store read that races commits behaves.
	f.mu.Lock()
	state, seq := append([]byte(nil), f.state...), f.seq
	f.mu.Unlock()
	if f.entered != nil {
		close(f.entered)
	}
	if f.snapGate != nil {
		<-f.snapGate
	}
	return state, seq, nil
}

func (f *fakeDurable) Since(_ context.Context, seq EventSeq) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if seq < f.purged {
		return nil, ErrEventSeqTooOld
	}
	var out []Event
	for _, ev := range f.events {
		if ev.Seq > seq {
			out = append(out, ev)
		}
	}
	return out, nil
}

// commit appends to the store's log with its own sequence — the durable
// sequence lives in the store and survives a restart — then publishes that
// sequenced event through the hub.
func (f *fakeDurable) commit(hub *EventHub, topic, evType string) (Event, error) {
	f.mu.Lock()
	f.seq++
	f.state = []byte(evType)
	f.events = append(f.events, Event{Seq: f.seq, Type: evType, Payload: []byte(evType)})
	seq := f.seq
	f.mu.Unlock()
	ev, err := hub.Publish(topic, Event{Seq: seq, Type: evType, Payload: []byte(evType)})
	if err != nil {
		return Event{}, err
	}
	return ev, nil
}

// mustCommit is commit for calls on the test goroutine; the durable test
// topic is always "d".
func (f *fakeDurable) mustCommit(t *testing.T, hub *EventHub, evType string) Event {
	t.Helper()
	ev, err := f.commit(hub, "d", evType)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

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

func TestRegisterRejectsDuplicatesAndInvalidTopics(t *testing.T) {
	t.Parallel()
	hub := newTestHub(t)
	err := hub.RegisterEphemeral("t", func(context.Context) ([]byte, error) { return nil, nil }, 0)
	if errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("duplicate registration = %v, want internal", err)
	}
	err = hub.RegisterEphemeral("", func(context.Context) ([]byte, error) { return nil, nil }, 0)
	if errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("empty name registration = %v, want internal", err)
	}
	err = hub.RegisterDurable("d2", nil)
	if errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("nil source registration = %v, want internal", err)
	}
	if _, err := hub.Publish("nope", Event{Type: "e"}); errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("publish to an unknown topic = %v, want internal", err)
	}
}

func TestEphemeralRingEvictionForcesSnapshot(t *testing.T) {
	t.Parallel()
	hub := NewEventHub(testIncarnation)
	err := hub.RegisterEphemeral("t", func(context.Context) ([]byte, error) { return []byte(`"s"`), nil }, 4)
	if err != nil {
		t.Fatal(err)
	}
	// A full ring of control events: a delta evicts the oldest entry so the
	// ring stays bounded; a cursor below the ring falls back to a snapshot,
	// so no client is handed a history with a missing event.
	publishN(t, hub, 4)
	if _, err := hub.Publish("t", Event{Type: testDelta, Key: "k1", Droppable: true}); err != nil {
		t.Fatal(err)
	}
	tooOld := subscribeCursor(t, hub, testIncarnation, 0)
	if _, _, ok := tooOld.Snapshot(); !ok {
		t.Fatal("a cursor below the ring did not get a snapshot")
	}
	// The ring holds seqs 2..5: a cursor at 2 catches up.
	edge := subscribeCursor(t, hub, testIncarnation, 2)
	if _, _, ok := edge.Snapshot(); ok {
		t.Fatal("a cursor at the ring's edge got a snapshot")
	}
	got := collectN(t, t.Context(), edge, 3)
	for i, want := range []EventSeq{3, 4, 5} {
		if got[i].Seq != want {
			t.Fatalf("catch-up event %d = seq %d, want %d", i, got[i].Seq, want)
		}
	}
}

func TestEphemeralRingEvictsADroppableEntryFirst(t *testing.T) {
	t.Parallel()
	hub := NewEventHub(testIncarnation)
	err := hub.RegisterEphemeral("t", func(context.Context) ([]byte, error) { return []byte(`"s"`), nil }, 4)
	if err != nil {
		t.Fatal(err)
	}
	// One delta in a full ring: the next control event evicts that delta —
	// counted, and the control history stays complete.
	publishN(t, hub, 4)
	if _, err := hub.Publish("t", Event{Type: testDelta, Key: "k1", Droppable: true}); err != nil {
		t.Fatal(err)
	}
	publishN(t, hub, 1)
	if got := hub.Dropped(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	after := subscribeCursor(t, hub, testIncarnation, 2)
	got := collectN(t, t.Context(), after, 3)
	for i, want := range []EventSeq{3, 4, 6} {
		if got[i].Seq != want {
			t.Fatalf("catch-up after eviction: event %d = seq %d, want %d", i, got[i].Seq, want)
		}
	}
}

func TestCoalescedRingKeepsCatchUpMonotone(t *testing.T) {
	t.Parallel()
	hub := NewEventHub(testIncarnation)
	err := hub.RegisterEphemeral("t", func(context.Context) ([]byte, error) { return []byte(`"s"`), nil }, 8)
	if err != nil {
		t.Fatal(err)
	}
	publishN(t, hub, 2)
	for range 3 {
		if _, perr := hub.Publish("t", Event{Type: testDelta, Key: "k", Droppable: true}); perr != nil {
			t.Fatal(perr)
		}
	}
	// A cursor at 1 catches up to the merged delta at seq 5; the coalesced
	// middle sequences are gone by design.
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI), Topic: "t",
		Cursor: &EventCursor{Topic: "t", Incarnation: testIncarnation, Sequence: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	got := collectN(t, t.Context(), sub, 2)
	want := []EventSeq{2, 5}
	for i, ev := range got {
		if ev.Seq != want[i] {
			t.Fatalf("event %d = seq %d, want %d", i, ev.Seq, want[i])
		}
	}
	if diff := cmp.Diff(EventSeq(5), got[len(got)-1].Seq); diff != "" {
		t.Fatalf("last seq mismatch (-want +got):\n%s", diff)
	}
}
