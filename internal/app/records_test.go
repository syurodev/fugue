package app

import (
	"bytes"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func recordsGen() ids.Generator {
	clk := clock.NewFake(time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	return ids.NewUUIDv7(clk, ids.NewDeterministic("records"))
}

type recordFunc func(ids.Generator) (coordinator.Payload, error)

func TestHostRecordsAreCanonicalAndDistinct(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		kind  string
		first recordFunc
		again recordFunc
		other recordFunc
	}{
		{
			"queue item", "queue_item",
			func(g ids.Generator) (coordinator.Payload, error) { return newQueueItem(g, "session-1", "run-1") },
			func(g ids.Generator) (coordinator.Payload, error) { return newQueueItem(g, "session-1", "run-1") },
			func(g ids.Generator) (coordinator.Payload, error) { return newQueueItem(g, "session-1", "run-2") },
		},
		{
			"queue decision", "queue_decision",
			func(g ids.Generator) (coordinator.Payload, error) { return newQueueDecision(g, "item-1") },
			func(g ids.Generator) (coordinator.Payload, error) { return newQueueDecision(g, "item-1") },
			func(g ids.Generator) (coordinator.Payload, error) { return newQueueDecision(g, "item-2") },
		},
		{
			"model authorization", "model_authorization",
			func(g ids.Generator) (coordinator.Payload, error) { return newModelAuthorization(g, "faux-model") },
			func(g ids.Generator) (coordinator.Payload, error) { return newModelAuthorization(g, "faux-model") },
			func(g ids.Generator) (coordinator.Payload, error) { return newModelAuthorization(g, "other-model") },
		},
		{
			"tool authorization", "tool_authorization",
			func(g ids.Generator) (coordinator.Payload, error) { return newToolAuthorization(g, "pure-read-v1") },
			func(g ids.Generator) (coordinator.Payload, error) { return newToolAuthorization(g, "pure-read-v1") },
			func(g ids.Generator) (coordinator.Payload, error) { return newToolAuthorization(g, "pure-read-v2") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireCanonicalRecord(t, tc.kind, tc.first, tc.again, tc.other)
		})
	}
}

// requireCanonicalRecord pins the record contract: the kind is exact, equal
// inputs marshal to equal bytes (the receipt dedup relies on it) and every
// record carries its own payload ID.
func requireCanonicalRecord(t *testing.T, kind string, first, again, other recordFunc) {
	t.Helper()
	gen := recordsGen()

	payload, err := first(gen)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if payload.Kind != kind {
		t.Fatalf("kind %q, want %q", payload.Kind, kind)
	}
	repeat, err := again(gen)
	if err != nil {
		t.Fatalf("seal again: %v", err)
	}
	if !bytes.Equal(payload.Plaintext, repeat.Plaintext) {
		t.Fatalf("equal inputs produced different bytes")
	}
	if payload.ID == repeat.ID {
		t.Fatalf("two records share one payload ID")
	}
	different, err := other(gen)
	if err != nil {
		t.Fatalf("seal other: %v", err)
	}
	if bytes.Equal(payload.Plaintext, different.Plaintext) {
		t.Fatalf("different inputs produced equal bytes")
	}
}

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()

	items := []contextItem{
		{Role: contextRoleUser, Ref: execInputRef, Content: []byte("the input")},
		{Role: "assistant", Ref: "response-1", Content: []byte("the response")},
		{Role: "tool", Ref: "result-1", Content: []byte("the result")},
	}
	payload, err := newContext(recordsGen(), items)
	if err != nil {
		t.Fatalf("newContext: %v", err)
	}
	if payload.Kind != "context" {
		t.Fatalf("context kind %q", payload.Kind)
	}
	decoded, err := decodeContext(payload.Plaintext)
	if err != nil {
		t.Fatalf("decodeContext: %v", err)
	}
	requireContextItemsEqual(t, decoded, items)
}

func requireContextItemsEqual(t *testing.T, decoded, want []contextItem) {
	t.Helper()
	if len(decoded) != len(want) {
		t.Fatalf("decoded %d items, want %d", len(decoded), len(want))
	}
	for i := range want {
		got := decoded[i]
		if got.Role != want[i].Role || got.Ref != want[i].Ref || !bytes.Equal(got.Content, want[i].Content) {
			t.Fatalf("item %d decoded as %+v, want %+v", i, got, want[i])
		}
	}
}

func TestDecodeContextRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	if _, err := decodeContext([]byte(`[{"role":"user","ref":"r","content":"aGk=","extra":1}]`)); err == nil {
		t.Fatalf("an unknown field must be rejected")
	}
	if _, err := decodeContext([]byte(`{"role":"user"}`)); err == nil {
		t.Fatalf("an object must not decode as a context list")
	}
}
