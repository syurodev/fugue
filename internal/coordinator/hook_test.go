package coordinator

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

const testReadBackDispatch = "cmd-disp-hook-1"

// hookRecorder collects OnCommitted deliveries. The hook runs on the session
// actor, so every access from the test goroutine is guarded.
type hookRecorder struct {
	mu      sync.Mutex
	entries []hookEntry
	wake    chan struct{}
}

type hookEntry struct {
	runID       string
	lastSeq     uint64
	accepted    bool
	admitted    bool
	invocations int
}

func newHookRecorder() *hookRecorder {
	return &hookRecorder{mu: sync.Mutex{}, entries: nil, wake: make(chan struct{}, 64)}
}

func (h *hookRecorder) onCommitted(runID string, state *kernel.State) {
	entry := hookEntry{runID: runID, lastSeq: 0, accepted: false, admitted: false, invocations: 0}
	if state != nil {
		entry.lastSeq = state.LastSequence
		entry.accepted = state.Accepted
		entry.admitted = state.Admitted
		entry.invocations = len(state.ModelInvocations)
	}
	h.mu.Lock()
	h.entries = append(h.entries, entry)
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *hookRecorder) snapshot() []hookEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.entries)
}

func (h *hookRecorder) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

// waitCount fails unless n deliveries arrived.
func (h *hookRecorder) waitCount(t *testing.T, ctx context.Context, n int) {
	t.Helper()
	for h.count() < n {
		select {
		case <-h.wake:
		case <-ctx.Done():
			t.Fatalf("timed out with %d hook deliveries, want %d", h.count(), n)
		}
	}
}

func startHookSession(t *testing.T, store *fakeStore, executor *hookExecutor, onCommitted func(string, *kernel.State)) (*Session, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	sess, err := Start(ctx, &Config{
		SessionID: testSessionID, OwnerIncarnation: 1, WorkerCapacity: 1,
		Store: store, Executor: executor, Clock: clock.NewFake(testNow()),
		IDs: ids.NewUUIDv7(clock.NewFake(testNow()), ids.NewDeterministic("hook")), OnCommitted: onCommitted,
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(sess.Stop)
	return sess, ctx
}

// hookExecutor hands each work item to the test, which decides whether the
// executor reports a terminal observation or abandons it.
type hookExecutor struct {
	mu      sync.Mutex
	works   []*Work
	reports []func(*Observation)
	sig     chan struct{}
}

func newHookExecutor() *hookExecutor {
	return &hookExecutor{mu: sync.Mutex{}, works: nil, reports: nil, sig: make(chan struct{}, 8)}
}

func (e *hookExecutor) Execute(_ context.Context, w *Work, report func(*Observation)) {
	e.mu.Lock()
	e.works = append(e.works, w)
	e.reports = append(e.reports, report)
	e.mu.Unlock()
	e.sig <- struct{}{}
}

func (e *hookExecutor) waitWork(t *testing.T, ctx context.Context) {
	t.Helper()
	for {
		e.mu.Lock()
		got := len(e.works)
		e.mu.Unlock()
		if got >= 1 {
			return
		}
		select {
		case <-e.sig:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for work (have %d)", got)
		}
	}
}

func (e *hookExecutor) reportResponse(index int) {
	e.mu.Lock()
	work := e.works[index]
	report := e.reports[index]
	e.mu.Unlock()
	obs := makeModelResponseObservation(
		work.Lease.SessionID, work.Lease.RunID, work.Lease.InvocationID, work.Lease.AttemptID,
		"cmd-obs-"+work.Lease.AttemptID, work.Lease.AdmissionToken, work.Lease.OwnerIncarnation,
		work.Lease.ExecutorFence,
	)
	report(&obs)
}

func TestOnCommittedSeesEveryAppliedCommit(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	hooks := newHookRecorder()
	executor := newHookExecutor()
	sess, ctx := startHookSession(t, store, executor, hooks.onCommitted)

	// Accept and the automatic admission each apply one commit. The admission
	// notification happens inside the accept's completion, so it arrives first,
	// and the accept's own delivery carries the actor's post-admission state.
	if _, err := sess.Submit(ctx, reqPtr(acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1"))); err != nil {
		t.Fatalf("submit accept: %v", err)
	}
	hooks.waitCount(t, ctx, 2)

	// The observation lease is validated against the admission row's owner, so
	// the row must carry this incarnation, as a real admission would.
	store.mu.Lock()
	store.admission.OwnerIncarnation = 1
	store.mu.Unlock()

	// The dispatch loses its acknowledgement; the read-back proves the commit
	// and its completion notifies like any other applied commit.
	store.queueLostAck(true)
	store.queueResolve(CommitResolutionCommitted, &codec.Receipt{
		CommandID: testReadBackDispatch, Kind: testKindPrepare, Target: testRunID1,
	})
	if _, err := sess.Submit(ctx, reqPtr(makeModelDispatchRequest(testRunID1, "m1", "a1", testReadBackDispatch))); err != nil {
		t.Fatalf("submit dispatch: %v", err)
	}
	hooks.waitCount(t, ctx, 3)
	executor.waitWork(t, ctx)

	// The terminal observation is the last applied commit of the run.
	executor.reportResponse(0)
	hooks.waitCount(t, ctx, 4)

	entries := hooks.snapshot()
	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(a, b hookEntry) int { return cmp.Compare(a.lastSeq, b.lastSeq) })
	wantSequences := []uint64{4, 4, 5, 9}
	for i, want := range wantSequences {
		if sorted[i].runID != testRunID1 {
			t.Fatalf("hook delivered run %q, want %q", sorted[i].runID, testRunID1)
		}
		if sorted[i].lastSeq != want {
			t.Fatalf("hook delivery %d has LastSequence %d, want %d (all: %v)", i, sorted[i].lastSeq, want, entries)
		}
	}
	for i := range 2 {
		if !sorted[i].accepted || !sorted[i].admitted {
			t.Fatalf("delivery %d is not the admitted run: %v", i, sorted[i])
		}
	}
	if sorted[3].invocations != 1 {
		t.Fatalf("sequence 9 snapshot lost the invocation: %v", sorted[3])
	}
}

func TestOnCommittedIsNotCalledForRejections(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	hooks := newHookRecorder()
	sess, ctx := startHookSession(t, store, newHookExecutor(), hooks.onCommitted)

	rejected := acceptRunRequest(testRunID2, testCmdAcceptR2, "m2", "a2")
	rejected.QueueDecisionRef = ""
	if _, err := sess.Submit(ctx, reqPtr(rejected)); !errors.Is(err, ErrInvalidAccept) {
		t.Fatalf("accept without a queue decision must be rejected, got %v", err)
	}

	disp := makeModelDispatchRequest(testRunID1, "m1", "unknown-attempt", "cmd-disp-reject")
	if _, err := sess.Submit(ctx, reqPtr(disp)); err == nil {
		t.Fatalf("dispatch of an unknown attempt must be rejected")
	}

	if got := hooks.count(); got != 0 {
		t.Fatalf("hook called %d times for rejected commands, want 0", got)
	}
}

func TestNilOnCommittedIsAllowed(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	sess, ctx := startHookSession(t, store, newHookExecutor(), nil)

	if _, err := sess.Submit(ctx, reqPtr(acceptRunRequest(testRunID1, testCmdAccept1, "m1", testAtt1))); err != nil {
		t.Fatalf("submit accept without a hook: %v", err)
	}
}

func TestOnCommittedSnapshotIsImmutable(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	executor := newHookExecutor()
	hooks := newHookRecorder()
	captured := make(chan *kernel.State, 1)
	sess, ctx := startHookSession(t, store, executor, func(runID string, state *kernel.State) {
		hooks.onCommitted(runID, state)
		// The first delivery is the admission; it is the earliest snapshot the
		// accept's completion publishes (its own delivery carries this state).
		select {
		case captured <- state:
		default:
		}
	})

	if _, err := sess.Submit(ctx, reqPtr(acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1"))); err != nil {
		t.Fatalf("submit accept: %v", err)
	}

	var snapshot *kernel.State
	select {
	case snapshot = <-captured:
	case <-ctx.Done():
		t.Fatalf("no snapshot delivered")
	}

	// The reader keeps walking the snapshot until both later commits happened,
	// so its reads genuinely overlap the session's writes.
	stop := make(chan struct{})
	corrupt := make(chan bool, 1)
	reader := supervise.Go(ctx, "test.snapshot-reader", func(_ context.Context) error {
		corrupt <- walkSnapshotUntil(stop, snapshot)
		return nil
	})

	// Commit more work while the reader walks the snapshot: the dispatch and its
	// terminal observation replace the session state twice.
	store.mu.Lock()
	store.admission.OwnerIncarnation = 1
	store.mu.Unlock()
	if _, err := sess.Submit(ctx, reqPtr(makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-imm"))); err != nil {
		t.Fatalf("submit dispatch: %v", err)
	}
	executor.waitWork(t, ctx)
	executor.reportResponse(0)
	hooks.waitCount(t, ctx, 4)

	close(stop)
	select {
	case wasCorrupt := <-corrupt:
		if wasCorrupt {
			t.Fatalf("a later commit changed a published snapshot")
		}
	case <-ctx.Done():
		t.Fatalf("reader did not finish")
	}
	if err := reader.Wait(); err != nil {
		t.Fatalf("reader task failed: %v", err)
	}
}

func TestAbandonedWorkFreesTheSlot(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	executor := newHookExecutor()
	sess, ctx := startHookSession(t, store, executor, nil)

	// The executor takes the work and returns without reporting anything.
	if _, err := sess.Submit(ctx, reqPtr(makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-abandon"))); err != nil {
		t.Fatalf("submit dispatch: %v", err)
	}
	executor.waitWork(t, ctx)

	// The attempt stays Requesting, so the kernel rejects a second dispatch of
	// the same attempt - but only after a worker slot was reserved. With the
	// slot still held by the abandoned handoff this submit would park until the
	// context ends; the kernel rejection proves the slot was freed.
	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-after-abandon")
	_, err := sess.Submit(ctx, reqPtr(req))
	var rej kernel.Rejection
	if !errors.As(err, &rej) {
		t.Fatalf("second dispatch must be rejected by the kernel, got %v", err)
	}

	executor.waitWork(t, ctx)
	executor.mu.Lock()
	handoffs := len(executor.works)
	executor.mu.Unlock()
	if handoffs != 1 {
		t.Fatalf("executor received %d work items, want 1", handoffs)
	}
}

// walkSnapshotUntil reads the snapshot in a tight loop until stop closes,
// reporting whether the snapshot ever changed under the reader.
func walkSnapshotUntil(stop <-chan struct{}, snapshot *kernel.State) bool {
	for {
		select {
		case <-stop:
			return false
		default:
		}
		if snapshotChanged(snapshot) {
			return true
		}
	}
}

// snapshotChanged reports whether the snapshot stopped being the admitted run
// the accept's completion published.
func snapshotChanged(snapshot *kernel.State) bool {
	return snapshot.LastSequence != 4 || !snapshot.Accepted || !snapshot.Admitted || len(snapshot.ModelInvocations) != 1
}

// reqPtr hands Submit a request the test built as a value.
func reqPtr[T any](r T) *T { return &r }

// TestRejectedTerminalObservationFreesTheSlot pins the invariant that every
// dispatched slot is freed exactly once: a terminal observation the kernel
// rejects commits nothing, and the served message still reclaims the slot.
func TestRejectedTerminalObservationFreesTheSlot(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	executor := newHookExecutor()
	sess, ctx := startHookSession(t, store, executor, nil)

	if _, err := sess.Submit(ctx, reqPtr(makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-rejected"))); err != nil {
		t.Fatalf("submit dispatch: %v", err)
	}
	executor.waitWork(t, ctx)

	// The executor reports a response whose usage names another attempt: the
	// kernel rejects it, nothing is appended, and the slot comes back.
	executor.mu.Lock()
	work := executor.works[0]
	report := executor.reports[0]
	executor.mu.Unlock()
	obs := makeModelResponseObservation(
		work.Lease.SessionID, work.Lease.RunID, work.Lease.InvocationID, work.Lease.AttemptID,
		"cmd-obs-rejected", work.Lease.AdmissionToken, work.Lease.OwnerIncarnation, work.Lease.ExecutorFence,
	)
	obs.Facts.Usage.AttemptID = "another-attempt"
	report(&obs)

	// The slot was reclaimed: a second dispatch reserves successfully and is
	// rejected by the kernel instead of parking until the context ends.
	again := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-after-rejection")
	if _, err := sess.Submit(ctx, reqPtr(again)); err == nil {
		t.Fatalf("a second dispatch of a requesting attempt must be rejected")
	}
	if store.appendCount() != 1 {
		t.Fatalf("%d transitions committed, want 1", store.appendCount())
	}
}
