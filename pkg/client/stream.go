package client

import (
	"fmt"

	"connectrpc.com/connect"

	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
)

// Cursor names a position in one topic's event sequence (host-api §6). It is
// what a caller passes back to Subscribe to catch up after a dropped
// connection: the topic, the host incarnation that produced the sequence, and
// the last sequence the caller processed.
type Cursor struct {
	Topic       string
	Incarnation string
	Sequence    uint64
}

// Snapshot is a topic's full state and the sequence it reflects (host-api §6).
// Every event that follows has a greater sequence. Incarnation is the host
// incarnation the snapshot belongs to: the caller builds its next cursor from
// it without calling HostStatus.
type Snapshot struct {
	Sequence    uint64
	State       []byte
	Incarnation string
}

// Event is one event on the topic: an opaque, topic-specific payload, ordered
// by Sequence within the incarnation.
type Event struct {
	Sequence uint64
	Type     string
	Payload  []byte
}

// Message is one received subscription message; exactly one field is set.
type Message struct {
	Snapshot *Snapshot
	Event    *Event
}

// Stream is one open subscription. Recv drives it; the caller owns closing
// it — a stream abandoned before its end must be closed, or its HTTP/2 stream
// holds the leased connection open (F5.2 review). Recv and Close are not safe
// for concurrent use.
type Stream struct {
	inner *connect.ServerStreamForClient[hostv1.SubscribeResponse]
}

// Recv returns the next message. A stream the host ended reports
// ErrResyncRequired when the caller must discard its state and subscribe
// fresh, and ErrStreamEnded on a clean end (the host is draining or ended the
// subscription); a transport or context failure surfaces as the error it is.
// After a typed end, the stream is finished and Close is a no-op.
func (s *Stream) Recv() (*Message, error) {
	if !s.inner.Receive() {
		if err := s.inner.Err(); err != nil {
			return nil, fmt.Errorf("receive from the host stream: %w", err)
		}
		return nil, ErrStreamEnded
	}
	switch kind := s.inner.Msg().GetKind().(type) {
	case *hostv1.SubscribeResponse_Snapshot:
		return &Message{Snapshot: &Snapshot{
			Sequence:    kind.Snapshot.GetSequence(),
			State:       kind.Snapshot.GetState(),
			Incarnation: kind.Snapshot.GetIncarnationId(),
		}}, nil
	case *hostv1.SubscribeResponse_Event:
		return &Message{Event: &Event{
			Sequence: kind.Event.GetSequence(),
			Type:     kind.Event.GetType(),
			Payload:  kind.Event.GetPayload(),
		}}, nil
	case *hostv1.SubscribeResponse_End:
		if kind.End.GetResyncRequired() {
			return nil, ErrResyncRequired
		}
		return nil, ErrStreamEnded
	default:
		return nil, ErrStreamMalformed
	}
}

// Close ends the receive side of a stream the caller abandons before its end.
// It is non-blocking; closing an already-ended stream is a no-op.
func (s *Stream) Close() error {
	if err := s.inner.Close(); err != nil {
		return fmt.Errorf("close the host stream: %w", err)
	}
	return nil
}
