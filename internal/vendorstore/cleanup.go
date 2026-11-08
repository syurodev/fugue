package vendorstore

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
)

// rollback abandons a transaction. sql.ErrTxDone means it already committed or
// rolled back, which is the normal path of a deferred call; any other failure
// is logged without payload because the caller already has its own result.
func rollback(ctx context.Context, tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		slog.WarnContext(ctx, "vendor store rollback failed", "err", err.Error())
	}
}

func closeRows(ctx context.Context, rows *sql.Rows) {
	if err := rows.Close(); err != nil {
		slog.WarnContext(ctx, "vendor store rows close failed", "err", err.Error())
	}
}

func closeConn(ctx context.Context, conn *sql.Conn) {
	if err := conn.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		slog.WarnContext(ctx, "vendor store connection close failed", "err", err.Error())
	}
}
