package session

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

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

// Display returns the view to render: the streaming one while the item
// streams, the durable one otherwise. It is named Display so the embedded
// TranscriptItem.View field stays unambiguous to a reader.
func (i *Item) Display() *client.TranscriptView {
	if i.Streaming != nil {
		return i.Streaming
	}
	return &i.View
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

// ItemByID returns one item the transcript holds by ID, for callers that
// act on an item's own payload (the Edited row's full diff, F10.4e).
func (t *Transcript) ItemByID(id string) (*Item, bool) {
	it, ok := t.byID[id]
	return it, ok
}

// KindUserMessage is the transcript kind of a message a user sent.
const KindUserMessage = "user_message"

// LastUserMessage returns the newest user_message item the model holds. The
// reply items of the last turn sit after it, so the backward scan stops
// within one turn in practice; the reopen and new-turn anchors (F10.4d)
// read it while arming, not every frame.
func (t *Transcript) LastUserMessage() (*Item, bool) {
	for i := len(t.items) - 1; i >= 0; i-- {
		if t.items[i].Kind == KindUserMessage {
			return t.items[i], true
		}
	}
	return nil, false
}

// LastSequence is the newest durable topic sequence the model applied.
func (t *Transcript) LastSequence() uint64 { return t.lastSeq }

// LastItemSequence is the newest item sequence the model holds, zero when it
// holds none: the marker a new-turn anchor arms against (F10.4d).
func (t *Transcript) LastItemSequence() uint64 {
	if len(t.items) == 0 {
		return 0
	}
	return t.items[len(t.items)-1].Sequence
}

// NeedsNewestPage reports whether the history must be read: after the model
// was created or cleared by a snapshot.
func (t *Transcript) NeedsNewestPage() bool { return !t.pageLoaded && !t.exhausted }

// OlderCursor returns the creation sequence to page older from, and whether
// there is older history to read at all.
func (t *Transcript) OlderCursor() (int64, bool) {
	if !t.pageLoaded || t.exhausted || t.oldestSeq <= 1 || t.oldestSeq > uint64(math.MaxInt64) {
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
		return fmt.Errorf("apply the transcript snapshot: %w", err)
	}
	t.items = nil
	t.byID = map[string]*Item{}
	t.pageLoaded = false
	t.exhausted = false
	t.oldestSeq = 0
	t.lastSeq = snap.Sequence
	t.sessionState = snap.SessionState
	t.contentLocked = snap.ContentLocked
	grew := false
	for i := range snap.Items {
		grew = t.upsert(&Item{TranscriptItem: snap.Items[i]}) || grew
	}
	if grew {
		t.sortBySequence()
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
			return fmt.Errorf("apply the transcript item event: %w", err)
		}
		if t.upsert(&Item{TranscriptItem: item}) {
			t.sortBySequence()
		}
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
		return fmt.Errorf("apply the transcript delta: %w", err)
	}
	item, ok := t.byID[delta.ItemID]
	if !ok || delta.BaseRevision < item.Revision {
		return nil
	}
	view := delta.View
	if view.Title == "" {
		view.Title = item.View.Title
	}
	if view.Input == "" {
		view.Input = item.View.Input
	}
	if len(view.Steps) == 0 {
		view.Steps = item.View.Steps
	}
	item.Streaming = &view
	return nil
}

// ApplyItemDetail folds a TranscriptItem RPC answer into the held item so
// truncated tool outputs or diffs expand to their full redacted text.
func (t *Transcript) ApplyItemDetail(detail *client.TranscriptDetail) {
	item, ok := t.byID[detail.Item.ItemID]
	if !ok || detail.Item.Revision < item.Revision {
		return
	}
	view := detail.Item.View
	applyContentDetail(detail.Item.Kind, detail.Content, &view)
	next := Item{TranscriptItem: detail.Item}
	next.View = view
	*item = next
}

func applyContentDetail(kind string, raw []byte, view *client.TranscriptView) {
	if len(raw) == 0 {
		return
	}
	var payload struct {
		Text    string   `json:"Text"`
		Title   string   `json:"Title"`
		Input   string   `json:"Input"`
		Output  string   `json:"Output"`
		Path    string   `json:"Path"`
		OldPath string   `json:"OldPath"`
		Diff    string   `json:"Diff"`
		Steps   []string `json:"Steps"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return
	}
	expand := func(s string) string { return strings.ReplaceAll(s, "\t", "    ") }
	switch kind {
	case "tool_call":
		setExpanded(&view.Text, &view.Truncated, payload.Output, expand)
		setIfNonEmpty(&view.Title, payload.Title, expand)
		setIfNonEmpty(&view.Input, payload.Input, expand)
	case "file_change":
		setExpanded(&view.Text, &view.Truncated, payload.Diff, expand)
		setIfNonEmpty(&view.Title, payload.Path, expand)
		setIfNonEmpty(&view.Input, payload.OldPath, expand)
	case "plan":
		if len(payload.Steps) > 0 {
			view.Steps = make([]string, len(payload.Steps))
			for i, s := range payload.Steps {
				view.Steps[i] = expand(s)
			}
			view.Text = strings.Join(view.Steps, "\n")
			view.Truncated = false
		}
	default:
		setExpanded(&view.Text, &view.Truncated, payload.Text, expand)
	}
}

func setExpanded(dst *string, trunc *bool, val string, expand func(string) string) {
	if val != "" {
		*dst = expand(val)
		*trunc = false
	}
}

func setIfNonEmpty(dst *string, val string, expand func(string) string) {
	if val != "" {
		*dst = expand(val)
	}
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

// ApplyOlderPage applies one page of older history and returns how many new
// items it prepended, so the caller can keep the reader's anchor in place. An
// empty page completes the history.
func (t *Transcript) ApplyOlderPage(items []client.TranscriptItem) int {
	before := len(t.items)
	t.applyPage(items)
	if len(items) == 0 {
		t.exhausted = true
	}
	return len(t.items) - before
}

// applyPage merges the page's items into the model, appending the new ones
// and sorting once — a page is one insert, not one per item.
func (t *Transcript) applyPage(items []client.TranscriptItem) {
	grew := false
	for i := range items {
		grew = t.upsert(&Item{TranscriptItem: items[i]}) || grew
	}
	if grew {
		t.sortBySequence()
	}
}

// upsert applies one item by item_id and revision: a first sighting is
// appended (the caller sorts), a newer revision replaces the held state and
// ends any streaming view, and a revision that is not newer is ignored. It
// reports whether the item is new.
func (t *Transcript) upsert(item *Item) bool {
	held, ok := t.byID[item.ItemID]
	if ok {
		if item.Revision <= held.Revision {
			return false
		}
		item.Streaming = nil
		*held = *item
		return false
	}
	t.byID[item.ItemID] = item
	t.items = append(t.items, item)
	return true
}

// sortBySequence restores the creation order and the oldest cursor after a
// batch of inserts.
func (t *Transcript) sortBySequence() {
	slices.SortStableFunc(t.items, func(a, b *Item) int { return cmp.Compare(a.Sequence, b.Sequence) })
	t.oldestSeq = t.items[0].Sequence
}
