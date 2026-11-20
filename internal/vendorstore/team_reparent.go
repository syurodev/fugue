package vendorstore

// The §12 takeover projection of a team manager's recovery cancel (F11.9,
// Q-159 A): the manager's non-terminal children re-parent to the lead in the
// same transaction as the event, so running work is not lost.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// reparentChildrenOnManagerCancel projects the §12 takeover (Q-159 A): the
// cancel recovery decision on an unknown team_manager hands its non-terminal
// children to the lead instead of cancelling them, so running work is not
// lost. The re-parent lands in the same transaction as the event. Any other
// event must carry no ReparentTo, and the adopter must be the team's lead
// task; the moved children become depth-1 members of the lead.
func reparentChildrenOnManagerCancel(ctx context.Context, tx *sql.Tx, teamID string, ev *AppendTeamEventInput, role TeamRole, now int64) error {
	if ev.ReparentTo == "" {
		return nil
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
	return nil
}
