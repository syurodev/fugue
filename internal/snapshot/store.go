package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Kind distinguishes turn snapshots, tool before/after snapshots, and safety
// snapshots taken before a restore or rewind.
type Kind string

const (
	// KindTurn is a workspace snapshot captured before a turn.
	KindTurn Kind = "turn"
	// KindTool is a before/after snapshot pair captured around a
	// side-effecting tool invocation.
	KindTool Kind = "tool"
	// KindRestoreSafety is a safety snapshot captured before a restore.
	KindRestoreSafety Kind = "restore_safety"
	// KindRewindSafety is a safety snapshot captured before a rewind.
	KindRewindSafety Kind = "rewind_safety"
)

// Record is the metadata stored for a snapshot. Callers outside the snapshot
// store hold only ID (a random identifier), while BeforeTree and AfterTree
// remain inside the store.
type Record struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"sessionId"`
	ProjectKey string    `json:"projectKey"`
	Kind       Kind      `json:"kind"`
	BeforeTree string    `json:"beforeTree"`
	AfterTree  string    `json:"afterTree,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Records is the narrow persistence contract Maintain needs to reconcile shadow
// repositories with stored snapshot metadata.
type Records interface {
	ListCheckpoints(ctx context.Context) ([]Record, error)
	DeleteCheckpoints(ctx context.Context, ids []string) error
}

// Store persists snapshot records and resolves opaque snapshot IDs to git tree
// hashes.
type Store interface {
	Records
	PutSnapshot(ctx context.Context, rec *Record) error
	GetSnapshot(ctx context.Context, id string) (Record, error)
}

// MemoryStore is an in-memory Store implementation safe for concurrent use.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	order   []string
}

// NewMemoryStore creates an empty in-memory snapshot store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		records: make(map[string]Record),
	}
}

// PutSnapshot inserts or updates a snapshot record.
func (s *MemoryStore) PutSnapshot(_ context.Context, rec *Record) error {
	if rec == nil || strings.TrimSpace(rec.ID) == "" {
		return errcode.New(errcode.InvalidArgument, "snapshot: record id is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.records[rec.ID]; !exists {
		s.order = append(s.order, rec.ID)
	}
	s.records[rec.ID] = *rec
	return nil
}

// GetSnapshot looks up a snapshot record by its opaque ID.
func (s *MemoryStore) GetSnapshot(_ context.Context, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrSnapshotNotFound, id)
	}
	return rec, nil
}

// ListCheckpoints returns all stored snapshot records in insertion order.
func (s *MemoryStore) ListCheckpoints(_ context.Context) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.order))
	for _, id := range s.order {
		if rec, ok := s.records[id]; ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// DeleteCheckpoints removes the given snapshot IDs from the store.
func (s *MemoryStore) DeleteCheckpoints(_ context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	gone := toSet(ids)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range gone {
		delete(s.records, id)
	}
	kept := s.order[:0]
	for _, id := range s.order {
		if !gone[id] {
			kept = append(kept, id)
		}
	}
	s.order = kept
	return nil
}

// CaptureTurn captures a turn snapshot (SlotBefore) with a fresh random
// snapshot ID, stores it in store, and enforces the session's turn quota
// without affecting tool snapshots.
func (e *Engine) CaptureTurn(ctx context.Context, store Store, sessionID, workTree string) (Record, Snapshot, error) {
	return e.captureBeforeSlot(ctx, store, sessionID, workTree, KindTurn)
}

// CaptureToolBefore captures the before-slot of a side-effecting tool snapshot
// with a fresh random snapshot ID, stores it in store, and enforces the
// session's circular tool quota without evicting turn snapshots.
func (e *Engine) CaptureToolBefore(ctx context.Context, store Store, sessionID, workTree string) (Record, Snapshot, error) {
	return e.captureBeforeSlot(ctx, store, sessionID, workTree, KindTool)
}

func (e *Engine) captureBeforeSlot(ctx context.Context, store Store, sessionID, workTree string, kind Kind) (Record, Snapshot, error) {
	if store == nil {
		return Record{}, Snapshot{}, errcode.New(errcode.InvalidArgument, "snapshot: store is nil")
	}
	if strings.TrimSpace(sessionID) == "" {
		return Record{}, Snapshot{}, errcode.New(errcode.InvalidArgument, "snapshot: session id is empty")
	}
	repo, err := e.Open(ctx, workTree)
	if err != nil {
		return Record{}, Snapshot{}, err
	}
	id, err := e.ids.NewID()
	if err != nil {
		return Record{}, Snapshot{}, fmt.Errorf("snapshot: generate snapshot id: %w", err)
	}
	snap, err := repo.Snapshot(ctx, id, SlotBefore)
	if err != nil {
		return Record{}, Snapshot{}, err
	}
	rec := Record{
		ID:         id,
		SessionID:  sessionID,
		ProjectKey: repo.Key(),
		Kind:       kind,
		BeforeTree: snap.Tree,
		CreatedAt:  e.clock.Now(),
	}
	if err := store.PutSnapshot(ctx, &rec); err != nil {
		return Record{}, Snapshot{}, fmt.Errorf("snapshot: store snapshot %s: %w", id, err)
	}
	if err := e.enforceSessionQuota(ctx, store, repo, sessionID, kind); err != nil {
		return Record{}, Snapshot{}, err
	}
	return rec, snap, nil
}

// CaptureToolAfter captures the after-slot of a tool snapshot previously opened
// by CaptureToolBefore and records the resulting tree hash under the same
// snapshot ID.
func (e *Engine) CaptureToolAfter(ctx context.Context, store Store, snapshotID, workTree string) (Record, Snapshot, error) {
	if store == nil {
		return Record{}, Snapshot{}, errcode.New(errcode.InvalidArgument, "snapshot: store is nil")
	}
	rec, err := store.GetSnapshot(ctx, snapshotID)
	if err != nil {
		return Record{}, Snapshot{}, fmt.Errorf("snapshot: get %s: %w", snapshotID, err)
	}
	if rec.Kind != KindTool {
		return Record{}, Snapshot{}, errcode.New(errcode.InvalidArgument, "snapshot: expected tool snapshot for "+snapshotID)
	}
	repo, err := e.Open(ctx, workTree)
	if err != nil {
		return Record{}, Snapshot{}, err
	}
	snap, err := repo.Snapshot(ctx, snapshotID, SlotAfter)
	if err != nil {
		return Record{}, Snapshot{}, err
	}
	rec.AfterTree = snap.Tree
	if err := store.PutSnapshot(ctx, &rec); err != nil {
		return Record{}, Snapshot{}, fmt.Errorf("snapshot: update after tree for %s: %w", snapshotID, err)
	}
	return rec, snap, nil
}

// PlanToolRestore plans a restore for a single tool invocation identified by
// snapshotID. Only paths present in the tool's own before/after diff are in
// scope; files modified outside that diff are left untouched, and files inside
// that diff that changed after the tool finished are reported as conflicts.
func (e *Engine) PlanToolRestore(ctx context.Context, store Store, snapshotID, workTree string, override []string) (*RestorePlan, error) {
	if store == nil {
		return nil, errcode.New(errcode.InvalidArgument, "snapshot: store is nil")
	}
	rec, err := store.GetSnapshot(ctx, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("snapshot: get %s: %w", snapshotID, err)
	}
	if rec.Kind != KindTool {
		return nil, errcode.New(errcode.InvalidArgument, "snapshot: expected tool snapshot for "+snapshotID)
	}
	if rec.BeforeTree == "" || rec.AfterTree == "" {
		return nil, ErrTreeMissing
	}
	repo, err := e.Open(ctx, workTree)
	if err != nil {
		return nil, err
	}
	changes, err := repo.Diff(ctx, rec.BeforeTree, rec.AfterTree)
	if err != nil {
		return nil, err
	}
	only := make([]string, 0, len(changes))
	for i := range changes {
		only = append(only, changes[i].Path)
	}
	return repo.PlanRestore(ctx, rec.BeforeTree, rec.AfterTree, only, override)
}

// RestoreTool plans and applies a restore for a single tool invocation
// identified by snapshotID, touching only the paths in that tool's before/after
// diff.
func (e *Engine) RestoreTool(ctx context.Context, store Store, snapshotID, workTree string, override []string) ([]string, *RestorePlan, error) {
	plan, err := e.PlanToolRestore(ctx, store, snapshotID, workTree, override)
	if err != nil {
		return nil, nil, err
	}
	repo, err := e.Open(ctx, workTree)
	if err != nil {
		return nil, nil, err
	}
	touched, err := repo.ApplyRestore(ctx, plan, override)
	if err != nil {
		return nil, plan, err
	}
	return touched, plan, nil
}

func (e *Engine) enforceSessionQuota(ctx context.Context, store Store, repo *Repo, sessionID string, kind Kind) error {
	capCount := int(e.limits.MaxTurnsPerSession)
	if kind == KindTool {
		capCount = int(e.limits.MaxToolsPerSession)
	}
	if capCount <= 0 {
		return nil
	}
	rows, err := store.ListCheckpoints(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: list checkpoints for quota: %w", err)
	}
	var matching []Record
	for i := range rows {
		if rows[i].SessionID == sessionID && rows[i].Kind == kind {
			matching = append(matching, rows[i])
		}
	}
	if len(matching) <= capCount {
		return nil
	}
	sortRecordsNewestFirst(matching)
	evict := make([]string, 0, len(matching)-capCount)
	for i := capCount; i < len(matching); i++ {
		evict = append(evict, matching[i].ID)
	}
	if err := store.DeleteCheckpoints(ctx, evict); err != nil {
		return fmt.Errorf("snapshot: delete evicted checkpoints: %w", err)
	}
	return dropCheckpointRefs(ctx, repo, evict)
}

func dropCheckpointRefs(ctx context.Context, repo *Repo, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var spec bytes.Buffer
	for _, id := range ids {
		for _, slot := range []string{SlotBefore, SlotAfter} {
			spec.WriteString("delete ")
			spec.WriteString(RefName(id, slot))
			spec.WriteByte(0)
			spec.WriteByte(0)
		}
	}
	return repo.runLocked(ctx, func() error {
		_, err := repo.git(ctx, spec.Bytes(), "update-ref", "--stdin", "-z")
		return err
	})
}

func sortRecordsNewestFirst(records []Record) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID > records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
}
