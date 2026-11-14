package hostconn

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"

	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
)

// TestManagerAddsAndRemovesTopicsWhileConnected pins the per-session topic
// lifecycle of F10.4a: a topic added after the connect streams from a
// snapshot, a removed topic stops, and a re-added topic catches up from its
// kept cursor instead of reloading.
func TestManagerAddsAndRemovesTopicsWhileConnected(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)

	var mu sync.Mutex
	// subscribes records, per topic, the cursors each subscribe carried.
	subscribes := map[string][]uint64{}
	const dynamicTopic = "transcript:s1"
	socket := serveFakeHost(t, &fakeHandler{subscribe: func(ctx context.Context, req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		mu.Lock()
		subscribes[req.Msg.Topic] = append(subscribes[req.Msg.Topic], req.Msg.GetCursor().GetSequence())
		mu.Unlock()
		switch req.Msg.Topic {
		case testTopicHost:
			if req.Msg.GetCursor() != nil {
				return fmt.Errorf("the host topic never carries a cursor: %w", errTransportFailure)
			}
			if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
				Snapshot: &hostv1.SnapshotEvent{Sequence: 1, IncarnationId: testIncA},
			}}); err != nil {
				return fmt.Errorf("send the snapshot: %w", err)
			}
			<-ctx.Done()
			return nil
		case dynamicTopic:
			if req.Msg.GetCursor() != nil {
				// The re-added topic catches up: one event, no snapshot.
				return stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Event{
					Event: &hostv1.HostEvent{Sequence: 11, Type: testTrailer},
				}})
			}
			if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
				Snapshot: &hostv1.SnapshotEvent{Sequence: 10, IncarnationId: testIncA},
			}}); err != nil {
				return fmt.Errorf("send the snapshot: %w", err)
			}
			<-ctx.Done()
			return nil
		default:
			return connect.NewError(connect.CodeNotFound, errNoSubscriptionScripted)
		}
	}})

	clock := newFakeClock()
	m, upd := newTestManager(t, dataDirWithHost(t, socket), clock, testTopicHost)
	m.Start(ctx)
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateConnected })
	if m.Client() == nil {
		t.Fatal("connected manager exposes no client")
	}

	// A topic added after the connect starts streaming at once.
	m.AddTopic(dynamicTopic)
	upd.waitFor(t, ctx, func(u Update) bool { return u.Topic == dynamicTopic && u.Snapshot != nil })

	// Removing it stops its pump: the next AddTopic re-subscribes, from the
	// kept cursor and without a snapshot.
	m.RemoveTopic(dynamicTopic)
	m.AddTopic(dynamicTopic)
	upd.waitFor(t, ctx, func(u Update) bool {
		return u.Topic == dynamicTopic && u.Event != nil && u.Event.Sequence == 11
	})
	mu.Lock()
	defer mu.Unlock()
	for _, seq := range subscribes[dynamicTopic][1:] {
		if seq != 10 {
			t.Fatalf("dynamic topic subscribed from cursor %d, want the kept 10", seq)
		}
	}
}
