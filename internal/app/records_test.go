package app

import (
	"bytes"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func recordsGen() ids.Generator {
	clk := clock.NewFake(time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	return ids.NewUUIDv7(clk, ids.NewDeterministic("records"))
}

func TestHostRecordsAreCanonicalAndDistinct(t *testing.T) {
	t.Parallel()
	gen := recordsGen()

	queueItem, err := newQueueItem(gen, "session-1", "run-1")
	if err != nil {
		t.Fatalf("newQueueItem: %v", err)
	}
	queueItemAgain, err := newQueueItem(gen, "session-1", "run-1")
	if err != nil {
		t.Fatalf("newQueueItem again: %v", err)
	}
	if queueItem.Kind != "queue_item" || queueItemAgain.Kind != "queue_item" {
		t.Fatalf("queue item kind %q/%q", queueItem.Kind, queueItemAgain.Kind)
	}
	if !bytes.Equal(queueItem.Plaintext, queueItemAgain.Plaintext) {
		t.Fatalf("equal inputs produced different bytes")
	}
	if queueItem.ID == queueItemAgain.ID {
		t.Fatalf("two records share one payload ID")
	}

	decision, err := newQueueDecision(gen, queueItem.ID)
	if err != nil {
		t.Fatalf("newQueueDecision: %v", err)
	}
	if decision.Kind != "queue_decision" {
		t.Fatalf("queue decision kind %q", decision.Kind)
	}
	if !bytes.Contains(decision.Plaintext, []byte(queueItem.ID)) {
		t.Fatalf("queue decision does not reference its queue item")
	}
	otherDecision, err := newQueueDecision(gen, "other-item")
	if err != nil {
		t.Fatalf("newQueueDecision other: %v", err)
	}
	if bytes.Equal(decision.Plaintext, otherDecision.Plaintext) {
		t.Fatalf("different queue items produced equal decisions")
	}

	auth, err := newModelAuthorization(gen, "faux-model")
	if err != nil {
		t.Fatalf("newModelAuthorization: %v", err)
	}
	if auth.Kind != "model_authorization" {
		t.Fatalf("model authorization kind %q", auth.Kind)
	}
	authAgain, err := newModelAuthorization(gen, "faux-model")
	if err != nil {
		t.Fatalf("newModelAuthorization again: %v", err)
	}
	if !bytes.Equal(auth.Plaintext, authAgain.Plaintext) {
		t.Fatalf("equal model authorizations produced different bytes")
	}
	otherAuth, err := newModelAuthorization(gen, "other-model")
	if err != nil {
		t.Fatalf("newModelAuthorization other: %v", err)
	}
	if bytes.Equal(auth.Plaintext, otherAuth.Plaintext) {
		t.Fatalf("different models produced equal authorizations")
	}

	tool, err := newToolAuthorization(gen, "pure-read-v1")
	if err != nil {
		t.Fatalf("newToolAuthorization: %v", err)
	}
	if tool.Kind != "tool_authorization" {
		t.Fatalf("tool authorization kind %q", tool.Kind)
	}
	toolAgain, err := newToolAuthorization(gen, "pure-read-v1")
	if err != nil {
		t.Fatalf("newToolAuthorization again: %v", err)
	}
	if !bytes.Equal(tool.Plaintext, toolAgain.Plaintext) {
		t.Fatalf("equal tool authorizations produced different bytes")
	}
}

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()
	gen := recordsGen()

	items := []contextItem{
		{Role: "user", Ref: "input-1", Content: []byte("the input")},
		{Role: "assistant", Ref: "response-1", Content: []byte("the response")},
		{Role: "tool", Ref: "result-1", Content: []byte("the result")},
	}
	payload, err := newContext(gen, items)
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
	if len(decoded) != len(items) {
		t.Fatalf("decoded %d items, want %d", len(decoded), len(items))
	}
	for i, want := range items {
		got := decoded[i]
		if got.Role != want.Role || got.Ref != want.Ref || !bytes.Equal(got.Content, want.Content) {
			t.Fatalf("item %d decoded as %+v, want %+v", i, got, want)
		}
	}
}

func TestDecodeContextRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	_, err := decodeContext([]byte(`[{"role":"user","ref":"r","content":"aGk=","extra":1}]`))
	if err == nil {
		t.Fatalf("an unknown field must be rejected")
	}
	if _, err := decodeContext([]byte(`{"role":"user"}`)); err == nil {
		t.Fatalf("an object must not decode as a context list")
	}
}
