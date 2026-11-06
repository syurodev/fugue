package coordinator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
	// IDs makes command IDs and worker reservation refs; a reservation ref is
	// recorded and must never repeat, so the generator must not either.
	IDs ids.Generator
	// OnCommitted is called on the actor after every applied commit — a
	// submission, an admission, a dispatch or a worker observation, including
	// one settled by a read-back — with the run's snapshot. The snapshot is the
	// actor's state at delivery, so a commit whose completion ran a follow-up
	// commit (an accept admitted immediately) delivers the later state. State is
	// never mutated after publication, but the hook must not block and must not
	// call back into the session synchronously.
	OnCommitted func(runID string, state *kernel.State)
}

// Request defines a command and caller context for the session actor.
//
// Origin is the envelope origin and the only proof of who is asking: only the
// host layer (app or hostapi) sets it, from the authenticated caller. The
// coordinator overwrites the command's own Origin with it and never reads the
// command's claim; an empty Origin is refused. Principal is audit metadata for
// the receipt and defaults to Origin.
//
// An AcceptRun also needs QueueDecisionRef, the evidence reference of the host's
// queue decision, and Facts.ModelPlan, the first step of the run: the
// coordinator records both when it admits the run and invents neither.
type Request struct {
	Command          kernel.Command
	Facts            kernel.DecisionFacts
	Origin           string
	QueueDecisionRef string
	Kind             string
	Principal        string
	Target           string
	MetadataInput    []byte
	ProtectedInput   []byte
	Payloads         []Payload
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

// submission is one mailbox message: a caller request, a worker observation,
// the result of a background read-back, or a worker whose executor abandoned
// its work without a terminal observation.
type submission struct {
	req       Request
	obs       *Observation
	resolved  *resolution
	abandoned *Lease
	replyCh   chan submitReply
}

// Session is the per-session coordinator actor. Everything below the channels
// is owned by the actor goroutine alone and is never locked: other goroutines
// reach it only by posting to the mailbox.
type Session struct {
	cfg            Config
	admission      Admission
	admissionStale bool
	inheritedRunID string
	slots          *slots
	runs           map[string]*kernel.State
	runRevs        map[string]uint64
	queue          *sessionQueue
	parked         []parkedDispatch
	unresolved     map[string]*step
	attemptSlots   map[attemptKey]string

	mailbox     chan submission
	cancel      context.CancelFunc
	task        *supervise.Task
	workerTasks []*supervise.Task
	resolvers   sync.WaitGroup
	stopped     chan struct{}
	stopOnce    sync.Once
	scheduler   atomic.Pointer[Scheduler]

	// Test hooks. They are set before the first command is posted and read by
	// the actor or a resolver started after that, so the mailbox orders them.
	onPark       func()
	onResolve    func(time.Time)
	onTimerArmed func()
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
	s := newSessionInstance(&c, &adm, inherited, cancel)
	s.startWorkers(actorCtx)
	s.task = supervise.Go(actorCtx, "coordinator.session", s.loop)
	return s, nil
}

func newSessionInstance(cfg *Config, adm *Admission, inherited string, cancel context.CancelFunc) *Session {
	s := &Session{
		cfg:            *cfg,
		admission:      *adm,
		admissionStale: false,
		inheritedRunID: inherited,
		slots:          newSlots(cfg.WorkerCapacity, cfg.IDs),
		runs:           make(map[string]*kernel.State),
		runRevs:        make(map[string]uint64),
		queue:          newSessionQueue(),
		parked:         nil,
		unresolved:     make(map[string]*step),
		attemptSlots:   make(map[attemptKey]string),
		mailbox:        make(chan submission),
		cancel:         cancel,
		task:           nil,
		workerTasks:    make([]*supervise.Task, 0, cfg.WorkerCapacity),
		resolvers:      sync.WaitGroup{},
		stopped:        make(chan struct{}),
		stopOnce:       sync.Once{},
		onPark:         nil,
		onResolve:      nil,
		onTimerArmed:   nil,
	}
	return s
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
					// Report is expected from the Execute call stack; the
					// atomic guards the ordering against a late report.
					terminal := &atomic.Bool{}
					s.cfg.Executor.Execute(wCtx, w, func(obs *Observation) {
						if obs != nil && isTerminalObservation(obs.Command) {
							terminal.Store(true)
						}
						s.report(obs)
					})
					if !terminal.Load() {
						s.abandon(w.Lease)
					}
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
		req:       *r,
		obs:       nil,
		resolved:  nil,
		abandoned: nil,
		replyCh:   make(chan submitReply, 1),
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

// Stop terminates the session actor and waits for it, its workers and its
// resolvers to finish.
func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopped)
		s.cancel()
		s.task.Stop()
		<-s.task.Done()
		for _, wt := range s.workerTasks {
			wt.Stop()
			<-wt.Done()
		}
		s.resolvers.Wait()
	})
}

func (s *Session) report(obs *Observation) {
	if obs == nil {
		return
	}
	sub := submission{
		req:       Request{},
		obs:       obs,
		resolved:  nil,
		abandoned: nil,
		replyCh:   nil,
	}
	select {
	case s.mailbox <- sub:
	case <-s.stopped:
	}
}

// abandon tells the actor that the executor returned from its Execute call
// without a terminal observation, so no commit will ever complete this
// attempt's slot. The error itself, with its one log line, is the executor's
// business; the coordinator only reclaims the capacity.
func (s *Session) abandon(lease Lease) {
	sub := submission{
		req:       Request{},
		obs:       nil,
		resolved:  nil,
		abandoned: &lease,
		replyCh:   nil,
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
	if state, exists := s.runs[runID]; exists {
		if _, fresh := s.runRevs[runID]; fresh {
			return state, nil
		}
	}
	if err := s.loadRun(ctx, runID); err != nil {
		return nil, err
	}
	return s.runs[runID], nil
}

func (s *Session) handleSubmission(ctx context.Context, sub *submission) {
	if sub.resolved != nil {
		s.handleResolution(ctx, sub.resolved)
		return
	}
	if sub.abandoned != nil {
		s.handleAbandoned(ctx, sub.abandoned)
		return
	}
	if sub.obs != nil {
		s.handleObservation(ctx, sub, sub.obs)
		return
	}
	s.handleRequest(ctx, sub)
}

// handleAbandoned reclaims the slot of an attempt whose executor abandoned it.
// The attempt keeps whatever state its dispatch committed — no events are
// appended, nothing retries — so only the capacity comes back.
func (s *Session) handleAbandoned(ctx context.Context, lease *Lease) {
	s.completeSlot(ctx, attemptKey{runID: lease.RunID, invocationID: lease.InvocationID, attemptID: lease.AttemptID})
	s.retryParked(ctx)
}

func (s *Session) handleRequest(ctx context.Context, sub *submission) {
	runID, state, err := s.prepareRequest(ctx, sub)
	if err != nil {
		s.respond(sub, nil, err)
		return
	}

	if prep, ok := sub.req.Command.(*kernel.PrepareDispatchCommand); ok && isDispatch(state, sub.req.Command) {
		s.handleDispatch(ctx, sub, prep)
		return
	}

	if accept, ok := sub.req.Command.(*kernel.AcceptRunCommand); ok {
		if acceptErr := s.validateAccept(accept, &sub.req); acceptErr != nil {
			s.respond(sub, nil, acceptErr)
			return
		}
	}
	s.decideAndCommit(ctx, sub, runID, state)
}

// prepareRequest runs the checks that need no decision: a trusted origin, a run
// this incarnation may touch, no unresolved commit on it, and a fresh view of
// the admission row and the run.
func (s *Session) prepareRequest(ctx context.Context, sub *submission) (string, *kernel.State, error) {
	if sub.req.Origin == "" {
		return "", nil, ErrOriginRequired
	}
	_, runID, _ := commandIdentity(sub.req.Command)
	if runID == "" {
		runID = sub.req.Target
	}
	if runID != "" && runID == s.inheritedRunID {
		return "", nil, ErrRecoveryRequired
	}
	if s.busy(runID) {
		return "", nil, ErrCommitUnresolved
	}
	if err := s.refreshAdmission(ctx, false); err != nil {
		return "", nil, err
	}
	state, err := s.ensureRunLoaded(ctx, runID)
	if err != nil {
		return "", nil, err
	}
	return runID, state, nil
}

func (s *Session) decideAndCommit(ctx context.Context, sub *submission, runID string, state *kernel.State) {
	cmdID, _, cmdToken := commandIdentity(sub.req.Command)
	origin := sub.req.Origin
	cmdID, err := s.resolveSubmissionFacts(runID, cmdID, origin, cmdToken, state, sub)
	if err != nil {
		s.respond(sub, nil, err)
		return
	}

	events, decErr := kernel.Decide(state, sub.req.Command, &sub.req.Facts)
	if decErr != nil {
		s.reactEarlyTimerRejection(runID, state, sub.req.Command, decErr)
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

	s.commit(ctx, &step{
		t:       s.buildTransition(runID, cmdID, origin, state, events, &sub.req),
		events:  events,
		attempt: 0,
		done: func(ctx context.Context, res *AppendResult) {
			s.requestCommitted(ctx, runID, events, res, sub)
		},
		fail: func(_ context.Context, err error) {
			s.respond(sub, nil, err)
		},
	})
}

func (s *Session) reactEarlyTimerRejection(runID string, state *kernel.State, cmd kernel.Command, decErr error) {
	var rej kernel.Rejection
	if errors.As(decErr, &rej) && rej.Code == kernel.RejectionCodeEarlyTimer {
		if timerCmd, ok := cmd.(*kernel.TimerFiredCommand); ok {
			if active, exists := state.ActiveTimers[timerCmd.TimerID]; exists && active != nil && active.Generation == timerCmd.Generation {
				s.armTimer(&ArmedTimer{
					TimerID:    active.TimerID,
					SessionID:  s.cfg.SessionID,
					RunID:      runID,
					Generation: active.Generation,
					Purpose:    active.Purpose,
					Deadline:   active.Deadline,
				})
			}
		}
	}
}

func (s *Session) resolveSubmissionFacts(
	runID, cmdID, origin string,
	cmdToken uint64,
	state *kernel.State,
	sub *submission,
) (string, error) {
	if cmdID == "" {
		newID, err := s.cfg.IDs.NewID()
		if err != nil {
			return "", fmt.Errorf("new id: %w", err)
		}
		cmdID = newID
	}

	var token uint64
	if state.Admitted {
		token = s.admission.AdmissionToken
	} else {
		token = cmdToken
	}

	cmd := sub.req.Command
	if accept, ok := cmd.(*kernel.AcceptRunCommand); ok && accept.SessionID == "" {
		withSession := *accept
		withSession.SessionID = s.cfg.SessionID
		cmd = &withSession
	}
	stamped, err := stampCommand(cmd, cmdID, runID, origin, token)
	if err != nil {
		return "", err
	}
	sub.req.Command = stamped

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

	return cmdID, nil
}

func (s *Session) requestCommitted(
	ctx context.Context,
	runID string,
	events []kernel.Event,
	res *AppendResult,
	sub *submission,
) {
	state := s.runs[runID]
	_, isAccept := sub.req.Command.(*kernel.AcceptRunCommand)
	if isAccept && state != nil && state.Accepted && !state.Admitted {
		s.queue.push(runID, sub.req.QueueDecisionRef, sub.req.Facts.ModelPlan)
	}
	if state != nil && state.Finished && s.admission.ActiveRunID == runID {
		s.admission.ActiveRunID = ""
	}

	s.maybeAdmitNext(ctx)
	s.armCommittedTimers(runID, events)

	s.respond(sub, &Reply{
		Events:    events,
		Duplicate: res.Duplicate,
		Receipt:   res.Receipt,
	}, nil)

	s.notifyCommitted(runID)
}

// notifyCommitted hands the run's post-commit snapshot to the configured hook.
// Evolve is copy-on-write and every commit replaces s.runs[runID], so the
// published state is never mutated by a later commit.
func (s *Session) notifyCommitted(runID string) {
	if s.cfg.OnCommitted != nil {
		s.cfg.OnCommitted(runID, s.runs[runID])
	}
}
