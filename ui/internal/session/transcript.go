package session

import (
	"cmp"
	"slices"

	"github.com/syurodev/fugue/pkg/client"
)

// The transcript topics' event types, with the payload shapes pkg/client
// decodes.
const (
	EventTranscriptItem  = "transcript.item"
	EventTranscriptState = "transcript.state"
	EventTranscriptDelta = "transcript.delta"
)

// PageLimit is the newest page's item count (transcript §6).
const PageLimit = 200

// Item is one transcript entry the model holds: the durable item at its
// newest revision plus, while the item is open, the delta view that shows the
// turn's progress.
type Item struct {
	client.TranscriptItem
	// Streaming is the latest delta view for the item; a durable revision
	// newer than the delta's base clears it again.
	Streaming *client.TranscriptView
}

// View returns the view to render: the streaming one while the item streams,
// the durable one otherwise.
func (i *Item) View() *client.TranscriptView {
	if i.Streaming != nil {
		return i.Streaming
	}
	return &i.TranscriptItem.View
}

// Transcript is one open session's read model: the newest page's history, the
// older pages read on demand, and the live upserts and deltas the topics
// deliver. It changes only on the window goroutine.
type Transcript struct {
	session string
	items   []*Item
	byID    map[string]*Item

	sessionState  string
	contentLocked bool
	// lastSeq is the newest durable topic sequence applied, so a catch-up can
	// be pinned as gapless.
	lastSeq    uint64
	pageLoaded bool
	// oldestSeq is the creation sequence of the oldest held item; zero when
	// the model holds none.
	oldestSeq uint64
	// exhausted means an older page came back empty: history is complete.
	exhausted bool
}

// NewTranscript builds the model of one session.
func NewTranscript(session string) *Transcript {
	return &Transcript{session: session, byID: map[string]*Item{}}
}

// Session names the session the model reads.
func (t *Transcript) Session() string { return t.session }

// SessionState is the session's state as the durable topic last reported it.
func (t *Transcript) SessionState() string { return t.sessionState }

// ContentLocked reports whether the vault is locked and the views carry
// metadata only.
func (t *Transcript) ContentLocked() bool { return t.contentLocked }

// Items returns the held items, oldest first.
func (t *Transcript) Items() []*Item { return t.items }

// LastSequence is the newest durable topic sequence the model applied.
func (t *Transcript) LastSequence() uint64 { return t.lastSeq }

// NeedsNewestPage reports whether the history must be read: after the model
// was created or cleared by a snapshot.
func (t *Transcript) NeedsNewestPage() bool { return !t.pageLoaded && !t.exhausted }

// OlderCursor returns the creation sequence to page older from, and whether
// there is older history to read at all.
func (t *Transcript) OlderCursor() (int64, bool) {
	if !t.pageLoaded || t.exhausted || t.oldestSeq <= 1 {
		return 0, false
	}
	return int64(t.oldestSeq), true
}

// ApplySnapshot applies the durable topic's snapshot: it clears everything
// held (a snapshot means the host cannot catch this client up, or a new
// incarnation answered), records the session and content state, and applies
// the snapshot's open items. The caller re-reads the history afterwards.
func (t *Transcript) ApplySnapshot(payload []byte) error {
	snap, err := client.DecodeTranscriptSnapshot(payload)
	if err != nil {
		return err
	}
	t.items = nil
	t.byID = map[string]*Item{}
	t.pageLoaded = false
	t.exhausted = false
	t.oldestSeq = 0
	t.lastSeq = snap.Sequence
	t.sessionState = snap.SessionState
	t.contentLocked = snap.ContentLocked
	for i := range snap.Items {
		t.upsert(&Item{TranscriptItem: snap.Items[i]})
	}
	return nil
}

// ApplyEvent applies one durable topic event: an item upsert or a state
// event. An upsert whose revision is not newer than the one held is ignored,
// and the sequence is remembered so a catch-up stays gapless.
func (t *Transcript) ApplyEvent(eventType string, sequence uint64, payload []byte) error {
	switch eventType {
	case EventTranscriptItem:
		item, err := client.DecodeTranscriptItem(payload)
		if err != nil {
			return err
		}
		t.upsert(&Item{TranscriptItem: item})
	case EventTranscriptState:
		// Turn and outcome states are rendered by the composer and the
		// status row (F10.4b); the read side records the sequence only.
	default:
		return nil
	}
	t.lastSeq = max(t.lastSeq, sequence)
	return nil
}

// ApplyDelta applies one transcript-delta topic event: the latest complete
// display state of an open item (never an increment). A delta whose base is
// older than the held revision is stale and ignored.
func (t *Transcript) ApplyDelta(payload []byte) error {
	delta, err := client.DecodeTranscriptDelta(payload)
	if err != nil {
		return err
	}
	item, ok := t.byID[delta.ItemID]
	if !ok || delta.BaseRevision < item.Revision {
		return nil
	}
	view := delta.View
	item.Streaming = &view
	return nil
}

// ApplyNewestPage applies the newest history page, oldest item last as the
// client returns it. An item already held from the snapshot is replaced only
// by a newer revision. A short page completes the history.
func (t *Transcript) ApplyNewestPage(items []client.TranscriptItem) {
	t.applyPage(items)
	t.pageLoaded = true
	if len(items) < PageLimit {
		t.exhausted = true
	}
}

// ApplyOlderPage applies one page of older history. An empty page completes
// the history.
func (t *Transcript) ApplyOlderPage(items []client.TranscriptItem) {
	t.applyPage(items)
	if len(items) == 0 {
		t.exhausted = true
	}
}

// applyPage merges the page's items into the model.
func (t *Transcript) applyPage(items []client.TranscriptItem) {
	for i := range items {
		t.upsert(&Item{TranscriptItem: items[i]})
	}
}

// upsert applies one item by item_id and revision: a first sighting is
// inserted at its sequence, a newer revision replaces the held state and ends
// any streaming view, and a revision that is not newer is ignored.
func (t *Transcript) upsert(item *Item) {
	held, ok := t.byID[item.ItemID]
	if ok {
		if item.Revision <= held.Revision {
			return
		}
		item.Streaming = nil
		*held = *item
		return
	}
	t.byID[item.ItemID] = item
	t.items = append(t.items, item)
	slices.SortStableFunc(t.items, func(a, b *Item) int { return cmp.Compare(a.Sequence, b.Sequence) })
	t.oldestSeq = t.items[0].Sequence
}
