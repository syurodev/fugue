package app

// The recovery and message building of the team mailbox (F11.3b). Recovery
// resolves every `delivering` batch a crash left behind against the session
// transcript (Q-122): found means delivered, absent means resend with the
// same batch ID, an unreadable transcript means hold — never guess. The
// message payload types and their rendering belong with the recovery, so the
// sealed shapes and their turn text stay in one place.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// mailboxDigestItem is the sealed payload of one digest row. Summary stays
// empty until the task tools report one (F11.3c); the state is Fugue's own.
type mailboxDigestItem struct {
	TaskID      string `json:"task_id"`
	State       string `json:"state"`
	FailureCode string `json:"failure_code,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Question    bool   `json:"question,omitempty"`
}

// mailboxDecision is the sealed payload of the OR-E13 delivery to the
// proposing session: the decision, the change list Fugue wrote, and the
// approved plan whose task text is model output.
type mailboxDecision struct {
	Decision  string               `json:"decision"`
	DecidedBy string               `json:"decided_by,omitempty"`
	Note      string               `json:"note,omitempty"`
	Changes   []string             `json:"changes,omitempty"`
	Plan      *teams.ProposalInput `json:"plan,omitempty"`
}

func (w *teamWake) logWake(ctx context.Context, err error) {
	if err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "delivering the team mailbox failed", "error_code", string(errcode.CodeOf(err)))
	}
}

// RecoverBatches resolves every `delivering` batch a crash left behind
// (Q-122). A locked vault or an unreadable transcript holds the batch; a
// found marker means delivered; an absent marker queues the resend with the
// same batch ID.
func (w *teamWake) RecoverBatches(ctx context.Context) error {
	if w.deps.Vault.Snapshot().State != vault.StateUnlocked {
		return nil
	}
	batches, err := w.deps.Store.DeliveringMailboxBatches(ctx)
	if err != nil {
		return boundaryError("list the delivering mailbox batches", err)
	}
	for _, b := range batches {
		if err := ctx.Err(); err != nil {
			return errcode.Wrap(errcode.Canceled, "the recovery pass was interrupted", err)
		}
		if err := w.resolveBatch(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

// resolveBatch resolves one delivering batch: a found marker means delivered,
// an absent marker queues the resend with the same batch ID (Q-122), and an
// unreadable transcript holds the batch for the next trigger.
func (w *teamWake) resolveBatch(ctx context.Context, b vendorstore.MailboxBatchRef) error {
	sessionID, err := w.recipientSession(ctx, b.TeamID, b.RecipientTaskID)
	if err != nil || sessionID == "" {
		return err
	}
	found, readable, err := w.transcriptHasMarker(ctx, sessionID, mailboxBatchPrefix(b.BatchID))
	if err != nil {
		return boundaryError("scan the session transcript", err)
	}
	if !readable {
		return nil
	}
	if found {
		if err := w.deps.Store.MarkMailboxBatchDelivered(ctx, b.BatchID); err != nil {
			return boundaryError("mark the mailbox batch delivered", err)
		}
		return nil
	}
	if err := w.deps.Store.ReleaseMailboxBatch(ctx, b.BatchID); err != nil {
		return boundaryError("release the mailbox batch", err)
	}
	return nil
}

// refusedSend reports a send the session refused before anything reached the
// vendor: busy, closed, not resumable, gone, out of capacity or behind a
// locked vault. The turn was not recorded, so the claim returns to pending
// (Q-122). Any other failure — a timeout after the write, a context cancel,
// a storage failure — leaves the outcome unknown and the claim `delivering`.
func refusedSend(err error) bool {
	return errors.Is(err, vendor.ErrBusy) ||
		errors.Is(err, vendor.ErrSessionClosed) ||
		errors.Is(err, vendorstore.ErrSessionNotFound) ||
		errcode.CodeOf(err) == errcode.VendorSessionState ||
		errcode.CodeOf(err) == errcode.VendorCapacity ||
		errcode.CodeOf(err) == errcode.VendorVaultLocked
}

// batchText opens the sealed payloads of a claim and renders one turn: the
// batch marker first (Q-122), then the §6.1 digest format (Q-124).
func (w *teamWake) batchText(ctx context.Context, claimed []vendorstore.MailboxMessage) (string, error) {
	var (
		header   string
		entries  []teams.DigestEntry
		decision mailboxDecision
	)
	for i := range claimed {
		raw, err := w.deps.Store.OpenMailboxPayload(ctx, claimed[i].TeamID, claimed[i].MessageID)
		if err != nil {
			return "", boundaryError("open the sealed mailbox payload", err)
		}
		switch claimed[i].Kind {
		case vendorstore.MailboxKindDecision:
			if err := json.Unmarshal(raw, &decision); err != nil {
				return "", errcode.New(errcode.FailedPrecondition, "the decision message is not readable")
			}
		default:
			var item mailboxDigestItem
			if err := json.Unmarshal(raw, &item); err != nil {
				return "", errcode.New(errcode.FailedPrecondition, "the digest item is not readable")
			}
			state := item.State
			if item.FailureCode != "" {
				state = item.State + "(" + item.FailureCode + ")"
			}
			entries = append(entries, teams.DigestEntry{TaskID: item.TaskID, State: state, Summary: item.Summary, Question: item.Question})
		}
	}
	header, entries = decisionTurnText(&decision, entries)
	nonce, err := w.deps.IDs.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate the digest nonce", err)
	}
	text := teams.BuildDigest(header, entries, nonce, teams.MaxDigestBytes)
	return mailboxBatchPrefix(claimed[0].BatchID) + "\n\n" + text, nil
}

// decisionTurnText folds a decision message into the digest input: the
// decision and the user's note are Fugue text (the user typed the note), the
// change list is Fugue's own, and the approved plan's task text is model
// output rendered as data blocks (OR-E13).
func decisionTurnText(decision *mailboxDecision, entries []teams.DigestEntry) (string, []teams.DigestEntry) {
	if decision.Decision == "" {
		return headerOf(entries), entries
	}
	var lines []string
	lines = append(lines, "The user decided \""+decision.Decision+"\" on your team proposal.")
	if decision.DecidedBy != "" {
		lines = append(lines, "Decided by: "+decision.DecidedBy+".")
	}
	if decision.Note != "" {
		lines = append(lines, "The user's note:", decision.Note)
	}
	lines = append(lines, decision.Changes...)
	if decision.Plan != nil {
		for i := range decision.Plan.Tasks {
			t := &decision.Plan.Tasks[i]
			entries = append(entries, teams.DigestEntry{TaskID: t.ID, State: "queued", Summary: t.Goal})
		}
	}
	return strings.Join(lines, "\n"), entries
}

// headerOf builds the digest header line when the claim has no decision.
func headerOf(entries []teams.DigestEntry) string {
	if len(entries) == 0 {
		return ""
	}
	return fmt.Sprintf("Team update: %d task item(s).", len(entries))
}

// transcriptHasMarker pages the session transcript for the batch marker in a
// Fugue-origin user message. readable is false while the vault is locked or
// the scan ran out of budget: the caller holds instead of guessing (Q-122).
func (w *teamWake) transcriptHasMarker(ctx context.Context, sessionID, marker string) (found, readable bool, err error) {
	scanned := 0
	for before := int64(0); ; {
		page, err := w.deps.Store.Transcript(ctx, sessionID, before, mailboxScanPage)
		if err != nil {
			return false, false, boundaryError("page the session transcript", err)
		}
		if len(page) == 0 {
			return false, true, nil
		}
		result, scanErr := w.scanForMarker(ctx, sessionID, page, marker, &scanned)
		if scanErr != nil {
			return false, false, scanErr
		}
		if result == scanFound {
			return true, true, nil
		}
		if result == scanHold {
			return false, false, nil
		}
		before = page[0].Sequence
	}
}

// scanOutcome is what one transcript page told the marker scan.
type scanOutcome int

const (
	scanContinue scanOutcome = iota
	scanFound
	scanHold
)

// scanForMarker scans one page's Fugue-origin user messages for the marker
// and reports whether the scan continues, found it, or must hold (Q-122).
func (w *teamWake) scanForMarker(ctx context.Context, sessionID string, page []vendorstore.StoredItem, marker string, scanned *int) (scanOutcome, error) {
	for i := range page {
		item := &page[i]
		if item.Kind != vendorstore.KindUserMessage || item.TurnOrigin != vendorstore.TurnOriginFugue {
			continue
		}
		*scanned++
		if *scanned > mailboxScanCap {
			return scanHold, nil
		}
		_, content, err := w.deps.Store.ReadItem(ctx, sessionID, item.ID)
		if err != nil {
			if errors.Is(err, vault.ErrLocked) {
				return scanHold, nil
			}
			return scanHold, boundaryError("read the transcript item", err)
		}
		if hasBatchPrefix(content, marker) {
			return scanFound, nil
		}
	}
	return scanContinue, nil
}

func hasBatchPrefix(content []byte, marker string) bool {
	var msg struct {
		Text string `json:"Text"`
	}
	if json.Unmarshal(content, &msg) == nil && msg.Text != "" {
		return strings.HasPrefix(msg.Text, marker)
	}
	return strings.HasPrefix(string(content), marker)
}
