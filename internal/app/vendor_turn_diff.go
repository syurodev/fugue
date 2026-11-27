package app

// Per-turn shadow snapshots and the turn_diff item (git-guard §3, VR-14).
// The actor takes one snapshot before a turn's input reaches the vendor and
// one when the turn ends, and records the difference as a turn_diff item.
// Snapshots never block the turn: each is capped, and a failure or a timeout
// records the item as unavailable. Owner: application layer. Allowed callers:
// the vendor session actor and the host composition.

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/snapshot"
	"github.com/syurodev/fugue/internal/transcript"
)

const (
	// TurnSnapshotCap bounds one snapshot (git-guard §3.1, §6).
	TurnSnapshotCap = 10 * time.Second
	// maxTurnDiffFiles bounds the sealed file list of one item; the counts in
	// the metadata still cover every changed file.
	maxTurnDiffFiles = 2000
	turnSnapshotsDir = "snapshots"
)

// ErrTurnSnapshotTimeout reports that a snapshot did not finish within its cap.
var ErrTurnSnapshotTimeout = errcode.New(errcode.DeadlineExceeded, "the turn snapshot exceeded its time cap")

// TurnSnapshot names the opening snapshot of one turn. The ID is the random
// journal ID, never a tree hash (git-guard §3.1).
type TurnSnapshot struct {
	ID      string
	WorkDir string
}

// TurnChanges is the difference between a turn's two snapshots. Skipped counts
// the files the end snapshot left out (excluded, too large, unreadable).
type TurnChanges struct {
	Files   []snapshot.Change
	Skipped int
	// BeforeTree and AfterTree name the two shadow trees the diff came from, so
	// the end-of-turn secret scan can read the added lines before the pair is
	// released. They are tree hashes, never shown to a client.
	BeforeTree, AfterTree string
}

// TurnSnapshotter is the port the vendor session actor uses to snapshot a
// working directory around a turn. The host binds the shadow-git engine.
//
// Warm primes the shadow index of workDir so the first Begin is incremental.
// Release forgets one turn's snapshot pair once its diff is computed, and
// ReleaseSession forgets whatever a session still holds when it ends; neither
// the engine's store nor its refs may grow with the number of turns.
type TurnSnapshotter interface {
	Warm(ctx context.Context, workDir string) error
	Begin(ctx context.Context, sessionID, workDir string) (TurnSnapshot, error)
	End(ctx context.Context, snap TurnSnapshot) (TurnChanges, error)
	Release(ctx context.Context, snap TurnSnapshot) error
	ReleaseSession(ctx context.Context, sessionID, workDir string) error
}

// engineTurnSnapshots implements TurnSnapshotter on the shadow-git engine.
type engineTurnSnapshots struct {
	engine *snapshot.Engine
	store  snapshot.Store
}

// newEngineTurnSnapshots binds the shadow-git engine rooted in the data
// directory. A machine without git still gets a port: every snapshot then
// fails and the items say unavailable.
func newEngineTurnSnapshots(dataDir string, git snapshot.Availability, limits snapshot.Limits, clk clock.Clock, idGen ids.Generator) *engineTurnSnapshots {
	engine := snapshot.NewWithDeps(filepath.Join(dataDir, turnSnapshotsDir), git, limits, clk, idGen)
	return &engineTurnSnapshots{engine: engine, store: snapshot.NewMemoryStore()}
}

func (e *engineTurnSnapshots) Warm(ctx context.Context, workDir string) error {
	if err := e.engine.Warm(ctx, workDir); err != nil {
		return fmt.Errorf("warm the shadow index: %w", err)
	}
	return nil
}

func (e *engineTurnSnapshots) Release(ctx context.Context, snap TurnSnapshot) error {
	if err := e.engine.ReleaseCheckpoints(ctx, e.store, snap.WorkDir, snap.ID); err != nil {
		return fmt.Errorf("release the turn snapshot: %w", err)
	}
	return nil
}

func (e *engineTurnSnapshots) ReleaseSession(ctx context.Context, sessionID, workDir string) error {
	if err := e.engine.ReleaseSession(ctx, e.store, sessionID, workDir); err != nil {
		return fmt.Errorf("release the session snapshots: %w", err)
	}
	return nil
}

func (e *engineTurnSnapshots) Begin(ctx context.Context, sessionID, workDir string) (TurnSnapshot, error) {
	rec, _, err := e.engine.CaptureTurn(ctx, e.store, sessionID, workDir)
	if err != nil {
		return TurnSnapshot{}, fmt.Errorf("capture the turn start snapshot: %w", err)
	}
	return TurnSnapshot{ID: rec.ID, WorkDir: workDir}, nil
}

func (e *engineTurnSnapshots) End(ctx context.Context, snap TurnSnapshot) (TurnChanges, error) {
	rec, end, err := e.engine.CaptureTurnEnd(ctx, e.store, snap.ID, snap.WorkDir)
	if err != nil {
		return TurnChanges{}, fmt.Errorf("capture the turn end snapshot: %w", err)
	}
	repo, err := e.engine.Open(ctx, snap.WorkDir)
	if err != nil {
		return TurnChanges{}, fmt.Errorf("open the shadow repository: %w", err)
	}
	files, err := repo.Diff(ctx, rec.BeforeTree, end.Tree)
	if err != nil {
		return TurnChanges{}, fmt.Errorf("diff the turn snapshots: %w", err)
	}
	return TurnChanges{Files: files, Skipped: len(end.Skipped), BeforeTree: rec.BeforeTree, AfterTree: end.Tree}, nil
}

// capped runs fn under the snapshot cap on the injected clock. On timeout the
// call returns at once and fn's context is cancelled, so a slow git cannot hold
// the turn; fn's goroutine is supervised and ends with its context.
func capped[T any](ctx context.Context, m *VendorSessions, name string, fn func(context.Context) (T, error)) (T, error) {
	type outcome struct {
		value T
		err   error
	}
	done := make(chan outcome, 1)
	task := m.spawn(name, func(taskCtx context.Context) error {
		value, err := fn(taskCtx)
		done <- outcome{value: value, err: err}
		return nil
	})
	timer := m.deps.Clock.NewTimer(TurnSnapshotCap)
	defer timer.Stop()
	select {
	case res := <-done:
		return res.value, res.err
	case <-timer.C():
		task.Stop()
		var zero T
		return zero, ErrTurnSnapshotTimeout
	case <-ctx.Done():
		task.Stop()
		var zero T
		return zero, fmt.Errorf("wait for the turn snapshot: %w", ctx.Err())
	}
}

// beginTurnSnapshot snapshots the working directory before a turn's input is
// sent. A failure yields the zero TurnSnapshot, which the end of the turn
// reports as unavailable; it never fails the turn.
func (a *vendorSessionActor) beginTurnSnapshot(ctx context.Context) TurnSnapshot {
	port := a.manager.deps.TurnSnapshots
	if port == nil {
		return TurnSnapshot{}
	}
	warm := a.warm
	snap, err := capped(ctx, a.manager, "turn-snapshot-begin-"+a.info.ID, func(taskCtx context.Context) (TurnSnapshot, error) {
		// A turn that starts during warm-up waits for it inside the same cap, so
		// the start snapshot never races the priming capture.
		select {
		case <-warm:
		case <-taskCtx.Done():
			return TurnSnapshot{}, fmt.Errorf("wait for the shadow warm-up: %w", taskCtx.Err())
		}
		return port.Begin(taskCtx, a.info.ID, a.cwd)
	})
	if err != nil {
		a.manager.deps.Logger.WarnContext(ctx, "the turn start snapshot failed", "error_code", string(errcode.CodeOf(err)))
		return TurnSnapshot{}
	}
	return snap
}

// recordTurnDiff writes the turn_diff item for turnID, just before the turn's
// end is appended. A turn without a start entry (no port, or a turn that began
// before this process) records nothing.
func (a *vendorSessionActor) recordTurnDiff(ctx context.Context, turnID string) {
	start, tracked := a.snaps[turnID]
	if !tracked {
		return
	}
	delete(a.snaps, turnID)
	metadata := transcript.TurnDiffMetadata{Status: transcript.TurnDiffUnavailable}
	var content transcript.Content
	var scan *turnScanResult
	if start.ID != "" {
		changes, err := capped(ctx, a.manager, "turn-snapshot-end-"+a.info.ID, func(taskCtx context.Context) (TurnChanges, error) {
			return a.manager.deps.TurnSnapshots.End(taskCtx, start)
		})
		if err != nil {
			a.manager.deps.Logger.WarnContext(ctx, "the turn end snapshot failed", "error_code", string(errcode.CodeOf(err)))
		} else {
			metadata, content = turnDiffItemData(&changes)
			scan = a.scanTurnAdditions(ctx, start, &changes)
		}
	}
	// The turn stays open for the other sessions until its end snapshot is done;
	// whatever the snapshot's outcome, its registry entry ends here.
	metadata.Overlapped = a.manager.overlaps.end(a.cwd, a.info.ID, turnID)
	if start.ID != "" {
		a.releaseTurnSnapshot(ctx, start)
	}
	id, err := a.manager.deps.IDs.NewID()
	if err != nil {
		a.manager.deps.Logger.WarnContext(ctx, "the turn diff item could not get an identity")
		return
	}
	item := transcript.Item{ID: id, SessionID: a.info.ID, TurnID: turnID, Kind: transcript.TurnDiff, Metadata: metadata, Content: content}
	if err := a.recordItem(ctx, &item); err != nil {
		a.manager.deps.Logger.WarnContext(ctx, "the turn diff item was not recorded", "error_code", string(errcode.CodeOf(err)))
	}
	a.recordPossibleSecretNotice(ctx, turnID, scan)
}

// turnDiffItemData turns the snapshot difference into the allowlisted counts
// and the sealed file list. Files are capped; whatever is left out, like a
// file the snapshot skipped, makes the item partial (git-guard §3.2).
func turnDiffItemData(changes *TurnChanges) (transcript.TurnDiffMetadata, transcript.Content) {
	metadata := transcript.TurnDiffMetadata{Status: transcript.TurnDiffComplete}
	files := make([]transcript.TurnDiffFile, 0, min(len(changes.Files), maxTurnDiffFiles))
	for i := range changes.Files {
		c := &changes.Files[i]
		change := transcriptChange(c.Kind)
		switch c.Kind {
		case snapshot.ChangeAdded:
			metadata.FilesAdded++
		case snapshot.ChangeDeleted:
			metadata.FilesDeleted++
		case snapshot.ChangeModified:
			metadata.FilesModified++
		}
		metadata.LinesAdded += max(c.Added, 0)
		metadata.LinesRemoved += max(c.Removed, 0)
		if len(files) < maxTurnDiffFiles {
			files = append(files, transcript.TurnDiffFile{Path: c.Path, Change: change, Added: c.Added, Removed: c.Removed})
		}
	}
	skipped := changes.Skipped + len(changes.Files) - len(files)
	if skipped > 0 {
		metadata.Status = transcript.TurnDiffPartial
	}
	if len(files) == 0 && skipped == 0 {
		return metadata, nil
	}
	return metadata, transcript.TurnDiffContent{Files: files, Skipped: skipped}
}

func transcriptChange(kind snapshot.ChangeKind) string {
	switch kind {
	case snapshot.ChangeAdded:
		return "add"
	case snapshot.ChangeDeleted:
		return "delete"
	case snapshot.ChangeModified:
		return "modify"
	default:
		return "modify"
	}
}

// trackTurnSnapshot remembers a turn's opening snapshot until its end. Without
// a port nothing is tracked, and the turn gets no turn_diff item.
func (a *vendorSessionActor) trackTurnSnapshot(turnID string, snap TurnSnapshot) {
	if a.manager.deps.TurnSnapshots != nil {
		a.snaps[turnID] = snap
		a.manager.overlaps.begin(a.cwd, a.info.ID, turnID)
	}
}

// turnSnapshotGit resolves the git binary of the turn snapshots: the host's
// override, else the one found on the machine.
func turnSnapshotGit(ctx context.Context, deps *HostDeps) snapshot.Availability {
	if deps.GitBinary != "" {
		return snapshot.Availability{Path: deps.GitBinary}
	}
	return snapshot.Locate(ctx)
}

// startWarmUp primes the shadow index in the background when the session starts
// or resumes, so the first turn's start snapshot, which must finish before the
// input is sent, is incremental instead of a cold capture of the whole tree.
func (a *vendorSessionActor) startWarmUp() {
	port := a.manager.deps.TurnSnapshots
	if port == nil || a.cwd == "" || a.warm != nil {
		return
	}
	done := make(chan struct{})
	a.warm = done
	workDir, logger := a.cwd, a.manager.deps.Logger
	a.warmTask = a.manager.spawn("turn-snapshot-warm-"+a.info.ID, func(ctx context.Context) error {
		defer close(done)
		if err := port.Warm(ctx, workDir); err != nil {
			logger.WarnContext(ctx, "the shadow warm-up failed", "error_code", string(errcode.CodeOf(err)))
		}
		return nil
	})
}

// stopWarmUp stops an in-flight shadow warm-up and waits for it to exit
// before session teardown releases snapshots or removes its temp directory.
func (a *vendorSessionActor) stopWarmUp() {
	if a.warmTask == nil {
		return
	}
	a.warmTask.Stop()
	<-a.warm
}

// releaseTurnSnapshot forgets a turn's snapshot pair after its diff is computed,
// under the same cap as the snapshots themselves.
func (a *vendorSessionActor) releaseTurnSnapshot(ctx context.Context, start TurnSnapshot) {
	_, err := capped(ctx, a.manager, "turn-snapshot-release-"+a.info.ID, func(taskCtx context.Context) (struct{}, error) {
		return struct{}{}, a.manager.deps.TurnSnapshots.Release(taskCtx, start)
	})
	if err != nil {
		a.manager.deps.Logger.WarnContext(ctx, "the turn snapshot was not released", "error_code", string(errcode.CodeOf(err)))
	}
}

// releaseSessionSnapshots forgets whatever the session still holds when its
// actor ends: a turn that never reached its end, or a failed release.
func (a *vendorSessionActor) releaseSessionSnapshots(ctx context.Context) {
	port := a.manager.deps.TurnSnapshots
	if port == nil || a.cwd == "" {
		return
	}
	a.manager.overlaps.endSession(a.info.ID)
	_, err := capped(context.WithoutCancel(ctx), a.manager, "turn-snapshot-release-session-"+a.info.ID, func(taskCtx context.Context) (struct{}, error) {
		return struct{}{}, port.ReleaseSession(taskCtx, a.info.ID, a.cwd)
	})
	if err != nil {
		a.manager.deps.Logger.WarnContext(ctx, "the session snapshots were not released", "error_code", string(errcode.CodeOf(err)))
	}
}
