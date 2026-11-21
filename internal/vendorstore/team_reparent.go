package vendorstore

// The §12 takeover projection of a team manager's recovery cancel (F11.9,
// Q-159 A): the manager's non-terminal children re-parent to the lead in the
// same transaction as the event, so running work is not lost.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// placeholders builds one `?, ?, ...` fragment for an IN clause.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// reparentChildrenOnManagerCancel projects the §12 takeover (Q-159 A): the
// cancel recovery decision on an unknown team_manager hands its non-terminal
// children to the lead instead of cancelling them, so running work is not
// lost. The re-parent lands in the same transaction as the event. Any other
// event must carry no ReparentTo, and the adopter must be the team's lead
// task; the moved children become depth-1 members of the lead.
func reparentChildrenOnManagerCancel(ctx context.Context, tx *sql.Tx, teamID string, ev *AppendTeamEventInput, role TeamRole, now int64) error {
	if ev.ReparentTo == "" {
		return requireNoReaddress(ev)
	}
	if ev.EventType != EventTaskRecoveryDecided || ev.Outcome != RecoveryOutcomeCancel || role != RoleTeamManager {
		return fmt.Errorf("%w: reparent_to is only valid on a team manager's recovery cancel", ErrTeamInvalidInput)
	}
	var adopterRole string
	err := tx.QueryRowContext(ctx,
		`SELECT hierarchy_role FROM team_tasks WHERE team_id = ? AND task_id = ?`,
		teamID, ev.ReparentTo,
	).Scan(&adopterRole)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: reparent adopter %s", ErrTaskNotFound, ev.ReparentTo)
	}
	if err != nil {
		return fmt.Errorf("query reparent adopter: %w", err)
	}
	if adopterRole != string(RoleLead) {
		return fmt.Errorf("%w: reparent adopter must be the lead task", ErrTeamInvalidInput)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_tasks SET parent_task_id = ?, depth = 1, updated_at = ?
		WHERE team_id = ? AND parent_task_id = ?
		  AND state NOT IN ('done', 'failed', 'stopped', 'cancelled')`,
		ev.ReparentTo, now, teamID, ev.TaskID,
	); err != nil {
		return fmt.Errorf("reparent manager children: %w", err)
	}
	return readdressManagerMailboxTx(ctx, tx, teamID, ev, now)
}

// readdressManagerMailboxTx hands the cancel target's queued mail to the
// adopter (F11.9): a cancelled manager never claims its mailbox, so its
// pending, unclaimed rows — a member's done digest queued while the manager
// was unknown — are re-addressed to the lead inside the same transaction.
// Message IDs and created_at stay untouched, so the idempotency keys and the
// digest order survive the move; rows a batch already claimed (delivering or
// delivered) are left alone so a delivery never duplicates. The keep list
// names the rows the lead itself sent to the manager, judged from their
// opened payloads before the append.
func readdressManagerMailboxTx(ctx context.Context, tx *sql.Tx, teamID string, ev *AppendTeamEventInput, now int64) error {
	if ev.ReaddressMailboxFrom == "" {
		return nil
	}
	if ev.ReaddressMailboxFrom != ev.TaskID {
		return fmt.Errorf("%w: readdress_mailbox_from must be the cancel target", ErrTeamInvalidInput)
	}
	query := `
		UPDATE team_mailbox_messages SET recipient_task_id = ?, updated_at = ?
		WHERE team_id = ? AND recipient_task_id = ? AND state = ?`
	args := []any{ev.ReparentTo, now, teamID, ev.ReaddressMailboxFrom, MailboxPending}
	if len(ev.ReaddressMailboxKeep) > 0 {
		query += ` AND message_id NOT IN (` + placeholders(len(ev.ReaddressMailboxKeep)) + `)`
		for _, id := range ev.ReaddressMailboxKeep {
			args = append(args, id)
		}
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("readdress manager mailbox: %w", err)
	}
	return nil
}

// requireNoReaddress rejects a re-address on an event that re-parents
// nothing, keeping the input tied to the takeover cancel alone.
func requireNoReaddress(ev *AppendTeamEventInput) error {
	if ev.ReaddressMailboxFrom != "" {
		return fmt.Errorf("%w: readdress_mailbox_from is only valid on a team manager's recovery cancel", ErrTeamInvalidInput)
	}
	return nil
}
