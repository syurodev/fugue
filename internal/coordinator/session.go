package coordinator

import (
	"context"
	"fmt"
	"sync"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

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

// Request is a command submission to the session coordinator.
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
	stopped        chan struct{}
	stopOnce       sync.Once
}

// Start loads the admission row, validates ownership, and starts the actor goroutine.
func Start(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.WorkerCapacity <= 0 {
		cfg.WorkerCapacity = 1
	}

	adm, err := cfg.Store.LoadAdmission(ctx, cfg.SessionID)
	if err != nil {
		return nil, fmt.Errorf("load admission: %w", err)
	}

	var inheritedRunID string
	if adm.ActiveRunID != "" && adm.OwnerIncarnation != cfg.OwnerIncarnation {
		inheritedRunID = adm.ActiveRunID
	}

	actorCtx, cancel := context.WithCancel(ctx)
	s := &Session{
		mu:             sync.Mutex{},
		cfg:            cfg,
		admission:      adm,
		inheritedRunID: inheritedRunID,
		slots:          newSlots(cfg.WorkerCapacity),
		runs:           make(map[string]*kernel.State),
		runRevs:        make(map[string]uint64),
		queue:          newSessionQueue(),
		mailbox:        make(chan submission),
		cancel:         cancel,
		task:           nil,
		stopped:        make(chan struct{}),
		stopOnce:       sync.Once{},
	}

	s.task = supervise.Go(actorCtx, "coordinator.session", s.loop)
	return s, nil
}

// Submit serializes a request through the session actor.
func (s *Session) Submit(ctx context.Context, r Request) (Reply, error) {
	select {
	case <-s.stopped:
		return Reply{}, ErrSessionStopped
	default:
	}

	sub := submission{
		req:     r,
		replyCh: make(chan submitReply, 1),
	}

	select {
	case s.mailbox <- sub:
	case <-ctx.Done():
		return Reply{}, ctx.Err()
	case <-s.stopped:
		return Reply{}, ErrSessionStopped
	}

	select {
	case res := <-sub.replyCh:
		return res.reply, res.err
	case <-ctx.Done():
		return Reply{}, ctx.Err()
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
			_ = s.task.Wait()
		}
	})
}

func (s *Session) loop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case sub := <-s.mailbox:
			s.handleSubmission(ctx, sub)
		}
	}
}

func (s *Session) handleSubmission(ctx context.Context, sub submission) {
	cmdID, runID, origin, cmdToken := commandMetadata(sub.req.Command)
	if runID == "" {
		runID = sub.req.Target
	}

	if runID != "" && runID == s.inheritedRunID {
		sub.replyCh <- submitReply{err: ErrRecoveryRequired}
		return
	}

	s.mu.Lock()
	state, exists := s.runs[runID]
	if !exists {
		st, rev, err := s.cfg.Store.LoadRun(ctx, runID)
		if err != nil {
			s.mu.Unlock()
			sub.replyCh <- submitReply{err: err}
			return
		}
		if st == nil {
			st = kernel.NewState()
		}
		state = st
		s.runs[runID] = state
		s.runRevs[runID] = rev
	}
	s.mu.Unlock()

	var err error
	if cmdID == "" {
		cmdID, err = s.cfg.IDs.NewID()
		if err != nil {
			sub.replyCh <- submitReply{err: fmt.Errorf("new id: %w", err)}
			return
		}
	}
	if origin == "" {
		origin = sub.req.Principal
		if origin == "" {
			origin = "session"
		}
	}

	if acceptCmd, ok := sub.req.Command.(*kernel.AcceptRunCommand); ok {
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

	var token uint64
	if state.Admitted {
		token = s.admission.AdmissionToken
	} else {
		token = cmdToken
	}
	setCommandMetadata(sub.req.Command, cmdID, runID, origin, token)

	nextSeq := state.LastSequence + 1
	sub.req.Facts.Envelope = kernel.Envelope{
		RunID:       runID,
		Sequence:    nextSeq,
		CommandID:   cmdID,
		CausationID: cmdID,
		Origin:      origin,
		Token:       token,
		Timestamp:   s.cfg.Clock.Now(),
	}
	sub.req.Facts.OwnerIncarnation = s.cfg.OwnerIncarnation

	events, decErr := kernel.Decide(state, sub.req.Command, &sub.req.Facts)
	if decErr != nil {
		sub.replyCh <- submitReply{err: decErr}
		return
	}

	if events == nil {
		sub.replyCh <- submitReply{
			reply: Reply{
				Events:    nil,
				Duplicate: true,
				Receipt: codec.Receipt{
					CommandID: cmdID,
					Kind:      sub.req.Kind,
					Principal: origin,
					Target:    runID,
				},
			},
		}
		return
	}

	t := s.buildTransition(runID, cmdID, origin, state, events, sub.req)
	appRes, appErr := s.cfg.Store.Append(ctx, t)
	if appErr != nil {
		sub.replyCh <- submitReply{err: appErr}
		return
	}

	s.mu.Lock()
	for _, ev := range events {
		s.runs[runID] = kernel.Evolve(s.runs[runID], ev)
	}
	s.runRevs[runID] = appRes.RunRevision
	if appRes.AdmissionRevision > 0 {
		s.admission.Revision = appRes.AdmissionRevision
	}

	if _, ok := sub.req.Command.(*kernel.AcceptRunCommand); ok {
		s.queue.push(runID, sub.req.Facts)
	}

	if s.runs[runID] != nil && s.runs[runID].Finished {
		if s.admission.ActiveRunID == runID {
			s.admission.ActiveRunID = ""
		}
	}
	s.mu.Unlock()

	s.maybeAdmitNext(ctx)

	sub.replyCh <- submitReply{
		reply: Reply{
			Events:    events,
			Duplicate: appRes.Duplicate,
			Receipt:   appRes.Receipt,
		},
	}
}

func (s *Session) buildTransition(
	runID, cmdID, origin string,
	state *kernel.State,
	events []kernel.Event,
	req Request,
) *Transition {
	fence := Fence{
		AdmissionToken:   0,
		OwnerIncarnation: s.cfg.OwnerIncarnation,
		QueueRevision:    s.admission.QueueRevision,
	}
	if state.Admitted {
		fence.AdmissionToken = s.admission.AdmissionToken
		fence.QueueRevision = 0
	}

	var firstSeq, lastSeq uint64
	if len(events) > 0 {
		firstSeq = events[0].EventEnvelope().Sequence
		lastSeq = events[len(events)-1].EventEnvelope().Sequence
	}

	kind := req.Kind
	if kind == "" && req.Command != nil {
		kind = req.Command.CommandName()
	}
	principal := req.Principal
	if principal == "" {
		principal = origin
	}
	target := req.Target
	if target == "" {
		target = runID
	}

	return &Transition{
		SessionID:                 s.cfg.SessionID,
		RunID:                     runID,
		CommandID:                 cmdID,
		Kind:                      kind,
		Principal:                 principal,
		Target:                    target,
		Fence:                     fence,
		ExpectedRunRevision:       s.runRevs[runID],
		ExpectedAdmissionRevision: s.admission.Revision,
		Events:                    events,
		Result: codec.ReceiptResult{
			FirstSequence: firstSeq,
			LastSequence:  lastSeq,
			RejectionCode: "",
			Message:       "",
		},
		MetadataInput:  req.MetadataInput,
		ProtectedInput: req.ProtectedInput,
		CorrelationID:  "",
	}
}
