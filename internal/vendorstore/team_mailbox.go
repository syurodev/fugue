package vendorstore

// F11.3b team mailbox (Q-121, Q-122). Rows land in the SAME transaction as the
// event that produces them, with the text sealed under the team key, so a
// decision or a task end never records without the message it owes. Delivery
// is at-least-once: a batch is claimed into `delivering`, sent as one
// Fugue-origin turn, and only then marked `delivered`; a crash leaves the
// claim behind for the transcript-checked recovery.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/sealing"
)

// Mailbox message kinds (Q-121): a digest row waits for the 30 s window of its
// lead or manager recipient; answer and send rows reach a member at once; a
// decision row carries the OR-E13 delivery to the proposing session.
const (
	MailboxKindDigest   = "digest"
	MailboxKindAnswer   = "answer"
	MailboxKindSend     = "send"
	MailboxKindDecision = "decision"
)

// Mailbox states of the at-least-once delivery (Q-122).
const (
	MailboxPending    = "pending"
	MailboxDelivering = "delivering"
	MailboxDelivered  = "delivered"
)

// teamMailboxRecordType is the sealing record type of a mailbox payload.
const teamMailboxRecordType = "team.mailbox"

// MailboxEnqueueInput is one message written with its producing event.
type MailboxEnqueueInput struct {
	// RecipientTaskID is the task whose session receives the message; the
	// session itself is read from that task's row at delivery time.
	RecipientTaskID string
	Kind            string
	// Payload is the sealed message content (metadata columns only, INV-09).
	Payload []byte
	// Informational marks a row that never schedules its own delivery: it is
	// skipped while nothing else is pending for the recipient and rides their
	// next digest without a separate wake (§6.7, Q-177 A: the delegator sees
	// sibling sends in its next digest, no separate wake).
	Informational bool
}

// MailboxMessage is the metadata view of one team_mailbox_messages row
// (INV-09); the text is only reachable through OpenMailboxPayload.
type MailboxMessage struct {
	MessageID       string
	TeamID          string
	RecipientTaskID string
	BatchID         string
	Kind            string
	State           string
	SourceSeq       int64
	CreatedAt       int64
}

// MailboxRecipient is one recipient task that has pending mail.
type MailboxRecipient struct {
	TeamID          string
	RecipientTaskID string
	// EarliestPending is the created_at (ms) of the oldest pending row: the
	// digest window starts there (§6.1).
	EarliestPending int64
	// HasImmediate reports a kind that never waits for the digest window.
	HasImmediate bool
}

// MailboxBatchRef names one claim awaiting its delivery outcome.
type MailboxBatchRef struct {
	TeamID          string
	BatchID         string
	RecipientTaskID string
}

// mailboxMessageID derives the idempotency key of a mailbox row: the team, the
// sequence of the producing event and the recipient task (Q-122).
func mailboxMessageID(teamID string, seq int64, recipientTaskID string) string {
	return fmt.Sprintf("%s:%d:%s", teamID, seq, recipientTaskID)
}

// insertMailboxRows seals and inserts the messages of one producing event at
// sequence seq. A duplicate message ID fails the transaction: it can only mean
// the same event produced the same recipient twice, which is a caller bug.
func (s *Store) insertMailboxRows(ctx context.Context, tx *sql.Tx, teamID string, seq int64, msgs []MailboxEnqueueInput, keyID string, rawKey []byte, now int64) error {
	for i := range msgs {
		m := &msgs[i]
		switch m.Kind {
		case MailboxKindDigest, MailboxKindAnswer, MailboxKindSend, MailboxKindDecision:
		default:
			return fmt.Errorf("%w: unknown mailbox kind %q", ErrTeamInvalidInput, m.Kind)
		}
		if m.RecipientTaskID == "" || len(m.Payload) == 0 {
			return fmt.Errorf("%w: a mailbox message requires a recipient and a payload", ErrTeamInvalidInput)
		}
		id := mailboxMessageID(teamID, seq, m.RecipientTaskID)
		envelope, err := sealing.SealPayload(randomReader{s.random}, teamMailboxRecordType, teamID, id, m.Kind, keyID, rawKey, m.Payload)
		if err != nil {
			return fmt.Errorf("seal the mailbox payload: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_mailbox_messages
				(message_id, team_id, recipient_task_id, batch_id, kind, state, payload_envelope, source_seq, created_at, updated_at, informational)
			VALUES (?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?)`,
			id, teamID, m.RecipientTaskID, m.Kind, MailboxPending, envelope, seq, now, now, m.Informational,
		); err != nil {
			return fmt.Errorf("insert the mailbox message: %w", err)
		}
	}
	return nil
}

// EnqueueDirectMailbox seals and enqueues an immediate mailbox message (such
// as fugue_task_send) without appending a team state event.
func (s *Store) EnqueueDirectMailbox(ctx context.Context, teamID, recipientTaskID, kind, uniqueID string, payload []byte) error {
	return s.enqueueDirectMailbox(ctx, teamID, recipientTaskID, kind, uniqueID, payload, false)
}

// EnqueueDirectMailboxIdempotent seals and enqueues a direct mailbox message,
// ignoring duplicate (teamID, uniqueID, recipientTaskID) keys so unlock/restart
// retries never emit duplicate notices.
func (s *Store) EnqueueDirectMailboxIdempotent(ctx context.Context, teamID, recipientTaskID, kind, uniqueID string, payload []byte) error {
	return s.enqueueDirectMailbox(ctx, teamID, recipientTaskID, kind, uniqueID, payload, true)
}

func (s *Store) enqueueDirectMailbox(ctx context.Context, teamID, recipientTaskID, kind, uniqueID string, payload []byte, ignoreDup bool) error {
	if _, err := s.requireUnlockedVault(); err != nil {
		return err
	}
	if recipientTaskID == "" || uniqueID == "" || len(payload) == 0 {
		return fmt.Errorf("%w: direct mailbox requires recipient, unique id, and payload", ErrTeamInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin direct mailbox tx: %w", ErrUnavailable, err)
	}
	defer rollback(ctx, tx)
	var keyID string
	var wrapped []byte
	var lastSeq int64
	if scanErr := tx.QueryRowContext(ctx, `SELECT key_id, wrapped_key, last_seq FROM team_records WHERE team_id = ?`, teamID).Scan(&keyID, &wrapped, &lastSeq); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrTeamNotFound, teamID)
		}
		return fmt.Errorf("%w: query team for mailbox: %w", ErrUnavailable, scanErr)
	}
	rawKey, err := s.unwrapTeamKey(teamID, keyID, wrapped)
	if err != nil {
		return err
	}
	defer clear(rawKey)
	id := teamID + ":" + uniqueID + ":" + recipientTaskID
	envelope, err := sealing.SealPayload(randomReader{s.random}, teamMailboxRecordType, teamID, id, kind, keyID, rawKey, payload)
	if err != nil {
		return fmt.Errorf("seal the direct mailbox payload: %w", err)
	}
	now := s.clock.Now().UnixMilli()
	seq := max(lastSeq, 1)
	if _, err := tx.ExecContext(ctx, directMailboxInsertSQL(ignoreDup), id, teamID, recipientTaskID, kind, MailboxPending, envelope, seq, now, now); err != nil {
		return fmt.Errorf("insert direct mailbox message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit direct mailbox: %w", ErrUnavailable, err)
	}
	return nil
}

func directMailboxInsertSQL(ignoreDup bool) string {
	if ignoreDup {
		return `INSERT OR IGNORE INTO team_mailbox_messages
			(message_id, team_id, recipient_task_id, batch_id, kind, state, payload_envelope, source_seq, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, ?, ?, ?, ?)`
	}
	return `INSERT INTO team_mailbox_messages
			(message_id, team_id, recipient_task_id, batch_id, kind, state, payload_envelope, source_seq, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, ?, ?, ?, ?)`
}

// claimBatchID derives a fresh batch ID for a claim with no resend leftover.
func claimBatchID(teamID, recipientTaskID string, sourceSeq int64) string {
	return mailboxMessageID(teamID, sourceSeq, recipientTaskID) + ":b"
}

// PendingMailbox lists the recipients that hold pending mail, metadata only
// and readable while the vault is locked (INV-09). A recipient whose pending
// rows are all informational is not listed: informational rows never wake
// their recipient (§6.7), so they wait until something else delivers to them.
func (s *Store) PendingMailbox(ctx context.Context) ([]MailboxRecipient, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT team_id, recipient_task_id, MIN(created_at),
			MAX(kind IN ('answer','send','decision'))
		FROM team_mailbox_messages WHERE state = ?
		GROUP BY team_id, recipient_task_id
		HAVING MAX(informational = 0) = 1`, MailboxPending)
	if err != nil {
		return nil, fmt.Errorf("%w: query the pending mailbox: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	var out []MailboxRecipient
	for rows.Next() {
		var r MailboxRecipient
		if err := rows.Scan(&r.TeamID, &r.RecipientTaskID, &r.EarliestPending, &r.HasImmediate); err != nil {
			return nil, fmt.Errorf("%w: scan the pending mailbox: %w", ErrUnavailable, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate the pending mailbox: %w", ErrUnavailable, err)
	}
	return out, nil
}

// ClaimMailbox moves every pending row of one recipient into the delivering
// state under one batch ID and returns them. A row that already carries a
// batch ID is a crash leftover queued for resend (Q-122), so its batch ID
// wins: the resend keeps the same ID.
func (s *Store) ClaimMailbox(ctx context.Context, teamID, recipientTaskID string) ([]MailboxMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: begin the mailbox claim: %w", ErrUnavailable, err)
	}
	defer rollback(ctx, tx)
	rows, err := tx.QueryContext(ctx, `
		SELECT message_id, team_id, recipient_task_id, batch_id, kind, state, source_seq, created_at
		FROM team_mailbox_messages
		WHERE team_id = ? AND recipient_task_id = ? AND state = ?
		ORDER BY created_at, message_id`,
		teamID, recipientTaskID, MailboxPending)
	if err != nil {
		return nil, fmt.Errorf("%w: query the mailbox claim: %w", ErrUnavailable, err)
	}
	var claimed []MailboxMessage
	batchID := ""
	for rows.Next() {
		var m MailboxMessage
		if err := rows.Scan(&m.MessageID, &m.TeamID, &m.RecipientTaskID, &m.BatchID, &m.Kind, &m.State, &m.SourceSeq, &m.CreatedAt); err != nil {
			closeRows(ctx, rows)
			return nil, fmt.Errorf("%w: scan the mailbox claim: %w", ErrUnavailable, err)
		}
		if batchID == "" {
			batchID = m.BatchID
		}
		claimed = append(claimed, m)
	}
	if err := rows.Err(); err != nil {
		closeRows(ctx, rows)
		return nil, fmt.Errorf("%w: iterate the mailbox claim: %w", ErrUnavailable, err)
	}
	closeRows(ctx, rows)
	if len(claimed) == 0 {
		return nil, nil
	}
	if batchID == "" {
		batchID = claimBatchID(teamID, recipientTaskID, claimed[0].SourceSeq)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_mailbox_messages SET state = ?, batch_id = ?, updated_at = ?
		WHERE team_id = ? AND recipient_task_id = ? AND state = ?`,
		MailboxDelivering, batchID, s.clock.Now().UnixMilli(), teamID, recipientTaskID, MailboxPending,
	); err != nil {
		return nil, fmt.Errorf("%w: claim the mailbox: %w", ErrUnavailable, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("%w: commit the mailbox claim: %w", ErrUnavailable, err)
	}
	for i := range claimed {
		claimed[i].BatchID = batchID
		claimed[i].State = MailboxDelivering
	}
	return claimed, nil
}

// MarkMailboxBatchDelivered records the delivery of one claimed batch (Q-122):
// a delivered message is never resent (§6.1).
func (s *Store) MarkMailboxBatchDelivered(ctx context.Context, batchID string) error {
	return s.updateMailboxBatch(ctx, batchID, MailboxDelivered)
}

// PendingMailboxRows lists one recipient's pending mailbox rows, metadata
// only, oldest first (F11.9: the takeover cancel classifies them by their
// opened payloads before re-addressing them to the adopting lead).
func (s *Store) PendingMailboxRows(ctx context.Context, teamID, recipientTaskID string) ([]MailboxMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT message_id, team_id, recipient_task_id, batch_id, kind, state, source_seq, created_at
		FROM team_mailbox_messages
		WHERE team_id = ? AND recipient_task_id = ? AND state = ?
		ORDER BY created_at, message_id`,
		teamID, recipientTaskID, MailboxPending,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: query the pending mailbox rows: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	var out []MailboxMessage
	for rows.Next() {
		var m MailboxMessage
		if err := rows.Scan(&m.MessageID, &m.TeamID, &m.RecipientTaskID, &m.BatchID, &m.Kind, &m.State, &m.SourceSeq, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("%w: scan the pending mailbox row: %w", ErrUnavailable, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate the pending mailbox rows: %w", ErrUnavailable, err)
	}
	return out, nil
}

// ReleaseMailboxBatch returns a claimed batch to pending after a refused or
// busy send, keeping its batch ID for the resend (Q-122).
func (s *Store) ReleaseMailboxBatch(ctx context.Context, batchID string) error {
	return s.updateMailboxBatch(ctx, batchID, MailboxPending)
}

func (s *Store) updateMailboxBatch(ctx context.Context, batchID, state string) error {
	if batchID == "" {
		return fmt.Errorf("%w: a mailbox batch requires an ID", ErrTeamInvalidInput)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE team_mailbox_messages SET state = ? WHERE batch_id = ? AND state = ?`,
		state, batchID, MailboxDelivering,
	)
	if err != nil {
		return fmt.Errorf("%w: update the mailbox batch: %w", ErrUnavailable, err)
	}
	if affected, aerr := res.RowsAffected(); aerr == nil && affected == 0 {
		return fmt.Errorf("%w: mailbox batch %s", ErrTaskNotFound, batchID)
	}
	return nil
}

// DeliveringMailboxBatches lists the claims whose send outcome a crash left
// unknown; the recovery resolves each against the session transcript (Q-122).
func (s *Store) DeliveringMailboxBatches(ctx context.Context) ([]MailboxBatchRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT team_id, batch_id, recipient_task_id
		FROM team_mailbox_messages WHERE state = ?
		GROUP BY team_id, batch_id, recipient_task_id`, MailboxDelivering)
	if err != nil {
		return nil, fmt.Errorf("%w: query the delivering mailbox batches: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	var out []MailboxBatchRef
	for rows.Next() {
		var r MailboxBatchRef
		if err := rows.Scan(&r.TeamID, &r.BatchID, &r.RecipientTaskID); err != nil {
			return nil, fmt.Errorf("%w: scan the delivering mailbox batches: %w", ErrUnavailable, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate the delivering mailbox batches: %w", ErrUnavailable, err)
	}
	return out, nil
}

// OpenMailboxPayload decrypts one sealed message while requiring an unlocked
// vault; a locked vault fails closed and the message waits.
func (s *Store) OpenMailboxPayload(ctx context.Context, teamID, messageID string) ([]byte, error) {
	snap, err := s.requireUnlockedVault()
	if err != nil {
		return nil, err
	}
	var keyID, kind string
	var wrapped, envelope []byte
	err = s.db.QueryRowContext(ctx, `
		SELECT r.key_id, r.wrapped_key, m.kind, m.payload_envelope
		FROM team_mailbox_messages m
		JOIN team_records r ON r.team_id = m.team_id
		WHERE m.team_id = ? AND m.message_id = ?`,
		teamID, messageID,
	).Scan(&keyID, &wrapped, &kind, &envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: mailbox message %s", ErrTaskNotFound, messageID)
	}
	if err != nil {
		return nil, fmt.Errorf("query the sealed mailbox payload: %w", err)
	}
	return s.openTeamCiphertext(snap, teamID, keyID, wrapped, teamMailboxRecordType, messageID, kind, envelope)
}
