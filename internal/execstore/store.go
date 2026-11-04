package execstore

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
)

// Store provides the transactional execution store adapter over SQLite.
type Store struct {
	db          *sql.DB
	host        storage.HostIncarnation
	clk         clock.Clock
	ids         ids.Generator
	equality    ProtectedEquality
	faultPoints map[FaultPoint]error
	mu          sync.Mutex
}

// New creates and validates an execution store over db.
// It verifies that journal_mode=DELETE, synchronous=FULL and foreign_keys=ON.
func New(
	ctx context.Context,
	db *sql.DB,
	host storage.HostIncarnation,
	clk clock.Clock,
	idGen ids.Generator,
	equality ProtectedEquality,
) (*Store, error) {
	if db == nil {
		return nil, errcode.New(errcode.InvalidArgument, "database handle is required")
	}
	if clk == nil {
		return nil, errcode.New(errcode.InvalidArgument, "clock is required")
	}
	if idGen == nil {
		return nil, errcode.New(errcode.InvalidArgument, "id generator is required")
	}
	if host.LockGeneration == 0 || host.IncarnationID == "" {
		return nil, errcode.New(errcode.InvalidArgument, "valid host incarnation is required")
	}

	if err := storage.VerifyPragmas(ctx, db); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsafePragmas, err)
	}

	return &Store{
		db:          db,
		host:        host,
		clk:         clk,
		ids:         idGen,
		equality:    equality,
		faultPoints: make(map[FaultPoint]error),
		mu:          sync.Mutex{},
	}, nil
}

// DB returns the underlying sql.DB handle.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Host returns the host incarnation associated with this store.
func (s *Store) Host() storage.HostIncarnation {
	if s == nil {
		return storage.HostIncarnation{}
	}
	return s.host
}

// SetFault configures a named fault point for fault injection testing.
func (s *Store) SetFault(point FaultPoint, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.faultPoints, point)
	} else {
		s.faultPoints[point] = err
	}
}

func (s *Store) checkFault(point FaultPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.faultPoints[point]
}
