package app

import (
	"bytes"
	"testing"

	"github.com/syurodev/fugue/internal/coordinator"
)

// One transition on a session that has never been written seals its input and
// several payloads; they must all be openable, which requires that the same
// new session key protects each of them and is stored once.
func TestAdapterSealsInputAndSeveralPayloadsUnderOneSessionKey(t *testing.T) {
	t.Parallel()
	s := newAdapterStore(t, true)
	tr := adapterAccept()
	tr.MetadataInput = nil
	tr.ProtectedInput = []byte(`{"command":"accept"}`)
	tr.Payloads = []coordinator.Payload{
		{ID: "queue-decision", Kind: "queue_decision", Plaintext: []byte("queue decision plaintext")},
		{ID: kindContext, Kind: kindContext, Plaintext: []byte("context plaintext")},
		{ID: "tool-declarations", Kind: "tool_declarations", Plaintext: []byte("tool declarations plaintext")},
	}
	res, err := NewCoordinatorStore(s).Append(t.Context(), tr)
	if err != nil {
		t.Fatalf("append with input and three payloads on a new session: %v", err)
	}
	want := map[string][]byte{res.Receipt.InputPayloadID: tr.ProtectedInput}
	for _, p := range tr.Payloads {
		want[p.ID] = p.Plaintext
	}
	if len(want) != 4 {
		t.Fatalf("expected four distinct payload IDs, got %d", len(want))
	}
	for id, plaintext := range want {
		opened, openErr := s.OpenPayload(t.Context(), adapterSession, id)
		if openErr != nil || !bytes.Equal(opened, plaintext) {
			t.Fatalf("payload %q must open to its plaintext: %v", id, openErr)
		}
	}
}
