package workspacememory

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// Store owns workspace_memory_entries and workspace_memory_history.
type Store struct {
	db      *sql.DB
	clk     clock.Clock
	ids     ids.Generator
	scanner SecretScanner
	anchors AnchorReader
	events  EventSink
	cfg     Config
	log     *slog.Logger
}

// NewStore constructs a workspace memory owner store.
func NewStore(
	db *sql.DB,
	clk clock.Clock,
	gen ids.Generator,
	scanner SecretScanner,
	anchors AnchorReader,
	events EventSink,
	cfg Config,
) (*Store, error) {
	if db == nil || clk == nil || gen == nil || scanner == nil {
		return nil, errcode.New(errcode.InvalidArgument, "workspacememory store dependencies are required")
	}
	norm, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	log := norm.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Store{
		db:      db,
		clk:     clk,
		ids:     gen,
		scanner: scanner,
		anchors: anchors,
		events:  events,
		cfg:     norm,
		log:     log,
	}, nil
}

func rollbackQuietly(tx *sql.Tx) {
	if tx == nil {
		return
	}
	if err := tx.Rollback(); err != nil {
		return
	}
}

func closeRows(ctx context.Context, log *slog.Logger, rows *sql.Rows) {
	if err := rows.Close(); err != nil {
		log.WarnContext(ctx, "close workspace memory rows", "error_code", string(errcode.CodeOf(err)))
	}
}
