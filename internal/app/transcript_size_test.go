package app

// F9.4b evidence with heavy data: stable pagination over 10,000 items
// (TR-E05), head-and-tail storage fit with flags (TR-E07) and end-to-end
// secret masking with the stored payload left original (TR-E08).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/secrets"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	oversizedMarker = "oversized"
	// totalPageItems is the TR-E05 item count; session start and turn start
	// precede the items in the sequence.
	totalPageItems    = 10000
	firstItemSequence = uint64(totalPageItems) + 2
)

func TestTranscriptPageOver10000ItemsIsStableWithoutDuplicatesOrGaps(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)

	updated := recordPageItems(t, f, totalPageItems)

	// An update must not move the item's position: revision advances, sequence
	// stays.
	next := *updated
	next.Revision = 1
	if err := f.service.UpdateItem(t.Context(), testSession, totalPageItems+2, updated, &next, nil); err != nil {
		t.Fatal(err)
	}

	entries := collectAllPages(t, f)
	assertPagedTranscript(t, entries, totalPageItems)
}

// recordPageItems records total notice items through the write path and
// returns the first item for the update check.
func recordPageItems(t *testing.T, f *transcriptFixture, total int) *transcript.Item {
	t.Helper()
	first := transcript.Item{
		ID: "item-000001", SessionID: testSession, TurnID: testTurn, Kind: transcript.Notice,
		Metadata: transcript.NoticeMetadata{Level: "info", Code: "page"},
		Content:  transcript.NoticeContent{Text: "page item"},
	}
	if err := f.service.RecordItem(t.Context(), testSession, 2, &first, nil); err != nil {
		t.Fatal(err)
	}
	for expected := 3; expected <= total+1; expected++ {
		item := transcript.Item{
			ID:        fmt.Sprintf("item-%06d", expected-1),
			SessionID: testSession, TurnID: testTurn, Kind: transcript.Notice,
			Metadata: transcript.NoticeMetadata{Level: "info", Code: "page"},
			Content:  transcript.NoticeContent{Text: "page item"},
		}
		if err := f.service.RecordItem(t.Context(), testSession, int64(expected), &item, nil); err != nil {
			t.Fatal(err)
		}
	}
	return &first
}

// collectAllPages pages the transcript newest first with the default limit.
func collectAllPages(t *testing.T, f *transcriptFixture) []TranscriptEntry {
	t.Helper()
	var entries []TranscriptEntry
	before := int64(0)
	pages := 0
	for {
		page, err := f.service.TranscriptPage(t.Context(), TranscriptPageParams{Session: testSession, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			return entries
		}
		if pages == 0 && len(page) != defaultPageLimit {
			t.Fatalf("first page = %d entries, want the default %d", len(page), defaultPageLimit)
		}
		cursor := pageCursor(t, page)
		if pages > 0 && cursor >= before {
			t.Fatalf("page repeats sequence %d and newer", before)
		}
		entries = append(entries, page...)
		before = cursor
		pages++
	}
}

// assertPagedTranscript checks the paged output: every sequence exactly once,
// newest first with no gaps, and the updated item at its original position.
func assertPagedTranscript(t *testing.T, entries []TranscriptEntry, total int) {
	t.Helper()
	seen := make(map[uint64]string, total)
	for i := range entries {
		entry := &entries[i]
		if id, dup := seen[entry.Sequence]; dup {
			t.Fatalf("sequence %d appeared twice (items %s and %s)", entry.Sequence, entry.ID, id)
		}
		seen[entry.Sequence] = entry.ID
	}
	if len(entries) != total || len(seen) != total {
		t.Fatalf("paged %d entries with %d distinct sequences, want %d", len(entries), len(seen), total)
	}
	if entries[0].Sequence != firstItemSequence {
		t.Fatalf("newest entry sequence = %d, want %d", entries[0].Sequence, firstItemSequence)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Sequence != entries[i-1].Sequence-1 {
			t.Fatalf("gap between sequences %d and %d", entries[i-1].Sequence, entries[i].Sequence)
		}
	}
	if entries[total-1].Revision != 2 || entries[total-1].ID != "item-000001" {
		t.Fatalf("updated item = %+v, want revision 2 at its original position", entries[total-1])
	}
}

func TestOversizedContentAndRawAreStoredHeadAndTailWithFlags(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	content := generateText(2<<20, oversizedMarker)
	raw := generateText(2<<20, "raw")
	item := transcript.Item{
		ID: testItem, SessionID: testSession, TurnID: testTurn, Kind: transcript.AssistantMessage,
		Metadata: transcript.AssistantMessageMetadata{ModelID: "m"},
		Content:  transcript.AssistantMessageContent{Text: content},
	}
	if err := f.service.RecordItem(t.Context(), testSession, 2, &item, []byte(raw)); err != nil {
		t.Fatal(err)
	}

	stored, err := f.store.StoredItemMeta(t.Context(), testSession, testItem)
	if err != nil {
		t.Fatal(err)
	}
	assertTruncationMetadata(t, &stored, len(content), len(raw))
	fitted, err := f.store.OpenPayload(t.Context(), testSession, stored.PayloadID)
	if err != nil {
		t.Fatal(err)
	}
	assertHeadAndTail(t, fitted, content)
	rawFitted, err := f.store.OpenPayload(t.Context(), testSession, stored.RawPayloadID)
	if err != nil {
		t.Fatal(err)
	}
	assertHeadAndTail(t, rawFitted, raw)
}

// assertTruncationMetadata checks the TR-09 flags and original sizes.
func assertTruncationMetadata(t *testing.T, stored *vendorstore.StoredItem, contentSize, rawSize int) {
	t.Helper()
	if !stored.Metadata.ContentTruncated || stored.Metadata.OriginalContentBytes != int64(contentSize) {
		t.Fatalf("content flags = %+v, want truncated with original size %d", stored.Metadata, contentSize)
	}
	if !stored.Metadata.RawTruncated || stored.Metadata.OriginalRawBytes != int64(rawSize) {
		t.Fatalf("raw flags = %+v, want truncated with original size %d", stored.Metadata, rawSize)
	}
}

// assertHeadAndTail checks the fitted payload keeps the head and tail of the
// original within the payload limit. The generator produces ASCII text without
// JSON-escapable characters, so the head and tail are byte-identical
// substrings of the original inside the JSON encoding.
func assertHeadAndTail(t *testing.T, fitted []byte, original string) {
	t.Helper()
	if len(fitted) > sealing.MaxPayloadPlaintext {
		t.Fatalf("stored payload is %d bytes, want at most %d", len(fitted), sealing.MaxPayloadPlaintext)
	}
	if !bytes.Contains(fitted[:4096], []byte(original[:512])) {
		t.Fatal("the stored payload lost the head of the original")
	}
	if !bytes.Contains(fitted[len(fitted)-4096:], []byte(original[len(original)-512:])) {
		t.Fatal("the stored payload lost the tail of the original")
	}
}

// generateText builds ASCII text without JSON-escapable characters.
func generateText(size int, marker string) string {
	var b strings.Builder
	unit := marker + " the quick brown fox jumps over the lazy dog. "
	for b.Len() < size {
		b.WriteString(unit)
	}
	return b.String()[:size]
}

func TestSecretsAreMaskedInPageItemSnapshotAndDeltaAndStoredPayloadIsOriginal(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, func(context.Context) ([]secrets.KnownValue, error) {
		return []secrets.KnownValue{{Value: redact.NewSecret(knownAPIToken), Kind: secrets.KindCredential}}, nil
	})
	f.awaitKnownValues()
	token := githubToken()
	// The known value sits past the display cut, so redaction must run on the
	// full content before any shortening (TR-10).
	secretOutput := generateText(64<<10, "filler") + " tail " + knownAPIToken + " " + token + " end"
	item := pendingToolCall(testSecretHit, secretOutput)
	if err := f.service.RecordItem(t.Context(), testSession, 2, &item, []byte("raw "+knownAPIToken)); err != nil {
		t.Fatal(err)
	}
	f.service.StreamItem(testSession, testSecretHit, 1, transcript.ViewToolOutput, secretOutput)
	f.pumpDelta()

	assertMaskedReads(t, f, token)
	assertMaskedTopics(t, f, token)
	assertStoredPayloadOriginal(t, f)
}

// assertMaskedReads checks the page and item reads hide both secrets.
func assertMaskedReads(t *testing.T, f *transcriptFixture, token string) {
	t.Helper()
	page, err := f.service.TranscriptPage(t.Context(), TranscriptPageParams{Session: testSession})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != testSecretHit {
		t.Fatalf("page = %+v", page)
	}
	assertMasked(t, page[0].View.Text, knownAPIToken, token)

	detail, err := f.service.TranscriptItem(t.Context(), testSession, testSecretHit)
	if err != nil {
		t.Fatal(err)
	}
	var content transcript.ToolCallContent
	if err := json.Unmarshal(detail.Content, &content); err != nil {
		t.Fatal(err)
	}
	assertMasked(t, content.Output, knownAPIToken, token)
}

// assertMaskedTopics checks the durable snapshot and the delta snapshot hide
// both secrets.
func assertMaskedTopics(t *testing.T, f *transcriptFixture, token string) {
	t.Helper()
	durable := subscribe(t, f.hub, TopicTranscript+testSession)
	snap, _ := decodeSnapshot(t, durable)
	if len(snap.Items) != 1 || snap.Items[0].View == nil {
		t.Fatalf("snapshot = %+v, want the open item's view", snap)
	}
	assertMasked(t, snap.Items[0].View.Text, knownAPIToken, token)

	// The delta subscription is fresh, so its snapshot carries the latest
	// published view per open item.
	delta := subscribe(t, f.hub, TopicTranscriptDelta+testSession)
	deltaSnap := decodeDeltaSnapshot(t, delta)
	if len(deltaSnap.Items) != 1 || deltaSnap.Items[0].ItemID != testSecretHit {
		t.Fatalf("delta snapshot = %+v, want the open item's latest view", deltaSnap)
	}
	assertMasked(t, deltaSnap.Items[0].View.Text, knownAPIToken, token)
}

// assertStoredPayloadOriginal checks the sealed payloads keep the secrets
// verbatim (TR-10): redaction never touches what is stored.
func assertStoredPayloadOriginal(t *testing.T, f *transcriptFixture) {
	t.Helper()
	stored, err := f.store.StoredItemMeta(t.Context(), testSession, testSecretHit)
	if err != nil {
		t.Fatal(err)
	}
	fitted, err := f.store.OpenPayload(t.Context(), testSession, stored.PayloadID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(fitted, []byte(knownAPIToken)) {
		t.Fatal("the stored content payload was altered by redaction")
	}
	raw, err := f.store.OpenPayload(t.Context(), testSession, stored.RawPayloadID)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "raw "+knownAPIToken {
		t.Fatalf("stored raw = %q, want the original", raw)
	}
}

// assertMasked checks text hides every secret and carries a marker.
func assertMasked(t *testing.T, text string, secret ...string) {
	t.Helper()
	for _, s := range secret {
		if strings.Contains(text, s) {
			t.Fatalf("text still contains the secret %q", s)
		}
	}
	if !strings.Contains(text, "[secret:") {
		t.Fatal("text contains no secret marker at all")
	}
}
