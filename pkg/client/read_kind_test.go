package client_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/syurodev/fugue/pkg/client"
	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"
)

// TestUnknownItemKindKeepsItsWireValue pins that proto3's open enums survive
// the client's mapping: a kind the proto does not name is carried as its
// numeric wire value, on the RPC read and on the topic decoders alike, and is
// never collapsed to unspecified.
func TestUnknownItemKindKeepsItsWireValue(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	const wireKind = transcriptv1.TranscriptKind(99)
	socket := serveReadHost(t, &readHandler{itemKind: wireKind})
	c := dialApp(t, socket)
	items, err := c.TranscriptPage(ctx, "s1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("page has %d items, want 1", len(items))
	}
	if got := items[0].Kind; got != "99" {
		t.Fatalf("unknown kind = %q, want the decimal wire value 99", got)
	}

	// The topic decoders carry the same raw value.
	payload, err := proto.Marshal(&transcriptv1.TranscriptItemEvent{ItemId: "i2", Kind: wireKind})
	if err != nil {
		t.Fatal(err)
	}
	item, err := client.DecodeTranscriptItem(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := item.Kind; got != "99" {
		t.Fatalf("topic item kind = %q, want the decimal wire value 99", got)
	}
}
