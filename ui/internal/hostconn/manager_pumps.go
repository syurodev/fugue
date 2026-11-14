package hostconn

import (
	"context"

	"github.com/syurodev/fugue/pkg/client"
)

// The per-topic stream pumps of one leased connection: one goroutine each,
// started and stopped by the manager's stream loop.

// pump streams one topic: it subscribes from the stored cursor — a cursor the
// host can no longer catch up from, including one from a dead host
// incarnation, is answered with a fresh snapshot (host-api §6) — and forwards
// every message to the window. The stream lives while ctx does.
func (m *Manager) pump(ctx context.Context, conn *client.Client, topic string) {
	stream, err := conn.Subscribe(ctx, topic, m.cursorFor(topic))
	if err != nil {
		m.report(ctx, topic, err)
		return
	}
	defer func() {
		// The stream is ending either way; a close error cannot be acted on.
		if cerr := stream.Close(); cerr != nil {
			return
		}
	}()
	for {
		msg, err := stream.Recv()
		if err != nil {
			m.report(ctx, topic, err)
			return
		}
		switch {
		case msg.Snapshot != nil:
			m.setCursor(topic, client.Cursor{
				Topic:       topic,
				Incarnation: msg.Snapshot.Incarnation,
				Sequence:    msg.Snapshot.Sequence,
			})
			m.deliver(ctx, Update{Topic: topic, Snapshot: msg.Snapshot})
		case msg.Event != nil:
			c := client.Cursor{Topic: topic, Sequence: msg.Event.Sequence}
			if old := m.cursorFor(topic); old != nil {
				c.Incarnation = old.Incarnation
			}
			m.setCursor(topic, c)
			m.deliver(ctx, Update{Topic: topic, Event: msg.Event})
		}
	}
}

// report hands a stream's end reason to the run loop, giving up when the
// stream's context ends first.
func (m *Manager) report(ctx context.Context, topic string, err error) {
	select {
	case m.failures <- streamFailure{topic: topic, err: err}:
	case <-ctx.Done():
	}
}

// pump is one topic's stream goroutine and its cancel.
type pump struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// pumpSet owns the stream goroutines of one leased connection.
type pumpSet struct {
	mgr   *Manager
	conn  *client.Client
	pumps map[string]*pump
}

// start launches (or relaunches) one topic's pump. A fresh start drops the
// stored cursor so the host answers with a snapshot.
func (ps *pumpSet) start(ctx context.Context, topic string, fresh bool) {
	if fresh {
		ps.mgr.clearCursor(topic)
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &pump{cancel: cancel, done: make(chan struct{})}
	ps.pumps[topic] = p
	go func() {
		defer close(p.done)
		ps.mgr.pump(pctx, ps.conn, topic)
	}()
}

// stopAll cancels every pump and waits for the goroutines.
func (ps *pumpSet) stopAll() {
	for _, p := range ps.pumps {
		p.cancel()
	}
	for _, p := range ps.pumps {
		<-p.done
	}
}

// applyReq starts or stops one dynamic topic's pump. An already-streaming
// topic — a configured one, or one requested twice — keeps its pump.
func (ps *pumpSet) applyReq(ctx context.Context, req topicReq) {
	p, ok := ps.pumps[req.topic]
	if req.add {
		if !ok {
			ps.start(ctx, req.topic, false)
		}
		return
	}
	if ok {
		p.cancel()
		<-p.done
		delete(ps.pumps, req.topic)
	}
}

// await waits for the first stream failure for a topic in this set, the next
// dynamic-topic request, or the end of ctx; ok is false when the context
// ended.
func (ps *pumpSet) await(ctx context.Context) (streamFailure, bool) {
	for {
		select {
		case <-ctx.Done():
			return streamFailure{}, false
		case f := <-ps.mgr.failures:
			p, ok := ps.pumps[f.topic]
			if !ok {
				continue
			}
			p.cancel()
			<-p.done
			return f, true
		case req := <-ps.mgr.topicReqs:
			ps.applyReq(ctx, req)
		}
	}
}
