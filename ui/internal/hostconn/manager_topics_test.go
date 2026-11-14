package hostconn

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"

	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
)

// topicScript serves the host topic and one dynamic topic: the dynamic topic
// answers a snapshot on the first subscribe and one catch-up event on every
// later one, and records every cursor it was handed.
type topicScript struct {
	mu         sync.Mutex
	cursors    map[string][]uint64
	curCursor  uint64
	hasCursor  bool
	topic      string
	catchUpSeq uint64
}

// subscribe is the fake handler's callback.
func (ts *topicScript) subscribe(ctx context.Context, req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
	ts.mu.Lock()
	ts.hasCursor = req.Msg.GetCursor() != nil
	ts.curCursor = req.Msg.GetCursor().GetSequence()
	ts.cursors[req.Msg.Topic] = append(ts.cursors[req.Msg.Topic], ts.curCursor)
	ts.mu.Unlock()
	if req.Msg.Topic != ts.topic {
		if ts.hasCursor {
			return fmt.Errorf("the host topic never carries a cursor: %w", errTransportFailure)
		}
		if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
			Snapshot: &hostv1.SnapshotEvent{Sequence: 1, IncarnationId: testIncA},
		}}); err != nil {
			return fmt.Errorf("send the snapshot: %w", err)
		}
		<-ctx.Done()
		return nil
	}
	if ts.hasCursor {
		if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Event{
			Event: &hostv1.HostEvent{Sequence: ts.catchUpSeq, Type: testTrailer},
		}}); err != nil {
			return fmt.Errorf("send the catch-up event: %w", err)
		}
		return nil
	}
	if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
		Snapshot: &hostv1.SnapshotEvent{Sequence: 10, IncarnationId: testIncA},
	}}); err != nil {
		return fmt.Errorf("send the snapshot: %w", err)
	}
	<-ctx.Done()
	return nil
}

// catchUpCursors reports the cursors every re-subscribe of the dynamic topic
// carried, after the first.
func (ts *topicScript) reSubscribeCursors(topic string) []uint64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.cursors[topic][1:]
}

// TestManagerAddsAndRemovesTopicsWhileConnected pins the per-session topic
// lifecycle of F10.4a: a topic added after the connect streams from a
// snapshot, a removed topic stops, and a re-added topic catches up from its
// kept cursor instead of reloading.
func TestManagerAddsAndRemovesTopicsWhileConnected(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	script := &topicScript{
		cursors:    map[string][]uint64{},
		topic:      "transcript:s1",
		catchUpSeq: 11,
	}
	socket := serveFakeHost(t, &fakeHandler{subscribe: script.subscribe})
	clock := newFakeClock()
	m, upd := newTestManager(t, dataDirWithHost(t, socket), clock, testTopicHost)
	m.Start(ctx)
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateConnected })
	if m.Client() == nil {
		t.Fatal("connected manager exposes no client")
	}

	// A topic added after the connect starts streaming at once.
	m.AddTopic(script.topic)
	upd.waitFor(t, ctx, func(u Update) bool { return u.Topic == script.topic && u.Snapshot != nil })

	// Removing it stops its pump: the next AddTopic re-subscribes, from the
	// kept cursor and without a snapshot.
	m.RemoveTopic(script.topic)
	m.AddTopic(script.topic)
	upd.waitFor(t, ctx, func(u Update) bool {
		return u.Topic == script.topic && u.Event != nil && u.Event.Sequence == script.catchUpSeq
	})
	for _, seq := range script.reSubscribeCursors(script.topic) {
		if seq != 10 {
			t.Fatalf("dynamic topic subscribed from cursor %d, want the kept 10", seq)
		}
	}
}
