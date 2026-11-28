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

// Add creates a new workspace memory entry.
func (s *Store) Add(_ context.Context, _ AddInput) (Entry, error) {
	return Entry{}, errcode.New(errcode.Unavailable, "workspace memory add is pending")
}

// Update updates an existing workspace memory entry under CAS.
func (s *Store) Update(_ context.Context, _ UpdateInput) (Entry, error) {
	return Entry{}, errcode.New(errcode.Unavailable, "workspace memory update is pending")
}

// Invalidate marks an existing entry as invalid or superseded under CAS.
func (s *Store) Invalidate(_ context.Context, _ InvalidateInput) (Entry, error) {
	return Entry{}, errcode.New(errcode.Unavailable, "workspace memory invalidate is pending")
}

// Restore restores an earlier revision of an entry as a user write under CAS.
func (s *Store) Restore(_ context.Context, _ RestoreInput) (Entry, error) {
	return Entry{}, errcode.New(errcode.Unavailable, "workspace memory restore is pending")
}

// Verify marks an entry as verified under CAS.
func (s *Store) Verify(_ context.Context, _ VerifyInput) (Entry, error) {
	return Entry{}, errcode.New(errcode.Unavailable, "workspace memory verify is pending")
}

// GetByID reads a workspace memory entry by ID and evaluates anchor freshness.
func (s *Store) GetByID(_ context.Context, _, _ string) (Entry, error) {
	return Entry{}, errcode.New(errcode.Unavailable, "workspace memory get by id is pending")
}

// GetByLabel reads a valid workspace memory entry by label and evaluates anchor freshness.
func (s *Store) GetByLabel(_ context.Context, _, _ string) (Entry, error) {
	return Entry{}, errcode.New(errcode.Unavailable, "workspace memory get by label is pending")
}

// List returns workspace memory entries matching filter with evaluated freshness.
func (s *Store) List(_ context.Context, _ ListFilter) ([]Entry, error) {
	return nil, errcode.New(errcode.Unavailable, "workspace memory list is pending")
}

// History returns immutable revision snapshots for an entry ordered by revision ascending.
func (s *Store) History(_ context.Context, _, _ string) ([]HistoryEntry, error) {
	return nil, errcode.New(errcode.Unavailable, "workspace memory history is pending")
}

// CheckLanguage evaluates the C-08 English-prose heuristic.
func CheckLanguage(_ string, _ int, _ float64) error {
	return errcode.New(errcode.Unavailable, "workspace memory language check is pending")
}
