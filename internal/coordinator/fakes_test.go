package coordinator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

var (
	errAdmLoadBoom   = errors.New("admission load boom")
	errLoadRunBoom   = errors.New("load run boom")
	errAppendObsBoom = errors.New("append obs failure")
)

const (
	testRunID1        = "r1"
	testRunID2        = "r2"
	testSessionID     = "s1"
	testOriginUser    = "user"
	testOriginSystem  = "system"
	testKindAcceptRun = "AcceptRun"
	testKindAdmitRun  = "AdmitRun"
	testKindPrepare   = "PrepareDispatch"
	testKindObserve   = "ObserveModelResponse"
	testPurposeTurn   = "turn"
	testCmdAccept1    = "cmd-accept-1"
	testCmdAcceptR2   = "cmd-accept-r2"
	testOriginWorker  = "worker"
	testAttTool1      = "att-tool-1"
	testAtt1          = "att-1"
	testToolAttempt   = "ta1"
	testTimeout       = 100 * time.Millisecond
)

type appendResultOrError struct {
	result AppendResult
	err    error
	// persist makes the fake store keep the transition although the caller sees
	// an error (a lost acknowledgement of a commit that did happen).
	persist bool
}

type resolveResultOrError struct {
	status  CommitResolution
	receipt codec.Receipt
	err     error
}

type fakeStore struct {
	mu           sync.Mutex
	appendQueue  []appendResultOrError
	resolveQueue []resolveResultOrError
	transitions  []*Transition
	resolves     []string
	loadRunFn    func(ctx context.Context, runID string) (*kernel.State, uint64, error)
	admission    Admission
	admissionErr error
	runs         map[string]*kernel.State
	runRevs      map[string]uint64
	onAppend     func(t *Transition)
	inFlight     int
	maxInFlight  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		mu:           sync.Mutex{},
		appendQueue:  nil,
		resolveQueue: nil,
		transitions:  nil,
		resolves:     nil,
		loadRunFn:    nil,
		admission:    Admission{},
		admissionErr: nil,
		runs:         make(map[string]*kernel.State),
		runRevs:      make(map[string]uint64),
		onAppend:     nil,
	}
}

func (s *fakeStore) queueAppend(res *AppendResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r AppendResult
	if res != nil {
		r = *res
	}
	s.appendQueue = append(s.appendQueue, appendResultOrError{result: r, err: err})
}

// queueLostAck scripts an Append whose acknowledgement is lost. When committed
// is true the store keeps the transition, as a real store would.
func (s *fakeStore) queueLostAck(committed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendQueue = append(s.appendQueue, appendResultOrError{err: ErrCommitUnknown, persist: committed})
}

// persistLocked mirrors what a real store keeps after a commit: the evolved run
// state, the next run revision and the next admission revision.
func (s *fakeStore) persistLocked(t *Transition) {
	st := s.runs[t.RunID]
	for _, ev := range t.Events {
		st = kernel.Evolve(st, ev)
	}
	s.runs[t.RunID] = st
	s.runRevs[t.RunID]++
	s.admission.Revision++
	for _, ev := range t.Events {
		if admitted, ok := ev.(*kernel.RunAdmittedEvent); ok {
			s.persistContextLocked(admitted)
		}
	}
	if st.Admitted && !st.Finished {
		s.admission.ActiveRunID = t.RunID
		s.admission.AdmissionToken = st.AdmissionToken
	}
	if st.Finished && s.admission.ActiveRunID == t.RunID {
		s.admission.ActiveRunID = ""
	}
}

func (s *fakeStore) queueResolve(status CommitResolution, receipt *codec.Receipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rc codec.Receipt
	if receipt != nil {
		rc = *receipt
	}
	s.resolveQueue = append(s.resolveQueue, resolveResultOrError{status: status, receipt: rc, err: nil})
}

func (s *fakeStore) Append(_ context.Context, t *Transition) (AppendResult, error) {
	s.mu.Lock()
	s.transitions = append(s.transitions, t)
	s.inFlight++
	s.maxInFlight = max(s.maxInFlight, s.inFlight)
	onApp := s.onAppend
	scripted := len(s.appendQueue) > 0
	var next appendResultOrError
	if scripted {
		next = s.appendQueue[0]
		s.appendQueue = s.appendQueue[1:]
	}
	s.mu.Unlock()

	if onApp != nil {
		onApp(t)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight--
	if next.err != nil {
		if next.persist {
			s.persistLocked(t)
		}
		return AppendResult{}, next.err
	}
	s.persistLocked(t)
	if scripted {
		return next.result, nil
	}
	return AppendResult{
		RunRevision:       s.runRevs[t.RunID],
		AdmissionRevision: s.admission.Revision,
	}, nil
}

func (s *fakeStore) ResolveCommit(_ context.Context, commandID string) (CommitResolution, codec.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolves = append(s.resolves, commandID)
	if len(s.resolveQueue) > 0 {
		next := s.resolveQueue[0]
		s.resolveQueue = s.resolveQueue[1:]
		return next.status, next.receipt, next.err
	}
	return CommitResolutionUnavailable, codec.Receipt{}, nil
}

func (s *fakeStore) appendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.transitions)
}

func (s *fakeStore) LoadRun(ctx context.Context, runID string) (*kernel.State, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadRunFn != nil {
		return s.loadRunFn(ctx, runID)
	}
	state := s.runs[runID]
	rev := s.runRevs[runID]
	return state, rev, nil
}

func (s *fakeStore) LoadAdmission(_ context.Context, _ string) (Admission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.admission, s.admissionErr
}

type fakeExecutor struct {
	mu       sync.Mutex
	recorded []Work
	reports  map[string]func(*Observation)
	onExec   func(w *Work, report func(*Observation))
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		mu:       sync.Mutex{},
		recorded: nil,
		reports:  make(map[string]func(*Observation)),
		onExec:   nil,
	}
}

func (e *fakeExecutor) Execute(_ context.Context, w *Work, report func(*Observation)) {
	e.mu.Lock()
	var item Work
	if w != nil {
		item = *w
	}
	e.recorded = append(e.recorded, item)
	key := item.Lease.AttemptID
	if key == "" {
		key = item.Lease.RunID
	}
	e.reports[key] = report
	onExec := e.onExec
	e.mu.Unlock()

	if onExec != nil {
		onExec(w, report)
	}
}

func startTestSession(t *testing.T, store *fakeStore, exec *fakeExecutor, seed string, cap int) (context.Context, *Session) {
	t.Helper()
	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic(seed))
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   cap,
		Store:            store,
		Executor:         exec,
		Clock:            fakeClk,
		IDs:              idGen,
	}
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	sess, err := Start(ctx, &cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	return ctx, sess
}

func (e *fakeExecutor) workCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.recorded)
}

func waitSignal(t *testing.T, ctx context.Context, ch <-chan struct{}, desc string) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s: %v", desc, ctx.Err())
	}
}

func waitValue[T any](t *testing.T, ctx context.Context, ch <-chan T, desc string) T {
	t.Helper()
	select {
	case val := <-ch:
		return val
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s: %v", desc, ctx.Err())
		var zero T
		return zero
	}
}

func testNow() time.Time {
	return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
}

func newTestSlots(capacity int) *slots {
	clk := clock.NewFake(testNow())
	return newSlots(capacity, ids.NewUUIDv7(clk, ids.NewDeterministic("test-slots")))
}

func reserveSlot(s *slots) (string, bool) {
	ref, err := s.reserve()
	return ref, err == nil
}

// persistContextLocked mirrors how the admission row takes the context fields of
// a RunAdmitted: the first admission of a session records the start run, and the
// epoch and revision only grow.
func (s *fakeStore) persistContextLocked(ev *kernel.RunAdmittedEvent) {
	if s.admission.ContextEpoch == 0 {
		s.admission.ContextStartRunID = ev.StartRunID
	}
	s.admission.ContextEpoch = max(s.admission.ContextEpoch, ev.ContextEpoch)
	s.admission.ContextRevision = max(s.admission.ContextRevision, ev.InitialContextRevision)
}
