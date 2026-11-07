package app

import (
	"context"
	"errors"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// EventSeq is one topic's monotonically increasing event sequence. Each topic
// counts from 1; sequence 0 means "before any event" (host-api §6).
type EventSeq = uint64

// TopicHost is the ephemeral topic carrying host lifecycle changes: client
// lease counts and the draining transition. Later items register the stage-A
// topics of host-api §6 (sessions, queue, interaction, transcript).
const TopicHost = "host"

// The event types published on TopicHost.
const (
	EventHostClients  = "host.clients"
	EventHostDraining = "host.draining"
)

const (
	// subscriberQueueCap bounds each subscriber's live queue. A full queue
	// never blocks the publisher: droppable deltas coalesce or drop, anything
	// else ends that subscriber's stream with resync_required.
	subscriberQueueCap = 512
	// defaultEphemeralBufferCap bounds an ephemeral topic's catch-up ring; a
	// cursor older than the ring falls back to a fresh snapshot.
	defaultEphemeralBufferCap = 256
)

// Event is one event on one topic. Payload is an opaque, topic-specific
// encoding — the hub never inspects it, so no protected payload may travel in
// Type or Key either.
type Event struct {
	Seq     EventSeq
	Type    string
	Key     string // coalescing key: consecutive droppable deltas with the same key merge
	Payload []byte
	// Droppable marks an ephemeral delta: under queue pressure it may be
	// coalesced into a newer delta or dropped (counted), never delivered late.
	// Control events are never dropped; their subscriber resynchronizes.
	Droppable bool
}

// EventCursor names a position in one topic's sequence: topic, the host
// incarnation that produced it, and the last sequence the client has.
type EventCursor struct {
	Topic       string
	Incarnation string
	Sequence    EventSeq
}

// DurableTopic is the port an owner of committed state implements so the hub
// can serve catch-up for a durable topic (F9.3/F9.4). Snapshot must be atomic
// against the owner's own commits: the returned state reflects exactly the
// returned sequence, so snapshot plus catch-up has no gap. Since returns the
// events after seq, oldest first, or ErrEventSeqTooOld when they are gone.
type DurableTopic interface {
	Snapshot(ctx context.Context) (state []byte, seq EventSeq, err error)
	Since(ctx context.Context, seq EventSeq) ([]Event, error)
}

// SubscribeParams are Subscribe's domain fields. Principal is the connection's
// authenticated local client kind; the session principal (host-api §7) has no
// transport yet and is denied by default.
type SubscribeParams struct {
	Principal string
	Topic     string
	Cursor    *EventCursor
}

// IsLocalPrincipal reports whether p is one of the local Host API client
// kinds. The session principal is never local: a future transport must allow
// it explicitly, never through this default.
func IsLocalPrincipal(p string) bool {
	switch ClientKind(p) {
	case KindTUI, KindCLI, KindApp, KindAppBackground:
		return true
	default:
		return false
	}
}

// topicKind distinguishes the two topic kinds of host-api §6.
type topicKind int

const (
	topicEphemeral topicKind = iota
	topicDurable
)

// eventTopic is one registered topic: its kind, its catch-up source and its
// live subscribers. Guarded by the hub mutex.
type eventTopic struct {
	name string
	kind topicKind
	// durable is the DurableTopic port for a durable topic; snapshot and ring
	// below belong to an ephemeral topic.
	durable  DurableTopic
	snapshot func(ctx context.Context) ([]byte, error)
	ringCap  int
	ring     []Event
	lastSeq  EventSeq
	subs     []*Subscription
}

// EventHub fans topic events out to subscribers. Publish never blocks and
// never holds a core transaction; every subscriber's queue is bounded, and the
// two topic kinds decide what pressure does (event-storage-recovery §9).
type EventHub struct {
	incarnation string

	mu       sync.Mutex
	topics   map[string]*eventTopic
	ended    bool
	authoriz func(principal, topic string) bool
	dropped  uint64
	coalesce uint64
}

// NewEventHub builds a hub for one host incarnation. The default authorizer
// denies every unknown topic and every non-local principal.
func NewEventHub(incarnation string) *EventHub {
	return &EventHub{incarnation: incarnation, topics: map[string]*eventTopic{}}
}

// RegisterDurable registers a durable topic backed by an owner of committed
// state. Registration is the authorization registry: an unregistered topic is
// denied at Subscribe.
func (h *EventHub) RegisterDurable(name string, src DurableTopic) error {
	return h.register(name, &eventTopic{name: name, kind: topicDurable, durable: src})
}

// RegisterEphemeral registers an ephemeral topic: an in-memory catch-up ring
// of bufferCap events plus a snapshot of the owner's current state. The
// snapshot call happens under the hub lock together with the sequence it
// reflects, so the pair is atomic against Publish.
func (h *EventHub) RegisterEphemeral(name string, snapshot func(ctx context.Context) ([]byte, error), bufferCap int) error {
	if bufferCap <= 0 {
		bufferCap = defaultEphemeralBufferCap
	}
	return h.register(name, &eventTopic{name: name, kind: topicEphemeral, snapshot: snapshot, ringCap: bufferCap})
}

func (h *EventHub) register(name string, t *eventTopic) error {
	if name == "" || (t.kind == topicDurable && t.durable == nil) || (t.kind == topicEphemeral && t.snapshot == nil) {
		return errcode.Wrap(errcode.Internal, "register the event topic "+name, ErrEventTopicInvalid)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.topics[name]; ok {
		return errcode.Wrap(errcode.Internal, "register the event topic "+name, ErrEventTopicInvalid)
	}
	h.topics[name] = t
	return nil
}

// SetAuthorizer replaces the topic-authorization decision. The function must
// answer for a principal and topic pair; nil restores the default, which
// allows exactly the registered topics to local principals.
func (h *EventHub) SetAuthorizer(authorize func(principal, topic string) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.authoriz = authorize
}

// authorize is called with the hub mutex held.
func (h *EventHub) authorize(principal, topic string) bool {
	if h.authoriz != nil {
		return h.authoriz(principal, topic)
	}
	_, ok := h.topics[topic]
	return ok && IsLocalPrincipal(principal)
}

// Publish delivers one event to every live subscriber of the topic and never
// blocks on a subscriber: a full queue coalesces or drops a droppable delta
// (counted) and otherwise ends that subscriber's stream with resync_required —
// a control event is never dropped silently.
//
// The sequence is the topic owner's concern: an ephemeral topic gets the next
// sequence from the hub, while a durable topic's owner sets ev.Seq from its
// committed log — that sequence survives a host restart, and a hub counter
// would not. A durable publish with a sequence at or below the last one is a
// programming error and fails typed.
func (h *EventHub) Publish(name string, ev Event) (Event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.topics[name]
	if !ok {
		return Event{}, errcode.Wrap(errcode.Internal, "publish to the event topic "+name, ErrEventTopicInvalid)
	}
	if t.kind == topicEphemeral {
		t.lastSeq++
		ev.Seq = t.lastSeq
		h.appendRingLocked(t, ev)
	} else {
		if ev.Seq == 0 || ev.Seq <= t.lastSeq {
			return Event{}, errcode.Wrap(errcode.Internal, "publish to the event topic "+name, ErrEventTopicInvalid)
		}
		t.lastSeq = ev.Seq
	}
	for _, s := range t.subs {
		if kind := s.offer(ev); kind == offerCoalesced {
			h.coalesce++
		} else if kind == offerDropped {
			h.dropped++
		}
	}
	return ev, nil
}

// appendRingLocked adds ev to an ephemeral topic's catch-up ring, coalescing
// into the tail when both are droppable deltas of the same key. A full ring
// evicts the oldest droppable entry; with none, the oldest entry makes room —
// any cursor that falls below the ring's oldest gets a fresh snapshot, so no
// client is ever handed a history with a silently missing control event.
func (h *EventHub) appendRingLocked(t *eventTopic, ev Event) {
	if n := len(t.ring); n > 0 && ev.Droppable && t.ring[n-1].Droppable && t.ring[n-1].Key == ev.Key {
		t.ring[n-1] = ev
		h.coalesce++
		return
	}
	if len(t.ring) < t.ringCap {
		t.ring = append(t.ring, ev)
		return
	}
	for i := range t.ring {
		if t.ring[i].Droppable {
			t.ring = append(t.ring[:i], t.ring[i+1:]...)
			h.dropped++
			break
		}
	}
	if len(t.ring) >= t.ringCap {
		t.ring = append(t.ring[:0], t.ring[1:]...)
	}
	t.ring = append(t.ring, ev)
}

// EndStreams ends every open subscription cleanly and refuses new ones. The
// drain calls it before the transport closes, so each stream delivers its end
// message and returns.
func (h *EventHub) EndStreams() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ended = true
	for _, t := range h.topics {
		for _, s := range t.subs {
			s.markEnded()
		}
	}
}

// Dropped reports how many droppable deltas were dropped under pressure.
func (h *EventHub) Dropped() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dropped
}

// Coalesced reports how many deltas were coalesced under pressure.
func (h *EventHub) Coalesced() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.coalesce
}

// Subscribe authorizes the request, registers the subscriber's queue first and
// only then takes the snapshot or catch-up, so nothing published in between is
// lost: the stream is the snapshot's sequence plus queued events with greater
// sequences, deduplicated by a monotone last-sent marker.
func (h *EventHub) Subscribe(ctx context.Context, p SubscribeParams) (*Subscription, error) {
	s, t, kind, ring, lastSeq, err := h.registerSubscriber(p)
	if err != nil {
		return nil, err
	}
	seed, err := h.seedStream(ctx, t, kind, ring, lastSeq, p)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.deliver(seed.pending, seed.snapState, seed.snapSeq, seed.haveSnapshot)
	return s, nil
}

// streamSeed is what Subscribe resolved for a new subscription: the events to
// deliver before the live queue and the snapshot the handler sends first.
type streamSeed struct {
	pending      []Event
	snapState    []byte
	snapSeq      EventSeq
	haveSnapshot bool
}

// registerSubscriber authorizes the request and appends the subscriber to the
// topic before any snapshot is taken, so nothing published from here on can
// be lost. The topic's kind, ring copy and sequence come back for the cursor
// resolution that follows without the hub lock.
func (h *EventHub) registerSubscriber(p SubscribeParams) (*Subscription, *eventTopic, topicKind, []Event, EventSeq, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ended {
		return nil, nil, 0, nil, 0, ErrHostStopping
	}
	t, ok := h.topics[p.Topic]
	if !ok || !h.authorize(p.Principal, p.Topic) {
		return nil, nil, 0, nil, 0, ErrSubscriptionDenied
	}
	s := &Subscription{topic: p.Topic, hub: h, notify: make(chan struct{}, 1)}
	t.subs = append(t.subs, s)
	return s, t, t.kind, append([]Event(nil), t.ring...), t.lastSeq, nil
}

// seedStream resolves the cursor to a snapshot or to catch-up events.
func (h *EventHub) seedStream(ctx context.Context, t *eventTopic, kind topicKind, ring []Event, lastSeq EventSeq, p SubscribeParams) (streamSeed, error) {
	switch {
	case p.Cursor == nil:
		return h.snapshotSeed(ctx, t)
	case kind == topicDurable:
		return h.durableSeed(ctx, t, p.Cursor)
	default:
		return h.ephemeralSeed(ctx, t, ring, lastSeq, p.Cursor)
	}
}

// snapshotSeed takes a full snapshot of the topic.
func (h *EventHub) snapshotSeed(ctx context.Context, t *eventTopic) (streamSeed, error) {
	state, seq, err := h.topicSnapshot(ctx, t)
	if err != nil {
		return streamSeed{}, err
	}
	return streamSeed{snapState: state, snapSeq: seq, haveSnapshot: true}, nil
}

// durableSeed catches a durable topic up from the cursor's sequence; a purged
// sequence falls back to a snapshot. The durable sequence ignores the cursor's
// incarnation: it lives in the store and survives a restart.
func (h *EventHub) durableSeed(ctx context.Context, t *eventTopic, c *EventCursor) (streamSeed, error) {
	events, err := t.durable.Since(ctx, c.Sequence)
	if err == nil {
		return streamSeed{pending: events, snapSeq: c.Sequence}, nil
	}
	if !errors.Is(err, ErrEventSeqTooOld) {
		return streamSeed{}, errcode.Wrap(errcode.Internal, "catch up the event topic "+t.name, err)
	}
	return h.snapshotSeed(ctx, t)
}

// ephemeralSeed catches an ephemeral topic up from its in-memory ring; a
// cursor from another incarnation, a future sequence or a sequence below the
// ring falls back to a fresh snapshot.
func (h *EventHub) ephemeralSeed(ctx context.Context, t *eventTopic, ring []Event, lastSeq EventSeq, c *EventCursor) (streamSeed, error) {
	if c.Incarnation != h.incarnation || c.Sequence > lastSeq || !ringCovers(ring, lastSeq, c.Sequence) {
		return h.snapshotSeed(ctx, t)
	}
	var pending []Event
	for _, ev := range ring {
		if ev.Seq > c.Sequence {
			pending = append(pending, ev)
		}
	}
	return streamSeed{pending: pending, snapSeq: c.Sequence}, nil
}

// topicSnapshot takes a topic's snapshot with the sequence it reflects. A
// durable topic's owner answers atomically from its store; an ephemeral
// snapshot is taken under the hub lock together with the topic sequence.
func (h *EventHub) topicSnapshot(ctx context.Context, t *eventTopic) ([]byte, EventSeq, error) {
	if t.kind == topicDurable {
		state, seq, err := t.durable.Snapshot(ctx)
		if err != nil {
			return nil, 0, errcode.Wrap(errcode.Internal, "snapshot the event topic "+t.name, err)
		}
		return state, seq, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := t.snapshot(ctx)
	if err != nil {
		return nil, 0, errcode.Wrap(errcode.Internal, "snapshot the event topic "+t.name, err)
	}
	return state, t.lastSeq, nil
}

// ringCovers reports whether an ephemeral ring reaches back far enough for
// catch-up from seq: the client needs every event after seq, so the oldest
// retained event must be seq+1 at most.
func ringCovers(ring []Event, lastSeq, seq EventSeq) bool {
	oldest := lastSeq + 1
	if len(ring) > 0 {
		oldest = ring[0].Seq
	}
	return seq+1 >= oldest
}
