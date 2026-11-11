package app

// The transcript write path, later driven by the session actor (F9.5/F9.9):
// stream one item's display state, record and update items with fitted
// content, complete the delta stream before the store append so no stale
// delta follows the durable event, and publish durable events with the
// committed sequence after the commit. Owner: application layer. Allowed
// callers: the session actor (future) and application tests.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// ErrTranscriptStorageLimit reports that the session reached its payload total
// limit; the running turn ends failed(storage) and takes no new turn
// (transcript §7).
var ErrTranscriptStorageLimit = errcode.New(errcode.FailedPrecondition, "the session payload limit is reached")

// StreamItem feeds the session's delta coalescer with one streaming update:
// the coalescer publishes the item's complete latest redacted view at most 30
// times per second (transcript §5). Updates for an unknown session and every
// update while the vault is locked are dropped.
func (t *Transcripts) StreamItem(session, itemID string, baseRevision uint64, kind transcript.ViewKind, content string) {
	if !t.unlocked() {
		return
	}
	st := t.sessionEntry(session)
	if st == nil {
		return
	}
	if co := t.coalescerOf(st); co != nil {
		co.Update(itemID, baseRevision, kind, content)
	}
}

// RecordItem stores a new item with its full content and raw vendor event and
// publishes the durable event after the commit. The item's delta stream is
// completed before the append, so no stale delta can follow the durable event
// (F9.4 row).
func (t *Transcripts) RecordItem(ctx context.Context, session string, expectedSeq int64, item *transcript.Item, raw []byte) error {
	if err := item.Validate(); err != nil {
		return errcode.Wrap(errcode.InvalidArgument, "validate the transcript item", err)
	}
	return t.storeItem(ctx, session, expectedSeq, item, raw, vendorstore.TranscriptItemRecorded, 1)
}

// UpdateItem validates the update against the stored identity — same item,
// same kind, advancing revision — and stores it like a record.
func (t *Transcripts) UpdateItem(ctx context.Context, session string, expectedSeq int64, old, next *transcript.Item, raw []byte) error {
	if err := transcript.ValidateUpdate(old, next); err != nil {
		return errcode.Wrap(errcode.InvalidArgument, "validate the transcript update", err)
	}
	return t.storeItem(ctx, session, expectedSeq, next, raw, vendorstore.TranscriptItemUpdated, next.Revision+1)
}

// storeItem is the shared record and update path.
func (t *Transcripts) storeItem(ctx context.Context, session string, expectedSeq int64, item *transcript.Item, raw []byte, eventType string, newRevision uint64) error {
	content, truncated, originalSize, display, err := fitContent(item.Content, sealing.MaxPayloadPlaintext)
	if err != nil {
		return err
	}
	fittedRaw, rawTruncated, rawOriginal := fitRaw(raw)
	expectedRev, revErr := expectedStoreRevision(eventType, item)
	if revErr != nil {
		return revErr
	}
	metadata := storeMetadata(item.Metadata)
	if strings.HasPrefix(item.TurnID, "vendor-") {
		metadata.Origin = vendorstore.TurnOriginVendor
	}
	withTruncation(&metadata, truncated, originalSize, rawTruncated, rawOriginal)
	stored := vendorstore.Item{
		ID:       item.ID,
		TurnID:   item.TurnID,
		Kind:     string(item.Kind),
		Revision: expectedRev,
		Metadata: metadata,
		Content:  content,
		Raw:      fittedRaw,
	}

	// The coalescer drops the item's pending view and pins the new revision
	// before the append: the durable event that follows carries the final
	// state, so no stale delta can trail it.
	t.completeDelta(session, item.ID, newRevision)

	seq, appendErr := t.deps.Store.Append(ctx, session, expectedSeq, &vendorstore.Event{Type: eventType, Item: &stored})
	if appendErr != nil {
		if isSessionLimit(appendErr) {
			return t.endTurnFailedStorage(ctx, session, expectedSeq, item.TurnID, appendErr)
		}
		return classifyStoreError(appendErr)
	}
	committed, seqErr := storeSeq(seq)
	if seqErr != nil {
		return seqErr
	}

	t.publishItemEvent(session, committed, &itemState{
		ItemID:   item.ID,
		TurnID:   item.TurnID,
		Kind:     string(item.Kind),
		Sequence: seq,
		Revision: newRevision,
		Metadata: stored.Metadata,
		View:     t.itemView(display, string(item.Kind)),
	})

	t.clearDeltaView(session, item.ID)
	return nil
}

// classifyStoreError wraps the store's rejection for the caller. The typed
// conflicts keep their identity; anything else is a store failure the caller
// still recognizes through the wrap.
func classifyStoreError(err error) error {
	return fmt.Errorf("append the transcript item: %w", err)
}

// expectedStoreRevision is the store's contract: a new item appends revision
// zero, an update carries the revision it expects to replace.
func expectedStoreRevision(eventType string, item *transcript.Item) (int64, error) {
	if eventType == vendorstore.TranscriptItemRecorded {
		return 0, nil
	}
	return expectedRevision(item.Revision)
}

// withTruncation records the TR-09 flags and original sizes. A flag is set
// exactly when its original size is positive (the store's consistency rule).
func withTruncation(metadata *vendorstore.ItemMetadata, truncated bool, originalSize int, rawTruncated bool, rawOriginal int) {
	metadata.ContentTruncated = truncated
	metadata.OriginalContentBytes = int64(originalSize)
	metadata.RawTruncated = rawTruncated
	metadata.OriginalRawBytes = int64(rawOriginal)
}

// itemView renders the durable event's redacted view from the fitted display
// field; while the vault is locked the event carries metadata only (TR-10,
// transcript §6).
func (t *Transcripts) itemView(display, kind string) *viewPayload {
	if !t.unlocked() || display == "" {
		return nil
	}
	view := transcript.BuildView(viewKindFor(transcript.Kind(kind)), display, t.redactor)
	payload := viewFromTranscript(view)
	return &payload
}

// publishItemEvent publishes the durable event with the committed sequence,
// after the commit. A failure is a programming error of the topic's owner —
// the journal is committed — so it is logged, not propagated.
func (t *Transcripts) publishItemEvent(session string, seq EventSeq, state *itemState) {
	event, err := marshalEvent(EventTranscriptItem, seq, state)
	if err != nil {
		slog.Warn("a transcript event payload did not marshal", "err", err.Error())
		return
	}
	if _, err := t.deps.Hub.Publish(TopicTranscript+session, event); err != nil {
		slog.Warn("the transcript event publish failed", "err", err.Error())
	}
}

// completeDelta completes the item in the session's coalescer.
func (t *Transcripts) completeDelta(session, itemID string, revision uint64) {
	t.mu.Lock()
	st := t.sessions[session]
	t.mu.Unlock()
	if st == nil {
		return
	}
	if co := t.coalescerOf(st); co != nil {
		co.Complete(itemID, revision)
	}
}

// clearDeltaView drops the item's stored delta view: the durable state
// supersedes it (transcript §5).
func (t *Transcripts) clearDeltaView(session, itemID string) {
	t.mu.Lock()
	st := t.sessions[session]
	t.mu.Unlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	delete(st.views, itemID)
	delete(st.revisions, itemID)
	st.mu.Unlock()
}

// isSessionLimit classifies the store's session payload total rejection. The
// per-payload limit cannot fire after the fit, and the typed conflicts — a
// concurrent writer, a closed or purged session, a locked vault — are the
// caller's to handle, not storage limits.
func isSessionLimit(err error) bool {
	if errors.Is(err, vendorstore.ErrSequenceConflict) ||
		errors.Is(err, vendorstore.ErrSessionClosed) ||
		errors.Is(err, vendorstore.ErrPurged) ||
		errors.Is(err, vault.ErrLocked) {
		return false
	}
	var coded *errcode.Error
	return errors.As(err, &coded) && coded.Code() == errcode.FailedPrecondition
}

// endTurnFailedStorage ends the running turn with failed(storage) (transcript
// §7). The failed append left the session sequence untouched, so the turn-end
// append uses the same expected sequence.
func (t *Transcripts) endTurnFailedStorage(ctx context.Context, session string, expectedSeq int64, turnID string, cause error) error {
	_, appendErr := t.deps.Store.Append(ctx, session, expectedSeq, &vendorstore.Event{
		Type:        vendorstore.VendorTurnEnded,
		TurnID:      turnID,
		Outcome:     vendorstore.OutcomeFailed,
		FailureCode: "storage",
	})
	joined := errors.Join(cause, appendErr)
	if joined == nil {
		return ErrTranscriptStorageLimit
	}
	return errcode.Wrap(errcode.FailedPrecondition, ErrTranscriptStorageLimit.Error(), joined)
}
