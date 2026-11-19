package vendorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
)

// txStatements routes queries inside a transaction through statements prepared
// once for the transaction when populated by prepareAndRun, and falls back to
// unprepared tx calls otherwise (for single-event transactions).
type txStatements struct {
	tx       *sql.Tx
	prepared map[string]*sql.Stmt
}

func newTxStatements(tx *sql.Tx) *txStatements {
	return &txStatements{tx: tx, prepared: make(map[string]*sql.Stmt)}
}

// prepareAndRun prepares every query in queries once on tx, closes them via
// defer when fn returns, and runs fn against stmts. Prepared statements stay
// scoped to the transaction and are always closed when it finishes.
func prepareAndRun(ctx context.Context, tx *sql.Tx, stmts *txStatements, queries []string, fn func() error) error {
	if len(queries) == 0 {
		return fn()
	}
	stmt, err := tx.PrepareContext(ctx, queries[0])
	if err != nil {
		return fmt.Errorf("%w: prepare statement: %w", ErrUnavailable, err)
	}
	defer closeStmt(ctx, stmt)
	stmts.prepared[queries[0]] = stmt
	return prepareAndRun(ctx, tx, stmts, queries[1:], fn)
}

func closeStmt(ctx context.Context, stmt *sql.Stmt) {
	if err := stmt.Close(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		slog.WarnContext(ctx, "vendor store statement close failed", "err", err.Error())
	}
}

// ExecContext runs sql.Tx.ExecContext, using the prepared statement when one
// was prepared for this transaction.
func (t *txStatements) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if stmt := t.prepared[query]; stmt != nil {
		res, err := stmt.ExecContext(ctx, args...)
		if err != nil {
			return nil, fmt.Errorf("exec prepared statement: %w", err)
		}
		return res, nil
	}
	res, err := t.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("exec statement: %w", err)
	}
	return res, nil
}

// QueryRowContext runs sql.Tx.QueryRowContext, using the prepared statement
// when one was prepared for this transaction.
func (t *txStatements) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if stmt := t.prepared[query]; stmt != nil {
		return stmt.QueryRowContext(ctx, args...)
	}
	return t.tx.QueryRowContext(ctx, query, args...)
}

type batchShape struct {
	payload, turnStart, turnEnd, itemRecord, itemUpdate, process, state bool
}

func observeBatch(events []*Event) batchShape {
	var shape batchShape
	for _, e := range events {
		observeEvent(&shape, e)
	}
	return shape
}

func observeEvent(shape *batchShape, e *Event) {
	if eventHasPayload(e) {
		shape.payload = true
	}
	switch e.Type {
	case VendorTurnStarted:
		shape.turnStart = true
	case VendorTurnEnded:
		shape.turnEnd = true
	case TranscriptItemRecorded:
		shape.itemRecord = true
	case TranscriptItemUpdated:
		shape.itemUpdate = true
	case VendorSessionClosed:
		shape.state = true
	}
	if e.ProcessPID != 0 || e.ResumeHandle != "" || e.CloseReason != "" {
		shape.process = true
	}
	if e.State != "" {
		shape.state = true
	}
}

func eventHasPayload(e *Event) bool {
	if e.Payload != nil {
		return true
	}
	return e.Item != nil && (e.Item.Content != nil || e.Item.Raw != nil)
}

func batchQueries(events []*Event) []string {
	shape := observeBatch(events)
	queries := []string{advanceSequenceQuery, rejectPurgedQuery, insertEventQuery}
	groups := []struct {
		needed  bool
		queries []string
	}{
		{shape.payload, []string{reservePayloadQuery, selectSessionKeyQuery, insertPayloadQuery}},
		{shape.turnStart, []string{insertTurnQuery}},
		{shape.turnEnd, []string{endTurnQuery}},
		{shape.itemRecord, []string{insertItemQuery}},
		{shape.itemUpdate, []string{updateItemQuery}},
		{shape.process, []string{updateSessionProcessQuery}},
		{shape.state, []string{updateSessionStateQuery, insertChangeQuery, pruneChangesQuery}},
	}
	for _, group := range groups {
		if group.needed {
			queries = append(queries, group.queries...)
		}
	}
	return queries
}
