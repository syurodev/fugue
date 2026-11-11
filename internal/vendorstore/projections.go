package vendorstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func validateEvent(e *Event) error {
	for _, check := range []func(*Event) error{validateTurn, validateState, validateItemEvent} {
		if err := check(e); err != nil {
			return err
		}
	}
	return nil
}

func validateTurn(e *Event) error {
	if e.TurnOrigin != "" && e.TurnOrigin != TurnOriginFugue && e.TurnOrigin != TurnOriginVendor {
		return errcode.New(errcode.InvalidArgument, "unknown turn origin")
	}
	if e.TurnOrigin != "" && e.Type != VendorTurnStarted {
		return errcode.New(errcode.InvalidArgument, "turn origin only valid when starting a turn")
	}
	if e.TurnOrigin == TurnOriginVendor && (!strings.HasPrefix(e.TurnID, "vendor-") || !ids.IsUUID(strings.TrimPrefix(e.TurnID, "vendor-"))) {
		return errcode.New(errcode.InvalidArgument, "vendor turn ID must contain a UUID")
	}
	if (e.Type == VendorTurnStarted || e.Type == VendorTurnEnded) && e.TurnID == "" {
		return errcode.New(errcode.InvalidArgument, "turn ID required")
	}
	if e.Type == VendorTurnEnded && e.Outcome == "" {
		return errcode.New(errcode.InvalidArgument, "turn outcome required")
	}
	if e.Outcome != "" && !validOutcome(e.Outcome) {
		return errcode.New(errcode.InvalidArgument, "unknown turn outcome")
	}
	if e.Outcome == OutcomeFailed && e.FailureCode == "" {
		return errcode.New(errcode.InvalidArgument, "failure code required")
	}
	return nil
}

func validateState(e *Event) error {
	if e.State != "" && !validState(e.State) {
		return errcode.New(errcode.InvalidArgument, "unknown session state")
	}
	if e.State != StateWaiting && e.WaitingReason != "" {
		return errcode.New(errcode.InvalidArgument, "waiting reason without waiting state")
	}
	if e.State == StateWaiting && !validWaitingReason(e.WaitingReason) {
		return errcode.New(errcode.InvalidArgument, "unknown waiting reason")
	}
	if e.Type == VendorSessionClosed && !validStopProof(e.StopProof) {
		return errcode.New(errcode.InvalidArgument, "invalid stop proof")
	}
	return nil
}

func validateItemEvent(e *Event) error {
	if e.Item == nil {
		return nil
	}
	if e.Type == TranscriptItemRecorded && e.Item.Revision != 0 {
		return errcode.New(errcode.InvalidArgument, "new item revision must be zero")
	}
	if e.Type == TranscriptItemUpdated && e.Item.Revision < 1 {
		return errcode.New(errcode.InvalidArgument, "expected item revision required")
	}
	if e.Item.ID == "" || e.Item.TurnID == "" {
		return errcode.New(errcode.InvalidArgument, "item identity required")
	}
	return validateItemMetadata(e.Item)
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) project(ctx context.Context, tx *sql.Tx, session string, sequence int64, e *Event, now string) error {
	if err := s.projectSession(ctx, tx, session, e); err != nil {
		return err
	}
	var err error
	switch e.Type {
	case VendorTurnStarted:
		_, err = tx.ExecContext(ctx,
			`INSERT INTO vendor_turns(session_id,turn_id,origin,created_at) VALUES (?,?,?,?)`,
			session, e.TurnID, turnOrigin(e.TurnOrigin), now)
	case VendorTurnEnded:
		return endTurn(ctx, tx, session, e, now)
	case TranscriptItemRecorded, TranscriptItemUpdated:
		if e.Item == nil {
			return errcode.New(errcode.InvalidArgument, "item required")
		}
		return s.projectItem(ctx, tx, session, sequence, e, now)
	}
	if err != nil {
		return fmt.Errorf("%w: project event: %w", ErrUnavailable, err)
	}
	return nil
}

func turnOrigin(origin string) string {
	if origin == "" {
		return TurnOriginFugue
	}
	return origin
}

func endTurn(ctx context.Context, tx *sql.Tx, session string, e *Event, now string) error {
	result, err := tx.ExecContext(ctx,
		`UPDATE vendor_turns SET outcome=?,failure_code=?,ended_at=?
		WHERE session_id=? AND turn_id=? AND outcome=''`,
		e.Outcome, e.FailureCode, now, session, e.TurnID)
	if err != nil {
		return fmt.Errorf("%w: end turn: %w", ErrUnavailable, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: end turn: %w", ErrUnavailable, err)
	}
	if n != 1 {
		return ErrTurnNotOpen
	}
	return nil
}

func (s *Store) projectItem(ctx context.Context, tx *sql.Tx, session string, sequence int64, e *Event, now string) error {
	item := e.Item
	metadata, marshalErr := json.Marshal(item.Metadata)
	if marshalErr != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, marshalErr)
	}
	content, raw := e.contentID, e.rawID
	var err error
	if e.Type != TranscriptItemRecorded {
		return updateItem(ctx, tx, session, item, now, content, raw)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO vendor_transcript_items(
			session_id,item_id,turn_id,kind,sequence,revision,created_at,updated_at,
			payload_id,raw_payload_id,metadata)
		VALUES (?,?,?,?,?,1,?,?,?,?,?)`,
		session, item.ID, item.TurnID, item.Kind, sequence, now, now,
		nullable(content), nullable(raw), string(metadata))
	if err != nil {
		return fmt.Errorf("%w: project item: %w", ErrUnavailable, err)
	}
	return nil
}

func updateItem(ctx context.Context, tx *sql.Tx, session string, item *Item, now, content, raw string) error {
	metadata, marshalErr := json.Marshal(item.Metadata)
	if marshalErr != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, marshalErr)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE vendor_transcript_items
		SET metadata=?,revision=revision+1,updated_at=?,
			payload_id=COALESCE(?,payload_id),raw_payload_id=COALESCE(?,raw_payload_id)
		WHERE session_id=? AND item_id=? AND kind=? AND revision=? AND revision<9223372036854775807`,
		string(metadata), now, nullable(content), nullable(raw),
		session, item.ID, item.Kind, item.Revision)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if n != 1 {
		return classifyMissedItemUpdate(tx.QueryRowContext(ctx,
			"SELECT revision FROM vendor_transcript_items WHERE session_id=? AND item_id=?",
			session, item.ID))
	}
	return nil
}

// classifyMissedItemUpdate tells a missing item from a stale revision or kind,
// so callers can retry only the second.
func classifyMissedItemUpdate(row *sql.Row) error {
	var revision int64
	err := row.Scan(&revision)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrItemNotFound
	case err != nil:
		return fmt.Errorf("%w: classify item update: %w", ErrUnavailable, err)
	default:
		return ErrSequenceConflict
	}
}

func (s *Store) projectSession(ctx context.Context, tx *sql.Tx, session string, e *Event) error {
	var err error
	if e.Type == VendorSessionClosed {
		e.State = StateClosed
	}
	if e.Type != VendorSessionClosed && e.State == StateClosed {
		return errcode.New(errcode.InvalidArgument, "closure requires session close event")
	}
	if e.ProcessPID != 0 || e.ResumeHandle != "" || e.CloseReason != "" {
		_, err = tx.ExecContext(ctx,
			`UPDATE vendor_sessions SET
				process_pid=CASE WHEN ?>0 THEN ? ELSE process_pid END,
				process_start_token=CASE WHEN ?>0 THEN ? ELSE process_start_token END,
				process_scope_unit=CASE WHEN ?>0 THEN ? ELSE process_scope_unit END,
				resume_handle=CASE WHEN ?<>'' THEN ? ELSE resume_handle END,
				close_reason=CASE WHEN ?<>'' THEN ? ELSE close_reason END
			WHERE session_id=?`,
			e.ProcessPID, e.ProcessPID,
			e.ProcessPID, e.ProcessStartToken,
			e.ProcessPID, e.ProcessScopeUnit,
			e.ResumeHandle, e.ResumeHandle,
			e.CloseReason, e.CloseReason,
			session)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	if e.State != "" {
		_, err = tx.ExecContext(ctx,
			`UPDATE vendor_sessions SET state=?,waiting_reason=?,stop_proof=? WHERE session_id=?`,
			e.State, e.WaitingReason, e.StopProof, session)
		if err != nil {
			return fmt.Errorf("%w: project session: %w", ErrUnavailable, err)
		}
	}
	return nil
}
