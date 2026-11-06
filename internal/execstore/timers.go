package execstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/kernel"
)

// timerDeadlineLayout formats deadlines in fixed-width UTC RFC 3339 with 9 fractional digits.
// Standard time.RFC3339Nano trims trailing zeros, which breaks lexicographical ordering in SQLite.
const timerDeadlineLayout = "2006-01-02T15:04:05.000000000Z"

func formatTimerDeadline(t time.Time) string {
	return t.UTC().Format(timerDeadlineLayout)
}

func parseTimerDeadline(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timer deadline: %w", err)
	}
	return t, nil
}

// ArmedTimer describes a currently armed durable timer.
type ArmedTimer struct {
	TimerID    string
	SessionID  string
	RunID      string
	Generation uint64
	Purpose    kernel.TimerPurpose
	Deadline   time.Time
}

func writeTimerProjections(ctx context.Context, conn *sql.Conn, req *AppendRequest) error {
	for _, evt := range req.Events {
		switch ev := evt.(type) {
		case *kernel.TimerScheduledEvent:
			if err := projectTimerScheduled(ctx, conn, req.SessionID, req.RunID, ev); err != nil {
				return err
			}
		case *kernel.TimerElapsedEvent:
			if err := projectTimerElapsed(ctx, conn, ev); err != nil {
				return err
			}
		case *kernel.TimerCancelledEvent:
			if err := projectTimerCancelled(ctx, conn, ev); err != nil {
				return err
			}
		}
	}
	return nil
}

func projectTimerScheduled(ctx context.Context, conn *sql.Conn, sessionID, runID string, ev *kernel.TimerScheduledEvent) error {
	deadlineStr := formatTimerDeadline(ev.SchedulingDeadline)
	res, err := conn.ExecContext(
		ctx,
		`INSERT INTO exec_timers (timer_id, session_id, run_id, generation, purpose, target_id, deadline, state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'armed')
		 ON CONFLICT(timer_id) DO UPDATE SET
			generation = excluded.generation,
			purpose = excluded.purpose,
			target_id = excluded.target_id,
			deadline = excluded.deadline,
			state = 'armed'
		 WHERE exec_timers.session_id = excluded.session_id
		   AND exec_timers.run_id = excluded.run_id
		   AND excluded.generation > exec_timers.generation`,
		ev.TimerID, sessionID, runID, ev.Generation, string(ev.Purpose), ev.TargetID, deadlineStr,
	)
	if err != nil {
		return fmt.Errorf("%w: project TimerScheduled %s: %w", ErrInvalidBatch, ev.TimerID, err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return fmt.Errorf("%w: project TimerScheduled %s affected %d rows", ErrInvalidBatch, ev.TimerID, n)
	}
	return nil
}

func projectTimerElapsed(ctx context.Context, conn *sql.Conn, ev *kernel.TimerElapsedEvent) error {
	res, err := conn.ExecContext(
		ctx,
		`UPDATE exec_timers
		 SET state = 'elapsed'
		 WHERE timer_id = ? AND generation = ?`,
		ev.TimerID, ev.Generation,
	)
	if err != nil {
		return fmt.Errorf("%w: project TimerElapsed %s: %w", ErrInvalidBatch, ev.TimerID, err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return fmt.Errorf("%w: project TimerElapsed %s affected %d rows", ErrInvalidBatch, ev.TimerID, n)
	}
	return nil
}

func projectTimerCancelled(ctx context.Context, conn *sql.Conn, ev *kernel.TimerCancelledEvent) error {
	res, err := conn.ExecContext(
		ctx,
		`UPDATE exec_timers
		 SET state = 'cancelled'
		 WHERE timer_id = ? AND generation = ?`,
		ev.TimerID, ev.Generation,
	)
	if err != nil {
		return fmt.Errorf("%w: project TimerCancelled %s: %w", ErrInvalidBatch, ev.TimerID, err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return fmt.Errorf("%w: project TimerCancelled %s affected %d rows", ErrInvalidBatch, ev.TimerID, n)
	}
	return nil
}

// ListArmedTimers returns all currently armed timers in true deadline order, breaking ties by timer ID.
func (s *Store) ListArmedTimers(ctx context.Context) (timers []ArmedTimer, retErr error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreUnavailable
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT timer_id, session_id, run_id, generation, purpose, deadline
		 FROM exec_timers
		 WHERE state = 'armed'
		 ORDER BY deadline ASC, timer_id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: list armed timers: %w", ErrStoreUnavailable, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("%w: close rows: %w", ErrStoreUnavailable, closeErr)
		}
	}()

	timers = []ArmedTimer{}
	for rows.Next() {
		var (
			t           ArmedTimer
			purposeStr  string
			deadlineStr string
		)
		if err := rows.Scan(&t.TimerID, &t.SessionID, &t.RunID, &t.Generation, &purposeStr, &deadlineStr); err != nil {
			return nil, fmt.Errorf("%w: scan armed timer: %w", ErrStoreUnavailable, err)
		}
		t.Purpose = kernel.TimerPurpose(purposeStr)
		parsedDeadline, err := parseTimerDeadline(deadlineStr)
		if err != nil {
			return nil, fmt.Errorf("%w: parse timer deadline %q: %w", ErrCorruptHistory, deadlineStr, err)
		}
		t.Deadline = parsedDeadline.UTC()
		timers = append(timers, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate armed timers: %w", ErrStoreUnavailable, err)
	}
	return timers, nil
}

type timerRow struct {
	timerID    string
	generation uint64
	purpose    string
	deadline   string
	state      string
}

func (s *Store) verifyTimerProjection(ctx context.Context, runID string, state *kernel.State) (retErr error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT timer_id, generation, purpose, deadline, state
		 FROM exec_timers
		 WHERE run_id = ?`,
		runID,
	)
	if err != nil {
		return fmt.Errorf("%w: query exec_timers for rebuild: %w", ErrStoreUnavailable, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("%w: close rows: %w", ErrStoreUnavailable, closeErr)
		}
	}()

	seenArmed := make(map[string]bool)
	for rows.Next() {
		var r timerRow
		if err := rows.Scan(&r.timerID, &r.generation, &r.purpose, &r.deadline, &r.state); err != nil {
			return fmt.Errorf("%w: scan exec_timers for rebuild: %w", ErrStoreUnavailable, err)
		}
		if err := checkRebuiltTimerRow(r, state); err != nil {
			return err
		}
		if r.state == "armed" {
			seenArmed[r.timerID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: iterate exec_timers for rebuild: %w", ErrStoreUnavailable, err)
	}

	for timerID := range state.ActiveTimers {
		if !seenArmed[timerID] {
			return fmt.Errorf("%w: rebuilt active timer %s is missing from stored exec_timers", ErrCorruptHistory, timerID)
		}
	}
	return nil
}

func checkRebuiltTimerRow(r timerRow, state *kernel.State) error {
	active, isActive := state.ActiveTimers[r.timerID]
	switch r.state {
	case "armed":
		if !isActive || active == nil {
			return fmt.Errorf("%w: stored timer %s is armed but missing from rebuilt active timers", ErrCorruptHistory, r.timerID)
		}
		if active.Generation != r.generation {
			return fmt.Errorf("%w: stored timer %s generation %d != rebuilt generation %d", ErrCorruptHistory, r.timerID, r.generation, active.Generation)
		}
		if string(active.Purpose) != r.purpose {
			return fmt.Errorf("%w: stored timer %s purpose %s != rebuilt purpose %s", ErrCorruptHistory, r.timerID, r.purpose, active.Purpose)
		}
		parsedDeadline, err := parseTimerDeadline(r.deadline)
		if err != nil {
			return fmt.Errorf("%w: parse stored timer %s deadline: %w", ErrCorruptHistory, r.timerID, err)
		}
		if !active.Deadline.Equal(parsedDeadline) {
			return fmt.Errorf("%w: stored timer %s deadline %v != rebuilt deadline %v", ErrCorruptHistory, r.timerID, parsedDeadline, active.Deadline)
		}
	case "elapsed", "cancelled":
		if isActive && active != nil {
			return fmt.Errorf("%w: stored timer %s is %s but present in rebuilt active timers", ErrCorruptHistory, r.timerID, r.state)
		}
	}
	return nil
}
