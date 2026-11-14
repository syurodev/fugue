package app

// The transcript reads over vendorstore: a paged newest-first transcript and a
// single item with its full stored content, both redacted at the host (TR-10).
// While the vault is locked or its state is unknown the reads return metadata
// only with content_locked set (transcript §6). Owner: application layer.
// Allowed callers: hostapi (F9.9) and application tests.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// defaultPageLimit is the transcript page default (transcript §6).
const defaultPageLimit = 200

// TranscriptMetadata is the public-safe metadata mapped by hostapi.
type TranscriptMetadata = vendorstore.ItemMetadata

// TranscriptKind is the public transcript item kind mapped by hostapi.
type TranscriptKind = transcript.Kind

// TranscriptPageParams are the page request's domain fields. Before zero means
// the newest page; Limit zero uses the default.
type TranscriptPageParams struct {
	Session string
	Before  int64
	Limit   int
}

// TranscriptEntry is one page entry: the item's metadata and its redacted
// display view at its latest revision. Updates never move an item's position.
type TranscriptEntry struct {
	ID            string                   `json:"item_id"`
	TurnID        string                   `json:"turn_id"`
	Kind          transcript.Kind          `json:"kind"`
	Sequence      uint64                   `json:"sequence"`
	Revision      uint64                   `json:"revision"`
	CreatedAt     string                   `json:"created_at"`
	UpdatedAt     string                   `json:"updated_at"`
	Metadata      vendorstore.ItemMetadata `json:"metadata"`
	View          TranscriptView           `json:"view"`
	ContentLocked bool                     `json:"content_locked"`
	TurnOrigin    string                   `json:"turn_origin"`
}

// TranscriptDetail is TranscriptItem's answer: the entry plus the full stored
// content, redacted, when the vault is open.
type TranscriptDetail struct {
	TranscriptEntry
	Content json.RawMessage `json:"content,omitempty"`
}

// TranscriptPage returns one page of items, newest first, each as its redacted
// display view at its latest revision.
func (t *Transcripts) TranscriptPage(ctx context.Context, p TranscriptPageParams) ([]TranscriptEntry, error) {
	if p.Before < 0 || p.Limit < 0 {
		return nil, errcode.New(errcode.InvalidArgument, "transcript page cursor and limit must not be negative")
	}
	limit := p.Limit
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > 500 {
		limit = 500
	}
	if _, err := t.deps.Store.Snapshot(ctx, p.Session); err != nil {
		return nil, transcriptSessionError(err)
	}
	items, err := t.deps.Store.Transcript(ctx, p.Session, p.Before, limit)
	if err != nil {
		return nil, fmt.Errorf("read the transcript page: %w", err)
	}
	// The store returns the page oldest first; the read model is newest first.
	slices.Reverse(items)
	locked := !t.unlocked()
	entries := make([]TranscriptEntry, 0, len(items))
	for i := range items {
		entry, entryErr := t.entryWithView(ctx, p.Session, &items[i], locked)
		if entryErr != nil {
			return nil, entryErr
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// TranscriptPageFor applies the Host API principal check before reading.
func (t *Transcripts) TranscriptPageFor(ctx context.Context, principal string, p TranscriptPageParams) ([]TranscriptEntry, error) {
	if !IsLocalPrincipal(principal) {
		return nil, errcode.New(errcode.PermissionDenied, "only local principals may read transcripts")
	}
	return t.TranscriptPage(ctx, p)
}

func (t *Transcripts) entryWithView(ctx context.Context, session string, stored *vendorstore.StoredItem, locked bool) (TranscriptEntry, error) {
	entry, err := entryFromStored(stored, locked)
	if err != nil {
		return TranscriptEntry{}, err
	}
	if !locked {
		view, viewErr := t.storedView(ctx, session, stored)
		if viewErr != nil {
			return TranscriptEntry{}, viewErr
		}
		entry.View = view
	}
	return entry, nil
}

// TranscriptItem returns one item at its latest revision with its full stored
// content, redacted, and its display view.
func (t *Transcripts) TranscriptItem(ctx context.Context, session, id string) (TranscriptDetail, error) {
	if _, err := t.deps.Store.Snapshot(ctx, session); err != nil {
		return TranscriptDetail{}, transcriptSessionError(err)
	}
	locked := !t.unlocked()
	stored, err := t.deps.Store.StoredItemMeta(ctx, session, id)
	if err != nil {
		if errors.Is(err, vendorstore.ErrItemNotFound) {
			return TranscriptDetail{}, errcode.New(errcode.TranscriptItemNotFound, "the transcript item does not exist in this session")
		}
		return TranscriptDetail{}, fmt.Errorf("read the transcript item: %w", err)
	}
	entry, entryErr := entryFromStored(&stored, locked)
	if entryErr != nil {
		return TranscriptDetail{}, entryErr
	}
	detail := TranscriptDetail{TranscriptEntry: entry}
	if locked || stored.PayloadID == "" {
		return detail, nil
	}
	data, err := t.deps.Store.OpenPayload(ctx, session, stored.PayloadID)
	if err != nil {
		return TranscriptDetail{}, fmt.Errorf("open the transcript content: %w", err)
	}
	content, err := decodeContent(transcript.Kind(stored.Kind), data)
	if err != nil {
		return TranscriptDetail{}, err
	}
	// The view is built first: it redacts the full field itself, and the
	// detail content is masked separately per field (TR-10).
	fields, _ := contentStrings(content)
	if display := largest(fields); display != nil {
		view := transcript.BuildView(viewKindFor(transcript.Kind(stored.Kind)), *display, t.redactor)
		detail.View = viewFromTranscript(view)
	}
	encoded, err := json.Marshal(redactContent(content, t.redactor))
	if err != nil {
		return TranscriptDetail{}, errcode.Wrap(errcode.Internal, "encode the transcript item", err)
	}
	detail.Content = encoded
	return detail, nil
}

// TranscriptItemFor applies the Host API principal check before reading.
func (t *Transcripts) TranscriptItemFor(ctx context.Context, principal, session, id string) (TranscriptDetail, error) {
	if !IsLocalPrincipal(principal) {
		return TranscriptDetail{}, errcode.New(errcode.PermissionDenied, "only local principals may read transcripts")
	}
	return t.TranscriptItem(ctx, session, id)
}

func entryFromStored(stored *vendorstore.StoredItem, locked bool) (TranscriptEntry, error) {
	sequence, err := storeRevision(stored.Sequence)
	if err != nil {
		return TranscriptEntry{}, err
	}
	revision, err := storeRevision(stored.Revision)
	if err != nil {
		return TranscriptEntry{}, err
	}
	return TranscriptEntry{
		ID:            stored.ID,
		TurnID:        stored.TurnID,
		Kind:          transcript.Kind(stored.Kind),
		Sequence:      sequence,
		Revision:      revision,
		CreatedAt:     stored.CreatedAt,
		UpdatedAt:     stored.UpdatedAt,
		Metadata:      stored.Metadata,
		ContentLocked: locked,
		TurnOrigin:    stored.TurnOrigin,
	}, nil
}

func transcriptSessionError(err error) error {
	if errors.Is(err, vendorstore.ErrSessionNotFound) {
		return errcode.New(errcode.TranscriptSessionNotFound, "the transcript session does not exist")
	}
	return fmt.Errorf("read the transcript session: %w", err)
}
