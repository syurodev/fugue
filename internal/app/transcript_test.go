package app

// F9.4b evidence: the transcript composition — reconnect (TR-E03), the
// complete-before-append ordering, the vault lock gate, purge unregistration
// and the redaction cost bound. TR-E05, TR-E07 and TR-E08 live in
// transcript_size_test.go with their heavy data.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/secrets"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	testSession   = "s"
	testIncarn    = "transcript-incarnation"
	testItem      = "i-1"
	testSecretHit = "i-secret"
	knownAPIToken = "sk-live-abcdef1234567890"
	testMasterPW  = "master-pw"
	testTurn      = "t1"
)

// transcriptFixture is an unlocked vault, a migrated store, an event hub and
// the transcript service with one created and registered session.
type transcriptFixture struct {
	t       *testing.T
	db      *sql.DB
	store   *vendorstore.Store
	vault   *vault.Service
	hub     *EventHub
	clock   *clock.Fake
	service *Transcripts
}

func newTranscriptFixture(t *testing.T, known KnownValues) *transcriptFixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "transcript.db")
	db, dbErr := storage.Open(t.Context(), path)
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	if err := storage.Migrate(t.Context(), db, clk); err != nil {
		t.Fatal(err)
	}
	fixture, readErr := os.ReadFile(filepath.Join("..", "vault", "testdata", "keyring-cheap.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var ring crypto.Keyring
	if err := json.Unmarshal(fixture, &ring); err != nil {
		t.Fatal(err)
	}
	v, vaultErr := vault.NewService(t.Context(), vault.NewMemoryKeyringStore(&ring), ids.NewSequence("v-"))
	if vaultErr != nil {
		t.Fatal(vaultErr)
	}
	if err := v.Unlock(t.Context(), redact.NewSecret(testMasterPW)); err != nil {
		t.Fatal(err)
	}
	s, storeErr := vendorstore.New(t.Context(), db, v, clk, nil)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	hub := NewEventHub(testIncarn)
	service, serviceErr := NewTranscripts(t.Context(), TranscriptDeps{
		Store: s, Hub: hub, Vault: v, Clock: clk, KnownValues: known,
	})
	if serviceErr != nil {
		t.Fatal(serviceErr)
	}
	t.Cleanup(func() {
		if cerr := service.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	f := &transcriptFixture{t: t, db: db, store: s, vault: v, hub: hub, clock: clk, service: service}
	f.mustCreateSession(testSession)
	return f
}

// mustCreateSession creates the session and opens turn testTurn; the session
// start and the turn start are sequences 1 and 2.
func (f *transcriptFixture) mustCreateSession(session string) {
	f.t.Helper()
	if err := f.store.CreateSession(f.t.Context(), &vendorstore.Session{
		ID: session, Vendor: "v", AccountID: "a", Role: "r", State: vendorstore.StateStarting,
	}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.store.Append(f.t.Context(), session, 1, &vendorstore.Event{
		Type: vendorstore.VendorTurnStarted, TurnID: testTurn, State: vendorstore.StateRunning,
	}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.service.RegisterSession(session); err != nil {
		f.t.Fatal(err)
	}
}

// awaitKnownValues waits for the redactor's asynchronous detector rebuild, so
// a known value is masked as soon as the test proceeds.
func (f *transcriptFixture) awaitKnownValues() {
	f.t.Helper()
	for range 100000 {
		if f.service.redactor.Redact(knownAPIToken) != knownAPIToken {
			return
		}
		runtime.Gosched()
	}
	f.t.Fatal("the redactor never rebuilt its detector with the known values")
}

// streamAndAwait streams one update and blocks until the coalescer has
// published it. The coalescer runs on its own goroutine, so returning from
// StreamItem says nothing about when the delta lands in the delta topic's
// memory; the published event is the signal. The watching subscription is
// opened first so the event cannot be missed.
func (f *transcriptFixture) streamAndAwait(item string, base uint64, kind transcript.ViewKind, content string) {
	f.t.Helper()
	watch := subscribe(f.t, f.hub, TopicTranscriptDelta+testSession)
	f.service.StreamItem(testSession, item, base, kind, content)
	if ev := nextEvent(f.t, watch); ev.Type != EventTranscriptDelta || ev.Key != item {
		f.t.Fatalf("delta event = %+v, want a delta for %q", ev, item)
	}
}

// pumpDelta flushes pending coalesced deltas on the fake clock. It cannot see
// a delta still on its way to the coalescer's pending set, so a test that
// expects a delta to be published uses streamAndAwait instead.
func (f *transcriptFixture) pumpDelta() {
	for range 100000 {
		if f.clock.Pending() == 0 {
			runtime.Gosched()
			if f.clock.Pending() == 0 {
				return
			}
		}
		f.clock.Advance(time.Second/30 + 1)
		runtime.Gosched()
	}
}

func subscribe(t *testing.T, hub *EventHub, topic string) *Subscription {
	t.Helper()
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return sub
}

func nextEvent(t *testing.T, sub *Subscription) Event {
	t.Helper()
	ev, ok := sub.Next(t.Context())
	if !ok {
		t.Fatal("the subscription ended before the expected event")
	}
	return ev
}

func decodeItemState(t *testing.T, payload []byte) itemState {
	t.Helper()
	var state itemState
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatalf("decode item event: %v", err)
	}
	return state
}

func decodeSnapshot(t *testing.T, sub *Subscription) (topicSnapshot, EventSeq) {
	t.Helper()
	state, seq, ok := sub.Snapshot()
	if !ok {
		t.Fatal("the subscription has no snapshot")
	}
	var snap topicSnapshot
	if err := json.Unmarshal(state, &snap); err != nil {
		t.Fatalf("decode topic snapshot: %v", err)
	}
	return snap, seq
}

func decodeDeltaSnapshot(t *testing.T, sub *Subscription) deltaSnapshotState {
	t.Helper()
	state, _, ok := sub.Snapshot()
	if !ok {
		t.Fatal("the subscription has no snapshot")
	}
	var snap deltaSnapshotState
	if err := json.Unmarshal(state, &snap); err != nil {
		t.Fatalf("decode delta snapshot: %v", err)
	}
	return snap
}

// pageCursor is the Before cursor for the page after this one: its oldest
// sequence.
func pageCursor(t *testing.T, page []TranscriptEntry) int64 {
	t.Helper()
	seq := page[len(page)-1].Sequence
	if seq > math.MaxInt64 {
		t.Fatalf("sequence %d is out of the store's range", seq)
		return 0
	}
	return int64(seq)
}

// pendingToolCall is an open tool call whose output carries content.
func pendingToolCall(id, output string) transcript.Item {
	return transcript.Item{
		ID: id, SessionID: testSession, TurnID: testTurn, Kind: transcript.ToolCall, Revision: 0,
		Metadata: transcript.ToolCallMetadata{ToolName: "shell", Status: "pending"},
		Content:  transcript.ToolCallContent{Title: "run", Output: output},
	}
}

// completeToolCall moves a pending tool call to succeeded with new content.
func completeToolCall(pending *transcript.Item, output string) *transcript.Item {
	done := *pending
	done.Revision = 1
	done.Metadata = transcript.ToolCallMetadata{ToolName: "shell", Status: "succeeded"}
	done.Content = transcript.ToolCallContent{Title: "run", Output: output}
	return &done
}

func TestTranscriptReconnectAfterLostDeltaGetsOpenItemsAndFinalDurableState(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)

	// The client streams deltas for an open tool call and loses them: the item
	// was recorded pending, more deltas streamed, then it completed.
	pending := pendingToolCall(testItem, "partial output")
	if err := f.service.RecordItem(t.Context(), testSession, 2, &pending, nil); err != nil {
		t.Fatal(err)
	}
	f.streamAndAwait(testItem, 1, transcript.ViewToolOutput, "partial output, more")

	// The client reconnects: one fresh durable subscription receives the
	// snapshot and then the completion event (TR-E03).
	durable := subscribe(t, f.hub, TopicTranscript+testSession)
	assertReconnectSnapshots(t, f, durable)

	// The item completes: the durable event carries the final state, and the
	// event's view equals the view the store's content renders.
	done := completeToolCall(&pending, "full output after completion")
	if err := f.service.UpdateItem(t.Context(), testSession, 3, &pending, done, nil); err != nil {
		t.Fatal(err)
	}
	assertFinalDurableState(t, f, durable)
}

// assertReconnectSnapshots checks the fresh subscriptions a reconnecting
// client makes: the durable snapshot includes the open item, the delta
// snapshot its latest published view.
func assertReconnectSnapshots(t *testing.T, f *transcriptFixture, durable *Subscription) {
	t.Helper()
	snap, seq := decodeSnapshot(t, durable)
	if seq != 3 || len(snap.Items) != 1 || snap.Items[0].ItemID != testItem {
		t.Fatalf("durable snapshot = %+v at seq %d, want one open item at seq 3", snap, seq)
	}
	if snap.Items[0].View.Text == "" {
		t.Fatal("the open item's snapshot view is empty")
	}
	delta := subscribe(t, f.hub, TopicTranscriptDelta+testSession)
	deltaSnap := decodeDeltaSnapshot(t, delta)
	if len(deltaSnap.Items) != 1 || deltaSnap.Items[0].ItemID != testItem {
		t.Fatalf("delta snapshot = %+v, want the open item's latest view", deltaSnap)
	}
}

// assertFinalDurableState checks the completion event against the stored
// content's view.
func assertFinalDurableState(t *testing.T, f *transcriptFixture, durable *Subscription) {
	t.Helper()
	event := nextEvent(t, durable)
	if event.Type != EventTranscriptItem || event.Seq != 4 {
		t.Fatalf("durable event = %+v, want the item event at seq 4", event)
	}
	state := decodeItemState(t, event.Payload)
	detail, err := f.service.TranscriptItem(t.Context(), testSession, testItem)
	if err != nil {
		t.Fatal(err)
	}
	if state.View == nil || state.View.Text != detail.View.Text || state.Revision != 2 {
		t.Fatalf("final durable view %v does not equal the stored view %q", state.View, detail.View.Text)
	}
}

func TestCompleteBeforeAppendSoNoStaleDeltaFollowsTheDurableEvent(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)

	durable := subscribe(t, f.hub, TopicTranscript+testSession)

	// A delta is pending when the item is recorded: its timer is armed but not
	// fired, so a complete-after-append implementation would publish it after
	// the durable event.
	f.service.StreamItem(testSession, testItem, 0, transcript.ViewText, "streaming text")
	item := pendingToolCall(testItem, "")
	if err := f.service.RecordItem(t.Context(), testSession, 2, &item, nil); err != nil {
		t.Fatal(err)
	}
	event := nextEvent(t, durable)
	if event.Seq != 3 {
		t.Fatalf("durable event seq = %d, want 3", event.Seq)
	}

	// Flush everything pending: no delta may follow the durable event, and the
	// delta snapshot has no state for the completed item.
	f.pumpDelta()
	assertDeltaMemoryEmpty(t, f, "after completion")
	// A strictly stale delta is dropped, not published.
	f.service.StreamItem(testSession, testItem, 0, transcript.ViewText, "stale")
	f.pumpDelta()
	assertDeltaMemoryEmpty(t, f, "after a stale delta")
}

// assertDeltaMemoryEmpty checks the delta topic's memory holds no state for
// the test item.
func assertDeltaMemoryEmpty(t *testing.T, f *transcriptFixture, when string) {
	t.Helper()
	snap, err := f.service.deltaSnapshot(testSession)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snap), fmt.Sprintf("%q", testItem)) {
		t.Fatalf("delta snapshot %s = %s, want no state for the item", when, snap)
	}
}

func TestLockedVaultReturnsMetadataOnlyAndStopsDeltas(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	item := pendingToolCall(testItem, "secret output")
	if err := f.service.RecordItem(t.Context(), testSession, 2, &item, []byte("raw vendor event")); err != nil {
		t.Fatal(err)
	}
	f.vault.Lock()

	assertLockedReads(t, f)
	assertLockedTopics(t, f)
}

// assertLockedReads checks the metadata-only reads with the locked flag.
func assertLockedReads(t *testing.T, f *transcriptFixture) {
	t.Helper()
	page, err := f.service.TranscriptPage(t.Context(), TranscriptPageParams{Session: testSession})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || !page[0].ContentLocked || page[0].View.Text != "" || page[0].Kind != transcript.ToolCall {
		t.Fatalf("locked page = %+v, want metadata only with content_locked", page)
	}
	detail, err := f.service.TranscriptItem(t.Context(), testSession, testItem)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.ContentLocked || detail.Content != nil {
		t.Fatalf("locked item = %+v, want metadata only", detail)
	}
}

// assertLockedTopics checks the locked snapshots, the stopped deltas and the
// metadata-only durable event.
func assertLockedTopics(t *testing.T, f *transcriptFixture) {
	t.Helper()
	durable := subscribe(t, f.hub, TopicTranscript+testSession)
	delta := subscribe(t, f.hub, TopicTranscriptDelta+testSession)

	snap, seq := decodeSnapshot(t, durable)
	if !snap.ContentLocked || seq != 3 || len(snap.Items) != 1 || snap.Items[0].View != nil {
		t.Fatalf("locked snapshot = %+v at seq %d, want metadata-only items at seq 3", snap, seq)
	}
	f.service.StreamItem(testSession, testItem, 1, transcript.ViewText, "after lock")
	f.pumpDelta()
	deltaSnap := decodeDeltaSnapshot(t, delta)
	if !deltaSnap.ContentLocked || len(deltaSnap.Items) != 0 {
		t.Fatalf("locked delta snapshot = %+v, want empty and locked", deltaSnap)
	}
	assertLockedDurableEvent(t, f, durable)
}

// assertLockedDurableEvent records a contentless item while locked and checks
// its durable event carries metadata only (transcript §6).
func assertLockedDurableEvent(t *testing.T, f *transcriptFixture, durable *Subscription) {
	t.Helper()
	usage := transcript.Item{
		ID: "i-2", SessionID: testSession, TurnID: testTurn, Kind: transcript.Usage,
		Metadata: transcript.UsageMetadata{InputTokens: 12, OutputTokens: 34},
		Content:  transcript.UsageContent{},
	}
	if err := f.service.RecordItem(t.Context(), testSession, 3, &usage, nil); err != nil {
		t.Fatal(err)
	}
	if state := decodeItemState(t, nextEvent(t, durable).Payload); state.View != nil || state.ItemID != "i-2" {
		t.Fatalf("locked durable event view = %+v, want metadata only", state.View)
	}
}

func TestPurgedSessionTopicsAreUnregisteredAndDenied(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	durable := subscribe(t, f.hub, TopicTranscript+testSession)
	delta := subscribe(t, f.hub, TopicTranscriptDelta+testSession)

	if err := f.service.PurgeSession(t.Context(), testSession); err != nil {
		t.Fatal(err)
	}
	assertPurgedSubscriber(t, durable)
	assertPurgedSubscriber(t, delta)
	for _, topic := range []string{TopicTranscript + testSession, TopicTranscriptDelta + testSession} {
		if _, err := f.hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: topic}); !errors.Is(err, ErrSubscriptionDenied) {
			t.Fatalf("subscribe %s after purge = %v, want denied", topic, err)
		}
	}
	if f.hub.Unregister(TopicTranscript + testSession) {
		t.Fatal("unregister reported a topic that was already removed")
	}
}

// assertPurgedSubscriber checks one subscriber ended with resync_required and
// received nothing.
func assertPurgedSubscriber(t *testing.T, sub *Subscription) {
	t.Helper()
	if _, ok := sub.Next(t.Context()); ok {
		t.Fatalf("%s: a purged session's subscriber still receives events", sub.topic)
	}
	if !sub.ResyncRequired() {
		t.Fatalf("%s: the subscriber did not end with resync_required", sub.topic)
	}
}

// EnvKnownValues maps a process-environment snapshot to env known values,
// exactly what the composition will pass once a session service exists. The
// detector itself keeps only the J-04 denylisted entries of at least eight
// characters (content-safety §4).
func EnvKnownValues(env []string) []secrets.KnownValue {
	values := make([]secrets.KnownValue, 0, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		values = append(values, secrets.KnownValue{Value: redact.NewSecret(value), Kind: secrets.KindEnv, Key: key})
	}
	return values
}

// githubToken is a GitHub token the pattern detector recognizes: exactly 36
// body characters.
func githubToken() string {
	return "ghp_" + strings.Repeat("a1B2c3D4e5", 3) + "F6g7H8"
}

// secretText builds size bytes of filler interleaved with one token pattern
// and the known credential.
func secretText(size int, token string) string {
	var b strings.Builder
	for b.Len() < size {
		b.WriteString("word ")
		b.WriteString(token)
		b.WriteString(" tail ")
		b.WriteString(knownAPIToken)
		b.WriteString(" end\n")
	}
	return b.String()[:size]
}

// TestRedactionCostIsLinear measures wall-clock time, so it measures before
// joining the parallel phase: the other parallel tests are paused at
// t.Parallel until this body returns to it, so the heavy payload tests of this
// package cannot share the CPU. One contended sample can otherwise inflate
// either size by an order of magnitude; run alone the ratio is about 16x to
// 19x.
func TestRedactionCostIsLinear(t *testing.T) {
	// The fixture wires the credential directly and exercises the J-04
	// environment mapping the composition will use.
	f := newTranscriptFixture(t, func(context.Context) ([]secrets.KnownValue, error) {
		known := make([]secrets.KnownValue, 0, 4)
		known = append(known, secrets.KnownValue{
			Value: redact.NewSecret(knownAPIToken), Kind: secrets.KindCredential,
		})
		known = append(known, EnvKnownValues([]string{
			"PATH=/usr/bin:/bin", "HOME=/home/fugue", "GITHUB_TOKEN=" + githubToken(),
		})...)
		return known, nil
	})
	f.awaitKnownValues()
	token := githubToken()
	// bench returns the fastest of several measurements: contention only ever
	// adds time, so the minimum is the closest sample to the true cost.
	bench := func(size int) time.Duration {
		text := secretText(size, token)
		best := time.Duration(math.MaxInt64)
		for range 3 {
			ns := time.Duration(testing.Benchmark(func(b *testing.B) {
				b.Helper()
				for b.Loop() {
					f.service.redactor.Redact(text)
				}
			}).NsPerOp())
			best = min(best, ns)
		}
		return best
	}
	small := bench(64 << 10)
	large := bench(1 << 20)
	t.Parallel()
	// 16x the input at a linear cost is about 16x the time (about 40x under
	// the race detector and low I/O priority); a quadratic detector would cost
	// at least 256x. The bound sits between the two with room for jitter.
	if large > 64*small {
		t.Fatalf("1 MiB redaction %v exceeds 64x the 64 KiB cost %v", large, small)
	}
	if masked := f.service.redactor.Redact(secretText(1<<20, token)); strings.Contains(masked, knownAPIToken) || strings.Contains(masked, token) {
		t.Fatal("1 MiB masking failed")
	}
}
