package coordinator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

const defaultOriginSession = "session"

// Config configures a per-session coordinator actor.
type Config struct {
	SessionID        string
	OwnerIncarnation uint64
	WorkerCapacity   int
	Store            Store
	Executor         Executor
	Clock            clock.Clock
	IDs              ids.Generator
}

// Request defines a command and caller context for the session actor.
type Request struct {
	Command        kernel.Command
	Facts          kernel.DecisionFacts
	Kind           string
	Principal      string
	Target         string
	MetadataInput  []byte
	ProtectedInput []byte
}

// Reply reports the outcome of a submitted command.
type Reply struct {
	Events    []kernel.Event
	Duplicate bool
	Receipt   codec.Receipt
}

type submitReply struct {
	reply Reply
	err   error
}

type submission struct {
	req     Request
	obs     *Observation
	replyCh chan submitReply
}

// Session is the per-session coordinator actor.
type Session struct {
	mu             sync.Mutex
	cfg            Config
	admission      Admission
	inheritedRunID string
	slots          *slots
	runs           map[string]*kernel.State
	runRevs        map[string]uint64
	queue          *sessionQueue
	mailbox        chan submission
	cancel         context.CancelFunc
	task           *supervise.Task
	workerTasks    []*supervise.Task
	stopped        chan struct{}
	stopOnce       sync.Once

	parked       []submission
	halted       bool
	attemptSlots map[attemptKey]string

	onPark       func()
	onResolve    func(time.Time)
	onTimerArmed func()
	onCommitted  func()
}

// Start loads the admission row, validates ownership, and starts the actor goroutine.
func Start(ctx context.Context, cfg *Config) (*Session, error) {
	if cfg == nil {
		return nil, errcode.New(errcode.InvalidArgument, "config is nil")
	}
	c := *cfg
	if c.WorkerCapacity <= 0 {
		c.WorkerCapacity = 1
	}

	adm, err := c.Store.LoadAdmission(ctx, c.SessionID)
	if err != nil {
		return nil, fmt.Errorf("load admission: %w", err)
	}

	var inherited string
	if adm.ActiveRunID != "" && adm.OwnerIncarnation != c.OwnerIncarnation {
		inherited = adm.ActiveRunID
	}

	actorCtx, cancel := context.WithCancel(ctx)
	s := newSessionInstance(&c, adm, inherited, cancel)
	s.startWorkers(actorCtx)
	s.task = supervise.Go(actorCtx, "coordinator.session", s.loop)
	return s, nil
}

func newSessionInstance(cfg *Config, adm Admission, inherited string, cancel context.CancelFunc) *Session {
	return &Session{
		mu:             sync.Mutex{},
		cfg:            *cfg,
		admission:      adm,
		inheritedRunID: inherited,
		slots:          newSlots(cfg.WorkerCapacity),
		runs:           make(map[string]*kernel.State),
		runRevs:        make(map[string]uint64),
		queue:          newSessionQueue(),
		mailbox:        make(chan submission),
		cancel:         cancel,
		task:           nil,
		workerTasks:    make([]*supervise.Task, 0, cfg.WorkerCapacity),
		stopped:        make(chan struct{}),
		stopOnce:       sync.Once{},
		parked:         nil,
		halted:         false,
		attemptSlots:   make(map[attemptKey]string),
		onPark:         nil,
		onResolve:      nil,
		onTimerArmed:   nil,
		onCommitted:    nil,
	}
}

func (s *Session) startWorkers(actorCtx context.Context) {
	for range s.cfg.WorkerCapacity {
		workerTask := supervise.Go(actorCtx, "coordinator.worker", func(wCtx context.Context) error {
			for {
				select {
				case <-wCtx.Done():
					return nil
				case w, ok := <-s.slots.handoffCh:
					if !ok {
						return nil
					}
					s.cfg.Executor.Execute(wCtx, w, s.report)
				}
			}
		})
		s.workerTasks = append(s.workerTasks, workerTask)
	}
}

// Submit serializes a request through the session actor.
func (s *Session) Submit(ctx context.Context, r *Request) (Reply, error) {
	if r == nil {
		return Reply{}, errcode.New(errcode.InvalidArgument, "request is nil")
	}
	return s.enqueue(ctx, &submission{
		req:     *r,
		obs:     nil,
		replyCh: make(chan submitReply, 1),
	})
}

func (s *Session) enqueue(ctx context.Context, sub *submission) (Reply, error) {
	select {
	case <-s.stopped:
		return Reply{}, ErrSessionStopped
	default:
	}

	select {
	case s.mailbox <- *sub:
	case <-ctx.Done():
		return Reply{}, fmt.Errorf("submit send: %w", ctx.Err())
	case <-s.stopped:
		return Reply{}, ErrSessionStopped
	}

	select {
	case res := <-sub.replyCh:
		return res.reply, res.err
	case <-ctx.Done():
		return Reply{}, fmt.Errorf("submit wait: %w", ctx.Err())
	case <-s.stopped:
		select {
		case res := <-sub.replyCh:
			return res.reply, res.err
		default:
			return Reply{}, ErrSessionStopped
		}
	}
}

// Stop terminates the session actor and waits for it to finish.
func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopped)
		s.cancel()
		if s.task != nil {
			s.task.Stop()
			<-s.task.Done()
		}
		for _, wt := range s.workerTasks {
			wt.Stop()
			<-wt.Done()
		}
	})
}

func (s *Session) report(obs *Observation) {
	if obs == nil {
		return
	}
	sub := submission{
		req:     Request{},
		obs:     obs,
		replyCh: nil,
	}
	select {
	case s.mailbox <- sub:
	case <-s.stopped:
	}
}

func (s *Session) loop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case sub := <-s.mailbox:
			s.handleSubmission(ctx, &sub)
		}
	}
}

func (s *Session) respond(sub *submission, reply *Reply, err error) {
	if sub.replyCh != nil {
		r := Reply{}
		if reply != nil {
			r = *reply
		}
		sub.replyCh <- submitReply{reply: r, err: err}
	}
}

func (s *Session) ensureRunLoaded(ctx context.Context, runID string) (*kernel.State, error) {
	s.mu.Lock()
	state, exists := s.runs[runID]
	if exists {
		s.mu.Unlock()
		return state, nil
	}
	s.mu.Unlock()

	st, rev, err := s.cfg.Store.LoadRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load run: %w", err)
	}
	if st == nil {
		st = kernel.NewState()
	}

	s.mu.Lock()
	s.runs[runID] = st
	s.runRevs[runID] = rev
	s.mu.Unlock()

	return st, nil
}

func (s *Session) handleSubmission(ctx context.Context, sub *submission) {
	if sub.obs != nil {
		s.handleObservation(ctx, sub, sub.obs)
		return
	}

	cmdID, runID, origin, cmdToken := commandMetadata(sub.req.Command)
	if runID == "" {
		runID = sub.req.Target
	}

	if runID != "" && runID == s.inheritedRunID {
		s.respond(sub, nil, ErrRecoveryRequired)
		return
	}

	state, err := s.ensureRunLoaded(ctx, runID)
	if err != nil {
		s.respond(sub, nil, err)
		return
	}

	if prep, ok := sub.req.Command.(*kernel.PrepareDispatchCommand); ok && isDispatch(state, sub.req.Command) {
		s.handleDispatch(ctx, sub, prep)
		return
	}

	cmdID, origin, _, prepErr := s.resolveSubmissionFacts(runID, cmdID, origin, cmdToken, state, sub)
	if prepErr != nil {
		s.respond(sub, nil, prepErr)
		return
	}

	events, decErr := kernel.Decide(state, sub.req.Command, &sub.req.Facts)
	if decErr != nil {
		s.respond(sub, nil, decErr)
		return
	}

	if events == nil {
		s.respond(sub, &Reply{
			Events:    nil,
			Duplicate: true,
			Receipt: codec.Receipt{
				CommandID: cmdID,
				Kind:      sub.req.Kind,
				Principal: origin,
				Target:    runID,
			},
		}, nil)
		return
	}

	t := s.buildTransition(runID, cmdID, origin, state, events, &sub.req)
	appRes, appErr := s.cfg.Store.Append(ctx, t)
	if appErr != nil {
		s.respond(sub, nil, appErr)
		return
	}

	s.applySubmissionSuccess(ctx, runID, events, &appRes, sub)
}

func (s *Session) resolveSubmissionFacts(
	runID, cmdID, origin string,
	cmdToken uint64,
	state *kernel.State,
	sub *submission,
) (string, string, uint64, error) {
	var err error
	if cmdID == "" {
		cmdID, err = s.cfg.IDs.NewID()
		if err != nil {
			return "", "", 0, fmt.Errorf("new id: %w", err)
		}
	}
	if origin == "" {
		origin = sub.req.Principal
		if origin == "" {
			origin = defaultOriginSession
		}
	}

	if acceptCmd, ok := sub.req.Command.(*kernel.AcceptRunCommand); ok {
		s.fillAcceptDefaults(acceptCmd, runID)
	}

	var token uint64
	if state.Admitted {
		token = s.admission.AdmissionToken
	} else {
		token = cmdToken
	}
	setCommandMetadata(sub.req.Command, cmdID, runID, origin, token)

	sub.req.Facts.Envelope = kernel.Envelope{
		RunID:       runID,
		Sequence:    state.LastSequence + 1,
		CommandID:   cmdID,
		CausationID: cmdID,
		Origin:      origin,
		Token:       token,
		Timestamp:   s.cfg.Clock.Now(),
	}
	sub.req.Facts.OwnerIncarnation = s.cfg.OwnerIncarnation

	return cmdID, origin, token, nil
}

func (s *Session) fillAcceptDefaults(acceptCmd *kernel.AcceptRunCommand, runID string) {
	if acceptCmd.SessionID == "" {
		acceptCmd.SessionID = s.cfg.SessionID
	}
	if acceptCmd.InputRef == "" {
		acceptCmd.InputRef = "input-" + runID
	}
	if acceptCmd.QueueItemRef == "" {
		acceptCmd.QueueItemRef = "queue-" + runID
	}
	if acceptCmd.ProtocolBinding == "" {
		acceptCmd.ProtocolBinding = "binding-v1"
	}
}

func (s *Session) applySubmissionSuccess(
	ctx context.Context,
	runID string,
	events []kernel.Event,
	appRes *AppendResult,
	sub *submission,
) {
	s.mu.Lock()
	for _, ev := range events {
		s.runs[runID] = kernel.Evolve(s.runs[runID], ev)
	}
	s.runRevs[runID] = appRes.RunRevision
	if appRes.AdmissionRevision > 0 {
		s.admission.Revision = appRes.AdmissionRevision
	}

	if s.runs[runID] != nil && s.runs[runID].Accepted && !s.runs[runID].Admitted {
		s.queue.push(runID, &sub.req.Facts)
	}

	if s.runs[runID] != nil && s.runs[runID].Finished {
		if s.admission.ActiveRunID == runID {
			s.admission.ActiveRunID = ""
		}
	}
	s.mu.Unlock()

	s.maybeAdmitNext(ctx)

	s.respond(sub, &Reply{
		Events:    events,
		Duplicate: appRes.Duplicate,
		Receipt:   appRes.Receipt,
	}, nil)

	s.notifyCommitted()
}

func (s *Session) setOnCommitted(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onCommitted = fn
}

func (s *Session) notifyCommitted() {
	s.mu.Lock()
	fn := s.onCommitted
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}
