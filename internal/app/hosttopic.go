package app

// The host topic: the runtime publishes lease-count changes and the draining
// transition on the ephemeral "host" topic, and answers its snapshots. Owner:
// application layer. Allowed callers: none outside this package.

import (
	"context"
	"encoding/json"
	"log/slog"
)

// hostTopicSnapshot snapshots the host topic's state: the lease counts and the
// draining flag as JSON. The hub pairs it with the topic sequence atomically.
func (rt *HostRuntime) hostTopicSnapshot(_ context.Context) ([]byte, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return json.Marshal(hostState{
		Clients:  rt.countsLocked(),
		Draining: rt.draining,
	})
}

// publishHostClients publishes the current lease counts on the host topic.
// Called after the runtime mutex is released — publishing never holds it.
func (rt *HostRuntime) publishHostClients() {
	if rt.hub == nil {
		return
	}
	rt.mu.Lock()
	payload, err := json.Marshal(hostState{Clients: rt.countsLocked(), Draining: rt.draining})
	rt.mu.Unlock()
	if err != nil {
		// Marshalling a plain struct cannot fail; skip the event rather than
		// publish a broken payload.
		slog.Warn("host topic payload did not marshal", "err", err.Error())
		return
	}
	_, _ = rt.hub.Publish(TopicHost, Event{Type: EventHostClients, Payload: payload})
}

// publishHostDraining publishes the draining transition on the host topic.
func (rt *HostRuntime) publishHostDraining(reason string) {
	if rt.hub == nil {
		return
	}
	payload, err := json.Marshal(hostDraining{Reason: reason})
	if err != nil {
		slog.Warn("host topic payload did not marshal", "err", err.Error())
		return
	}
	_, _ = rt.hub.Publish(TopicHost, Event{Type: EventHostDraining, Payload: payload})
}

// hostState is the host topic's snapshot and clients-event payload shape.
type hostState struct {
	Clients  ClientCounts `json:"clients"`
	Draining bool         `json:"draining"`
}

// hostDraining is the draining-event payload shape.
type hostDraining struct {
	Reason string `json:"reason"`
}
