package app

// The per-session transcript topics: transcript:<session> (durable, snapshot =
// store sequence plus session and turn state plus the open items' redacted
// views, no history) and transcript-delta:<session> (ephemeral, memory-only
// snapshot, latest-for-key events keyed by item ID). Every payload is
// host-redacted (TR-10) and serialized as JSON for hostapi to map to protobuf.
// Owner: application layer. Allowed callers: hostapi.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// TranscriptView is the JSON shape of a redacted display view.
type TranscriptView struct {
	Text         string                   `json:"text"`
	Truncated    bool                     `json:"truncated,omitempty"`
	CutAt        int                      `json:"cut_at,omitempty"`
	OmittedBytes int                      `json:"omitted_bytes,omitempty"`
	Files        []transcript.FileSummary `json:"files,omitempty"`
}

// TranscriptTopicItemEvent is the durable item payload. A nil View means the
// content is not included — the vault is locked or the event was rebuilt from
// the journal — and the client reads the content through TranscriptItem.
type TranscriptTopicItemEvent struct {
	ItemID     string                   `json:"item_id"`
	TurnID     string                   `json:"turn_id"`
	Kind       string                   `json:"kind"`
	Sequence   int64                    `json:"sequence,omitempty"`
	Revision   uint64                   `json:"revision"`
	Metadata   vendorstore.ItemMetadata `json:"metadata"`
	View       *TranscriptView          `json:"view,omitempty"`
	TurnOrigin string                   `json:"turn_origin"`
}

// TranscriptTopicDelta is one delta event: the latest complete display state
// for its item, never an increment (host-api §6).
type TranscriptTopicDelta struct {
	ItemID       string         `json:"item_id"`
	BaseRevision uint64         `json:"base_revision"`
	View         TranscriptView `json:"view"`
}

// TranscriptTopicStateEvent carries durable turn and session state events.
type TranscriptTopicStateEvent struct {
	TurnID        string `json:"turn_id,omitempty"`
	State         string `json:"state,omitempty"`
	WaitingReason string `json:"waiting_reason,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	FailureCode   string `json:"failure_code,omitempty"`
	StopProof     string `json:"stop_proof,omitempty"`
	TurnOrigin    string `json:"turn_origin,omitempty"`
}

// TranscriptTopicSnapshot is the durable topic's snapshot shape (transcript §6).
type TranscriptTopicSnapshot struct {
	Sequence      uint64                     `json:"sequence"`
	SessionState  string                     `json:"session_state"`
	Turns         []vendorstore.TurnState    `json:"turns,omitempty"`
	ContentLocked bool                       `json:"content_locked"`
	Items         []TranscriptTopicItemEvent `json:"items,omitempty"`
}

// TranscriptDeltaSnapshot carries the latest published view per open item,
// read from memory only.
type TranscriptDeltaSnapshot struct {
	ContentLocked bool                   `json:"content_locked"`
	Items         []TranscriptTopicDelta `json:"items,omitempty"`
}

// transcriptDurable serves the durable topic of one session.
type transcriptDurable struct {
	t       *Transcripts
	session string
}

// Snapshot answers atomically from one store read transaction: the sequence it
// returns is the session row's, and every open item's view is rendered from
// the payload references that transaction captured, so a concurrent append
// cannot split the answer. While the vault is locked the snapshot carries
// metadata only (transcript §6).
func (d *transcriptDurable) Snapshot(ctx context.Context) ([]byte, EventSeq, error) {
	session, turns, items, err := d.t.deps.Store.Overview(ctx, d.session)
	if err != nil {
		return nil, 0, fmt.Errorf("read the vendor session overview: %w", err)
	}
	sequence, seqErr := storeSeq(session.LastSequence)
	if seqErr != nil {
		return nil, 0, seqErr
	}
	locked := !d.t.unlocked()
	snap := TranscriptTopicSnapshot{
		Sequence:      sequence,
		SessionState:  session.State,
		Turns:         turns,
		ContentLocked: locked,
	}
	for i := range items {
		state := itemStateFromStored(&items[i])
		if !locked {
			view, viewErr := d.t.storedView(ctx, d.session, &items[i])
			if viewErr != nil {
				return nil, 0, viewErr
			}
			state.View = &view
		}
		snap.Items = append(snap.Items, state)
	}
	payload, err := json.Marshal(snap)
	if err != nil {
		return nil, 0, errcode.Wrap(errcode.Internal, "marshal the transcript topic snapshot", err)
	}
	return payload, sequence, nil
}

// Since maps the store journal onto durable events. Journal payloads are
// sealed, so catch-up events carry metadata only; a client reads content
// through the reads (transcript §6).
func (d *transcriptDurable) Since(ctx context.Context, seq EventSeq) ([]Event, error) {
	var out []Event
	cursor, cursorErr := storeCursor(seq)
	if cursorErr != nil {
		return nil, cursorErr
	}
	for {
		events, err := d.t.deps.Store.EventsSince(ctx, d.session, cursor, eventPageLimit)
		if err != nil {
			return nil, fmt.Errorf("read the vendor events: %w", err)
		}
		for i := range events {
			event, eventErr := durableEvent(&events[i])
			if eventErr != nil {
				return nil, eventErr
			}
			out = append(out, event)
		}
		if len(events) < eventPageLimit {
			return out, nil
		}
		cursor = events[len(events)-1].Sequence
	}
}

// eventPageLimit is the store's maximum page for one journal read.
const eventPageLimit = 1000

// durableEvent rebuilds one durable event from a journal record.
func durableEvent(recorded *vendorstore.RecordedEvent) (Event, error) {
	if recorded.Metadata.ItemID != "" {
		revision, revErr := storeRevision(recorded.Metadata.ItemRevision)
		if revErr != nil {
			return Event{}, revErr
		}
		state := TranscriptTopicItemEvent{
			ItemID:     recorded.Metadata.ItemID,
			TurnID:     recorded.Metadata.ItemTurnID,
			Kind:       recorded.Metadata.ItemKind,
			Revision:   revision,
			Sequence:   recorded.Sequence,
			TurnOrigin: recorded.TurnOrigin,
		}
		if recorded.Metadata.ItemMetadata != nil {
			state.Metadata = *recorded.Metadata.ItemMetadata
		}
		sequence, seqErr := storeSeq(recorded.Sequence)
		if seqErr != nil {
			return Event{}, seqErr
		}
		return marshalEvent(EventTranscriptItem, sequence, state)
	}
	payload := TranscriptTopicStateEvent{
		TurnID:        recorded.Metadata.TurnID,
		State:         recorded.Metadata.State,
		WaitingReason: recorded.Metadata.WaitingReason,
		Outcome:       recorded.Metadata.Outcome,
		FailureCode:   recorded.Metadata.FailureCode,
		StopProof:     recorded.Metadata.StopProof,
		TurnOrigin:    recorded.TurnOrigin,
	}
	sequence, seqErr := storeSeq(recorded.Sequence)
	if seqErr != nil {
		return Event{}, seqErr
	}
	return marshalEvent(EventTranscriptState, sequence, payload)
}

// PublishCommittedEvent fans out one actor commit after the store transaction
// finishes; item commits use the transcript writer's richer view path.
func (t *Transcripts) PublishCommittedEvent(ctx context.Context, session string, sequence int64) error {
	if sequence < 1 {
		return errcode.New(errcode.InvalidArgument, "transcript event sequence must be positive")
	}
	records, err := t.deps.Store.EventsSince(ctx, session, sequence-1, 1)
	if err != nil {
		return fmt.Errorf("read the committed transcript event: %w", err)
	}
	if len(records) != 1 || records[0].Sequence != sequence {
		return errcode.New(errcode.Unavailable, "the committed transcript event is not available")
	}
	event, err := durableEvent(&records[0])
	if err != nil {
		return err
	}
	if _, err := t.deps.Hub.Publish(TopicTranscript+session, event); err != nil {
		return fmt.Errorf("publish the committed transcript event: %w", err)
	}
	return nil
}

// marshalEvent encodes one durable event payload.
func marshalEvent(eventType string, seq EventSeq, payload any) (Event, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal the %s payload: %w", eventType, err)
	}
	return Event{Seq: seq, Type: eventType, Payload: data}, nil
}

// itemStateFromStored projects a stored item onto the event payload shape.
func itemStateFromStored(stored *vendorstore.StoredItem) TranscriptTopicItemEvent {
	revision, err := storeRevision(stored.Revision)
	if err != nil {
		// The schema CHECK keeps revisions positive; a corrupt row projects
		// without a usable revision rather than failing the whole snapshot.
		revision = 0
	}
	return TranscriptTopicItemEvent{
		ItemID:     stored.ID,
		TurnID:     stored.TurnID,
		Kind:       stored.Kind,
		Sequence:   stored.Sequence,
		Revision:   revision,
		Metadata:   stored.Metadata,
		TurnOrigin: stored.TurnOrigin,
	}
}

// storedView renders one stored item's redacted display view from its
// committed content payload.
func (t *Transcripts) storedView(ctx context.Context, session string, stored *vendorstore.StoredItem) (TranscriptView, error) {
	if stored.PayloadID == "" {
		return TranscriptView{}, nil
	}
	data, err := t.deps.Store.OpenPayload(ctx, session, stored.PayloadID)
	if err != nil {
		return TranscriptView{}, fmt.Errorf("open the transcript content: %w", err)
	}
	content, err := decodeContent(transcript.Kind(stored.Kind), data)
	if err != nil {
		return TranscriptView{}, err
	}
	fields, _ := contentStrings(content)
	display := largest(fields)
	if display == nil {
		return TranscriptView{}, nil
	}
	view := transcript.BuildView(viewKindFor(transcript.Kind(stored.Kind)), *display, t.redactor)
	return viewFromTranscript(view), nil
}

func viewFromTranscript(view transcript.View) TranscriptView {
	return TranscriptView{Text: view.Text, Truncated: view.Truncated, CutAt: view.CutAt, OmittedBytes: view.OmittedBytes, Files: view.Files}
}

// deltaSink returns the sink one session's coalescer publishes to: it records
// the latest view for the delta snapshot and publishes the latest-for-key
// event. Deltas never flow while the vault is locked (transcript §5).
func (t *Transcripts) deltaSink(session string) transcript.DeltaSink {
	return deltaSinkFunc(func(_ context.Context, d *transcript.Delta) {
		if !t.unlocked() {
			return
		}
		payload, err := json.Marshal(TranscriptTopicDelta{ItemID: d.ItemID, BaseRevision: d.BaseRevision, View: viewFromTranscript(d.View)})
		if err != nil {
			slog.Warn("a transcript delta payload did not marshal", "err", err.Error())
			return
		}
		t.mu.Lock()
		st := t.sessions[session]
		t.mu.Unlock()
		if st == nil {
			return
		}
		st.mu.Lock()
		st.views[d.ItemID] = d.View
		st.revisions[d.ItemID] = d.BaseRevision
		st.mu.Unlock()
		if _, err := t.deps.Hub.Publish(TopicTranscriptDelta+session, Event{
			Type: EventTranscriptDelta, Key: d.ItemID, Payload: payload, LatestForKey: true,
		}); err != nil {
			slog.Warn("the transcript delta publish failed", "err", err.Error())
		}
	})
}

type deltaSinkFunc func(ctx context.Context, d *transcript.Delta)

func (f deltaSinkFunc) Publish(ctx context.Context, d *transcript.Delta) { f(ctx, d) }

// deltaSnapshot snapshots the delta topic from memory: the latest view
// published per open item (transcript §6). The hub calls it without holding
// any lock, so it reads only its own state.
func (t *Transcripts) deltaSnapshot(session string) ([]byte, error) {
	snap := TranscriptDeltaSnapshot{ContentLocked: !t.unlocked()}
	t.mu.Lock()
	st := t.sessions[session]
	t.mu.Unlock()
	if st != nil {
		st.mu.Lock()
		for id, view := range st.views {
			snap.Items = append(snap.Items, TranscriptTopicDelta{ItemID: id, BaseRevision: st.revisions[id], View: viewFromTranscript(view)})
		}
		st.mu.Unlock()
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("marshal the transcript delta snapshot: %w", err)
	}
	return data, nil
}
