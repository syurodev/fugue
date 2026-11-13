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
	defer rollback(ctx, tx)
	if purgeErr := rejectPurged(ctx, tx, session.ID); purgeErr != nil {
		return purgeErr
	}
	now := s.clock.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO vendor_sessions(
			session_id,vendor,account_id,role,state,created_at,updated_at,team_id,task_id,
			resume_handle,process_pid,process_start_token,process_scope_unit,last_sequence,
			approval_mode,waiting_reason,stop_proof,close_reason,workspace_id,workspace_root)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,?)`,
		session.ID, session.Vendor, session.AccountID, session.Role, session.State, now, now,
		nullable(session.TeamID), nullable(session.TaskID), session.ResumeHandle,
		session.ProcessPID, session.ProcessStartToken, session.ProcessScopeUnit,
		session.ApprovalMode, session.WaitingReason, session.StopProof, session.CloseReason,
		session.WorkspaceID, session.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO vendor_events(session_id,sequence,event_type,recorded_at) VALUES (?,1,?,?)`,
		session.ID, VendorSessionStarted, now)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err = recordChange(ctx, tx, session.ID, ChangeCreated, session.State, session.WaitingReason, now); err != nil {
		return err
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
	if err := validateEvent(event); err != nil {
		return 0, err
	}
	epoch, protected, gateErr := s.payloadGate(event)
	if gateErr != nil {
		return 0, gateErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("%w: begin append: %w", ErrUnavailable, err)
	}
	defer rollback(ctx, tx)
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
	if err = commit(); err != nil {
		return 0, fmt.Errorf("%w: commit event: %w", ErrUnavailable, err)
	}
	return sequence, nil
}

// Snapshot reads the session and last sequence atomically.
func (s *Store) Snapshot(ctx context.Context, sessionID string) (Session, error) {
	var result Session
	err := s.db.QueryRowContext(ctx, sessionColumns+` FROM vendor_sessions WHERE session_id=?`, sessionID).Scan(scanSession(&result)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
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
	if err := rejectPurged(ctx, tx, sessionID); err != nil {
		return err
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
	_, err = tx.ExecContext(ctx,
		`INSERT INTO vendor_events(session_id,sequence,event_type,recorded_at,payload_id,metadata)
		VALUES (?,?,?,?,?,?)`,
		sessionID, sequence, event.Type, now, nullable(payloadID), string(metadata))
	if err != nil {
		return fmt.Errorf("%w: append event: %w", ErrUnavailable, err)
	}
	return s.project(ctx, tx, sessionID, sequence, event, now)
}

// EventMetadata is the allowlisted control data persisted with each event.
type EventMetadata struct {
	ContentID     string
	RawID         string
	ItemID        string
	State         string
	WaitingReason string
	TurnID        string
	TurnOrigin    string
	Outcome       string
	FailureCode   string
	StopProof     string
	ItemKind      string
	ItemTurnID    string
	ItemRevision  int64
	ItemMetadata  *ItemMetadata
}

func marshalEventMetadata(event *Event) ([]byte, error) {
	data := EventMetadata{
		ContentID:     event.contentID,
		RawID:         event.rawID,
		ItemID:        "",
		State:         event.State,
		WaitingReason: event.WaitingReason,
		TurnID:        event.TurnID,
		TurnOrigin:    event.TurnOrigin,
		Outcome:       event.Outcome,
		FailureCode:   event.FailureCode,
		StopProof:     event.StopProof,
		ItemKind:      "",
		ItemTurnID:    "",
		ItemRevision:  0,
		ItemMetadata:  nil,
	}
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
	result, err := s.db.ExecContext(ctx,
		`UPDATE vendor_sessions
		SET approval_mode=?,policy_revision=policy_revision+1,updated_at=?
		WHERE session_id=? AND policy_revision=? AND policy_revision<9223372036854775807
			AND state <> 'closed'`,
		mode, s.clock.Now().UTC().Format(time.RFC3339Nano), session, expected)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if n != 1 {
		return classifyMissedUpdate(s.db.QueryRowContext(ctx,
			"SELECT state FROM vendor_sessions WHERE session_id=?", session))
	}
	return nil
}

// classifyMissedUpdate explains why a guarded session update touched no row,
// so callers retry only a real conflict and not a closed or missing session.
func classifyMissedUpdate(row *sql.Row) error {
	var state string
	err := row.Scan(&state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrSessionNotFound
	case err != nil:
		return fmt.Errorf("%w: classify update: %w", ErrUnavailable, err)
	case state == StateClosed:
		return ErrSessionClosed
	default:
		return ErrSequenceConflict
	}
}

func normalizeEvent(event *Event) (*Event, error) {
	if event == nil {
		return nil, errcode.New(errcode.InvalidArgument, "event required")
	}
	if err := validateProcess(event.ProcessPID, event.ProcessStartToken, event.ProcessScopeUnit); err != nil {
		return nil, err
	}
	copyEvent := *event
	event = &copyEvent
	if event.Type == VendorSessionClosed {
		event.State = StateClosed
	}
	switch event.Type {
	case VendorSessionStarted, VendorSessionStateChanged, VendorTurnStarted, TranscriptItemRecorded, TranscriptItemUpdated, ApprovalRequested, ApprovalDecided, VendorTurnEnded, UsageObserved, QuotaObserved, VendorProcessExited, VendorSessionClosed:
	default:
		return nil, errcode.New(errcode.InvalidArgument, "invalid event type")
	}
	return event, nil
}

// validateProcess checks the identity internal/process needs to verify a
// recovered process: a PID with its start token, and a scope unit only when
// there is a process (empty when uncontained).
func validateProcess(pid int64, startToken, scopeUnit string) error {
	if pid < 0 || (pid > 0 && startToken == "") {
		return errcode.New(errcode.InvalidArgument, "process start token required")
	}
	if pid == 0 && (startToken != "" || scopeUnit != "") {
		return errcode.New(errcode.InvalidArgument, "process identity without a PID")
	}
	return nil
}

func normalizeSession(session *Session) (*Session, error) {
	if session == nil {
		return nil, errcode.New(errcode.InvalidArgument, "session required")
	}
	if err := validateProcess(session.ProcessPID, session.ProcessStartToken, session.ProcessScopeUnit); err != nil {
		return nil, err
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
	identity := &Event{
		Type:          eventType,
		State:         session.State,
		WaitingReason: session.WaitingReason,
		StopProof:     session.StopProof,
	}
	if err := validateState(identity); err != nil {
		return err
	}
	if session.ID == "" || session.Vendor == "" || session.AccountID == "" || session.Role == "" {
		return errcode.New(errcode.InvalidArgument, "invalid session")
	}
	return nil
}

func advanceSequence(ctx context.Context, tx *sql.Tx, sessionID string, expected int64, now string) error {
	result, err := tx.ExecContext(ctx,
		`UPDATE vendor_sessions SET last_sequence = last_sequence + 1, updated_at = ?
		WHERE session_id = ? AND last_sequence = ? AND state <> 'closed'`,
		now, sessionID, expected)
	if err != nil {
		return fmt.Errorf("%w: advance sequence: %w", ErrUnavailable, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: check sequence: %w", ErrUnavailable, err)
	}
	if n != 1 {
		if err := rejectPurged(ctx, tx, sessionID); err != nil {
			return err
		}
		return classifyMissedUpdate(tx.QueryRowContext(ctx,
			"SELECT state FROM vendor_sessions WHERE session_id=?", sessionID))
	}
	return nil
}

func rejectPurged(ctx context.Context, tx *sql.Tx, session string) error {
	var purged int
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM vendor_purges WHERE session_id=?)", session).Scan(&purged); err != nil {
		return fmt.Errorf("%w: purge barrier: %w", ErrUnavailable, err)
	}
	if purged != 0 {
		return ErrPurged
	}
	return nil
}
