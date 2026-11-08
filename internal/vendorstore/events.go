package vendorstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/vault"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// CreateSession creates the session and its first event atomically.
func (s *Store) CreateSession(ctx context.Context, session *Session) error {
	normalized, validationErr := normalizeSession(session)
	if validationErr != nil {
		return validationErr
	}
	session = normalized
	s.prepare.Lock()
	defer s.prepare.Unlock()
	if s.cache.IsPurged(session.ID) {
		return ErrPurged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer rollback(tx)
	now := s.clock.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO vendor_sessions(session_id,vendor,account_id,role,state,created_at,updated_at,team_id,task_id,resume_handle,process_pid,process_start_time,last_sequence,approval_mode,waiting_reason,stop_proof,close_reason) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?)`, session.ID, session.Vendor, session.AccountID, session.Role, session.State, now, now, nullable(session.TeamID), nullable(session.TaskID), session.ResumeHandle, session.ProcessPID, session.ProcessStartTime, session.ApprovalMode, session.WaitingReason, session.StopProof, session.CloseReason)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO vendor_events(session_id,sequence,event_type,recorded_at) VALUES (?,1,?,?)`, session.ID, VendorSessionStarted, now)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

// Append records one event with a contiguous per-session sequence.
func (s *Store) Append(ctx context.Context, sessionID string, expected int64, event *Event) (int64, error) {
	normalized, normalizeErr := normalizeEvent(event)
	if normalizeErr != nil {
		return 0, normalizeErr
	}
	event = normalized
	s.prepare.Lock()
	defer s.prepare.Unlock()
	if s.cache.IsPurged(sessionID) {
		return 0, ErrPurged
	}
	if operationErr1 := validateEvent(event); operationErr1 != nil {
		return 0, operationErr1
	}
	epoch, protected, gateErr := s.payloadGate(event)
	if gateErr != nil {
		return 0, gateErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("%w: begin append: %w", ErrUnavailable, err)
	}
	defer rollback(tx)
	now := s.clock.Now().UTC().Format(time.RFC3339Nano)
	if sequenceErr := advanceSequence(ctx, tx, sessionID, expected, now); sequenceErr != nil {
		return 0, sequenceErr
	}
	sequence := expected + 1
	if writeErr := s.appendRows(ctx, tx, sessionID, sequence, event, now); writeErr != nil {
		return 0, writeErr
	}
	if s.beforeCommit != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, s.beforeCommit)
	}
	commit := tx.Commit
	if protected {
		commit = func() error { return s.vault.WithUnlocked(epoch, tx.Commit) }
	}
	if operationErr2 := commit(); operationErr2 != nil {
		return 0, fmt.Errorf("%w: commit event: %w", ErrUnavailable, operationErr2)
	}
	return sequence, nil
}

// Snapshot reads the session and last sequence atomically.
func (s *Store) Snapshot(ctx context.Context, sessionID string) (Session, error) {
	var result Session
	err := s.db.QueryRowContext(ctx, `SELECT session_id,vendor,account_id,role,state,last_sequence,COALESCE(team_id,''),COALESCE(task_id,''),resume_handle,process_pid,process_start_time,approval_mode,policy_revision,waiting_reason,close_reason,stop_proof,created_at,updated_at FROM vendor_sessions WHERE session_id=?`, sessionID).Scan(&result.ID, &result.Vendor, &result.AccountID, &result.Role, &result.State, &result.LastSequence, &result.TeamID, &result.TaskID, &result.ResumeHandle, &result.ProcessPID, &result.ProcessStartTime, &result.ApprovalMode, &result.PolicyRevision, &result.WaitingReason, &result.CloseReason, &result.StopProof, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, errcode.New(errcode.NotFound, "session not found")
	}
	if err != nil {
		return Session{}, fmt.Errorf("%w: read session: %w", ErrUnavailable, err)
	}
	return result, nil
}

func (s *Store) payloadGate(event *Event) (vault.Snapshot, bool, error) {
	protected := event.Payload != nil || (event.Item != nil && (event.Item.Content != nil || event.Item.Raw != nil))
	if !protected {
		return vault.Snapshot{}, false, nil
	}
	if s.vault == nil {
		return vault.Snapshot{}, true, fmt.Errorf("%w: %w", ErrUnavailable, vault.ErrLocked)
	}
	epoch := s.vault.Snapshot()
	if epoch.State != vault.StateUnlocked {
		return epoch, true, fmt.Errorf("%w: %w", ErrUnavailable, vault.ErrLocked)
	}
	return epoch, true, nil
}

func (s *Store) appendRows(ctx context.Context, tx *sql.Tx, sessionID string, sequence int64, event *Event, now string) error {
	var purged int
	if operationErr3 := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM vendor_purges WHERE session_id=?", sessionID).Scan(&purged); operationErr3 != nil {
		return fmt.Errorf("%w: purge barrier: %w", ErrUnavailable, operationErr3)
	}
	if purged != 0 {
		return ErrPurged
	}
	payloadID, err := s.writePayload(ctx, tx, sessionID, "event", event.Payload, now)
	if err != nil {
		return err
	}
	if event.Item != nil {
		event.contentID, err = s.writePayload(ctx, tx, sessionID, "content", event.Item.Content, now)
		if err != nil {
			return err
		}
		event.rawID, err = s.writePayload(ctx, tx, sessionID, "raw", event.Item.Raw, now)
		if err != nil {
			return err
		}
	}
	metadata, marshalErr := marshalEventMetadata(event)
	if marshalErr != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, marshalErr)
	}
	if _, operationErr4 := tx.ExecContext(ctx, `INSERT INTO vendor_events (session_id,sequence,event_type,recorded_at,payload_id,metadata) VALUES (?,?,?,?,?,?)`, sessionID, sequence, event.Type, now, nullable(payloadID), string(metadata)); operationErr4 != nil {
		return fmt.Errorf("%w: append event: %w", ErrUnavailable, operationErr4)
	}
	if operationErr5 := s.project(ctx, tx, sessionID, sequence, event, now); operationErr5 != nil {
		return operationErr5
	}
	return nil
}

// EventMetadata is the allowlisted control data persisted with each event.
type EventMetadata struct {
	ContentID, RawID, ItemID, State, WaitingReason, TurnID, Outcome, FailureCode, StopProof string
	ItemKind, ItemTurnID                                                                    string
	ItemRevision                                                                            int64
	ItemMetadata                                                                            *ItemMetadata
}

func marshalEventMetadata(event *Event) ([]byte, error) {
	data := EventMetadata{State: event.State, WaitingReason: event.WaitingReason, TurnID: event.TurnID, Outcome: event.Outcome, FailureCode: event.FailureCode, StopProof: event.StopProof, ItemID: "", ContentID: event.contentID, RawID: event.rawID, ItemKind: "", ItemTurnID: "", ItemRevision: 0, ItemMetadata: nil}
	if event.Item != nil {
		data.ItemID = event.Item.ID
		data.ItemKind = event.Item.Kind
		data.ItemTurnID = event.Item.TurnID
		data.ItemMetadata = &event.Item.Metadata
		data.ItemRevision = event.Item.Revision + 1
	}
	result, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return result, nil
}

// UpdatePolicy changes approval mode only at the expected policy revision.
func (s *Store) UpdatePolicy(ctx context.Context, session string, expected int64, mode string) error {
	s.prepare.Lock()
	defer s.prepare.Unlock()
	if s.cache.IsPurged(session) {
		return ErrPurged
	}
	if !validMode(mode) {
		return errcode.New(errcode.InvalidArgument, "invalid approval mode")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE vendor_sessions SET approval_mode=?,policy_revision=policy_revision+1,updated_at=? WHERE session_id=? AND policy_revision=? AND policy_revision<9223372036854775807 AND state <> 'closed'`, mode, s.clock.Now().UTC().Format(time.RFC3339Nano), session, expected)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if n != 1 {
		return ErrSequenceConflict
	}
	return nil
}

func normalizeEvent(event *Event) (*Event, error) {
	if event == nil {
		return nil, errcode.New(errcode.InvalidArgument, "event required")
	}
	if event.ProcessPID < 0 || (event.ProcessPID > 0 && event.ProcessStartTime == "") {
		return nil, errcode.New(errcode.InvalidArgument, "process start time required")
	}
	copyEvent := *event
	event = &copyEvent
	if event.Type == VendorSessionClosed {
		event.State = StateClosed
	}
	switch event.Type {
	case VendorSessionStarted, VendorTurnStarted, TranscriptItemRecorded, TranscriptItemUpdated, ApprovalRequested, ApprovalDecided, VendorTurnEnded, UsageObserved, QuotaObserved, VendorProcessExited, VendorSessionClosed:
	default:
		return nil, errcode.New(errcode.InvalidArgument, "invalid event type")
	}
	return event, nil
}

func validMode(mode string) bool {
	switch mode {
	case ItemPlan, ApprovalAsk, ApprovalAuto, ApprovalFull:
		return true
	default:
		return false
	}
}

func normalizeSession(session *Session) (*Session, error) {
	if session == nil {
		return nil, errcode.New(errcode.InvalidArgument, "session required")
	}
	if session.ProcessPID < 0 || (session.ProcessPID > 0 && session.ProcessStartTime == "") {
		return nil, errcode.New(errcode.InvalidArgument, "process start time required")
	}
	if session.ApprovalMode == "" {
		sessionCopy := *session
		sessionCopy.ApprovalMode = ApprovalAuto
		session = &sessionCopy
	}
	if !validMode(session.ApprovalMode) || session.PolicyRevision != 0 || session.LastSequence != 0 {
		return nil, errcode.New(errcode.InvalidArgument, "invalid initial policy")
	}
	if err := validateSessionIdentity(session); err != nil {
		return nil, err
	}
	return session, nil
}

func validateSessionIdentity(session *Session) error {
	eventType := VendorSessionStarted
	if session.State == StateClosed {
		eventType = VendorSessionClosed
	}
	if session.State == "" {
		return errcode.New(errcode.InvalidArgument, "session state required")
	}
	if err := validateState(&Event{Type: eventType, State: session.State, WaitingReason: session.WaitingReason, StopProof: session.StopProof}); err != nil {
		return err
	}

	if session.ID == "" || session.Vendor == "" || session.AccountID == "" || session.Role == "" {
		return errcode.New(errcode.InvalidArgument, "invalid session")
	}
	return nil
}

func advanceSequence(ctx context.Context, tx *sql.Tx, sessionID string, expected int64, now string) error {
	result, err := tx.ExecContext(ctx, `UPDATE vendor_sessions SET last_sequence = last_sequence + 1, updated_at = ? WHERE session_id = ? AND last_sequence = ? AND state <> 'closed'`, now, sessionID, expected)
	if err != nil {
		return fmt.Errorf("%w: advance sequence: %w", ErrUnavailable, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: check sequence: %w", ErrUnavailable, err)
	}
	if n != 1 {
		return ErrSequenceConflict
	}
	return nil
}
