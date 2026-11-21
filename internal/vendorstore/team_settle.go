package vendorstore

// SettleTeam ends a still-proposed or active team in one transaction: every
// non-terminal task cancels first, then TeamEnded appends (Q-84). A failure
// anywhere leaves the team exactly as it was, so a settlement is never half
// applied behind a decision the user already saw.

import (
	"context"
	"database/sql"
	"fmt"
)

// TeamSettleInput ends a team as cancelled: the sealed payload carries the
// decision (F11.2b solo and reject, F11.2a cancel).
type TeamSettleInput struct {
	ExpectedSeq int64
	EndEventID  string
	EndPayload  []byte
	// Mailbox rows are written in the same transaction as the TeamEnded
	// event, keyed to its sequence (Q-122, OR-E13 delivery).
	Mailbox []MailboxEnqueueInput
}

// SettleTeam cancels the team's non-terminal tasks and appends TeamEnded
// atomically, returning the team's new last sequence.
func (s *Store) SettleTeam(ctx context.Context, teamID string, in *TeamSettleInput) (int64, error) {
	if !validSettleInput(teamID, in) {
		return 0, fmt.Errorf("%w: a settlement requires a team, an event ID, a sequence and a sealed payload", ErrTeamInvalidInput)
	}
	snap, err := s.requireUnlockedVault()
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin team settlement tx: %w", err)
	}
	defer rollback(ctx, tx)

	meta, err := loadTeamMetaForUpdate(ctx, tx, teamID, in.ExpectedSeq)
	if err != nil {
		return 0, err
	}
	now := s.clock.Now().UnixMilli()
	lastSeq, err := s.cancelOpenTasks(ctx, tx, teamID, in, now)
	if err != nil {
		return 0, err
	}
	lastSeq++
	if err := s.appendSettleEnd(ctx, tx, teamID, &meta, in, lastSeq, now); err != nil {
		return 0, err
	}
	if err := cancelTeamTimersTx(ctx, tx, teamID, now); err != nil {
		return 0, err
	}
	if err := s.commitTeamTx(teamID, lastSeq, snap, true, tx.Commit); err != nil {
		return 0, err
	}
	return lastSeq, nil
}

func validSettleInput(teamID string, in *TeamSettleInput) bool {
	return in != nil && teamID != "" && in.EndEventID != "" && in.ExpectedSeq >= 1 && len(in.EndPayload) > 0
}

// appendSettleEnd appends the TeamEnded event after the task cancels.
func (s *Store) appendSettleEnd(ctx context.Context, tx *sql.Tx, teamID string, meta *teamMetaRow, in *TeamSettleInput, seq, now int64) error {
	end := &AppendTeamEventInput{
		EventID: in.EndEventID, EventType: EventTeamEnded, Outcome: outcomeCancelled, ProtectedPayload: in.EndPayload,
	}
	fromState, toState, err := applyTeamProjection(ctx, tx, teamID, meta.State, seq, now, end)
	if err != nil {
		return err
	}
	envelope, err := s.sealEventPayloadIfPresent(teamID, meta.KeyID, meta.WrappedKey, end)
	if err != nil {
		return err
	}
	if err := insertAppendedTeamEvent(ctx, tx, teamID, seq, end, fromState, toState, envelope, now); err != nil {
		return err
	}
	if len(in.Mailbox) == 0 {
		return nil
	}
	rawKey, unwrapErr := s.unwrapTeamKey(teamID, meta.KeyID, meta.WrappedKey)
	if unwrapErr != nil {
		return unwrapErr
	}
	defer clear(rawKey)
	return s.insertMailboxRows(ctx, tx, teamID, seq, in.Mailbox, meta.KeyID, rawKey, now)
}

// cancelOpenTasks cancels every non-terminal task of the team inside the
// transaction and returns the last sequence it used (Q-84: tasks first).
func (s *Store) cancelOpenTasks(ctx context.Context, tx *sql.Tx, teamID string, in *TeamSettleInput, now int64) (int64, error) {
	tasks, err := queryTeamTasks(ctx, tx, teamID)
	if err != nil {
		return 0, err
	}
	lastSeq := in.ExpectedSeq
	for i := range tasks {
		if isTaskTerminal(tasks[i].State) {
			continue
		}
		if lastSeq, err = s.appendTaskCancellation(ctx, tx, teamID, tasks[i].TaskID, cancelEventID(in.EndEventID, tasks[i].TaskID), lastSeq+1, now); err != nil {
			return 0, err
		}
	}
	return lastSeq, nil
}
