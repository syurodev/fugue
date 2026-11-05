package coordinator

import (
	"context"
	"sync"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

type appendResultOrError struct {
	result AppendResult
	err    error
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

func (s *fakeStore) queueAppend(res AppendResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendQueue = append(s.appendQueue, appendResultOrError{result: res, err: err})
}

func (s *fakeStore) queueResolve(status CommitResolution, receipt codec.Receipt, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolveQueue = append(s.resolveQueue, resolveResultOrError{status: status, receipt: receipt, err: err})
}

func (s *fakeStore) Append(_ context.Context, t *Transition) (AppendResult, error) {
	s.mu.Lock()
	s.transitions = append(s.transitions, t)
	onApp := s.onAppend
	var next appendResultOrError
	if len(s.appendQueue) > 0 {
		next = s.appendQueue[0]
		s.appendQueue = s.appendQueue[1:]
	}
	s.mu.Unlock()

	if onApp != nil {
		onApp(t)
	}

	return next.result, next.err
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
	reports  map[string]func(Observation)
	onExec   func(w Work, report func(Observation))
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		mu:       sync.Mutex{},
		recorded: nil,
		reports:  make(map[string]func(Observation)),
		onExec:   nil,
	}
}

func (e *fakeExecutor) Execute(_ context.Context, w Work, report func(Observation)) {
	e.mu.Lock()
	e.recorded = append(e.recorded, w)
	key := w.Lease.AttemptID
	if key == "" {
		key = w.Lease.RunID
	}
	e.reports[key] = report
	onExec := e.onExec
	e.mu.Unlock()

	if onExec != nil {
		onExec(w, report)
	}
}

func (e *fakeExecutor) workCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.recorded)
}
