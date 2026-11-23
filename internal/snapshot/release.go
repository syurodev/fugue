package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// ReleaseCheckpoints forgets the given snapshots: their records leave store and
// their refs leave the shadow repository, so the trees become collectable. A
// caller that has finished reading a snapshot pair (a turn diff) releases it so
// neither the store nor the refs grow with the number of turns.
func (e *Engine) ReleaseCheckpoints(ctx context.Context, store Store, workTree string, snapshotIDs ...string) error {
	if store == nil {
		return errcode.New(errcode.InvalidArgument, "snapshot: store is nil")
	}
	if len(snapshotIDs) == 0 {
		return nil
	}
	repo, err := e.Open(ctx, workTree)
	if err != nil {
		return err
	}
	if err := store.DeleteCheckpoints(ctx, snapshotIDs); err != nil {
		return fmt.Errorf("snapshot: release checkpoints: %w", err)
	}
	return dropCheckpointRefs(ctx, repo, snapshotIDs)
}

// ReleaseSession releases every snapshot a session still holds in store.
func (e *Engine) ReleaseSession(ctx context.Context, store Store, sessionID, workTree string) error {
	if store == nil {
		return errcode.New(errcode.InvalidArgument, "snapshot: store is nil")
	}
	rows, err := store.ListCheckpoints(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: list checkpoints to release: %w", err)
	}
	var held []string
	for i := range rows {
		if rows[i].SessionID == sessionID {
			held = append(held, rows[i].ID)
		}
	}
	return e.ReleaseCheckpoints(ctx, store, workTree, held...)
}

// Warm captures workTree once and drops the capture, leaving the shadow index
// primed so the next Snapshot hashes only files that changed since.
func (e *Engine) Warm(ctx context.Context, workTree string) error {
	repo, err := e.Open(ctx, workTree)
	if err != nil {
		return err
	}
	id, err := e.ids.NewID()
	if err != nil {
		return fmt.Errorf("snapshot: generate warm-up id: %w", err)
	}
	if _, err := repo.Snapshot(ctx, id, SlotBefore); err != nil {
		return err
	}
	return dropCheckpointRefs(ctx, repo, []string{id})
}

// staleLockFiles are the lock files a killed git leaves behind. Only index.lock
// and packed-refs.lock are touched: the first blocks every later snapshot, the
// second blocks ref deletion.
func staleLockFiles() []string { return []string{"index.lock", "packed-refs.lock"} }

// clearStaleLocks removes lock files left by a git process that was killed when
// its context ended. It runs under the repo gate, which admits one git command
// at a time in this process, and the host holds the data directory exclusively,
// so no live git owns these files.
func (r *Repo) clearStaleLocks() error {
	root, err := os.OpenRoot(r.gitDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("snapshot: open shadow root: %w", err)
	}
	var firstErr error
	for _, name := range staleLockFiles() {
		if rmErr := root.Remove(name); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) && firstErr == nil {
			firstErr = fmt.Errorf("snapshot: remove stale %s: %w", name, rmErr)
		}
	}
	if closeErr := root.Close(); closeErr != nil && firstErr == nil {
		firstErr = fmt.Errorf("snapshot: close shadow root: %w", closeErr)
	}
	return firstErr
}
