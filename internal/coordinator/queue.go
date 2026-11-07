package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

// queuedRun is an accepted run waiting for admission. It carries what the
// AcceptRun request supplied for the admission step, so admission invents
// nothing.
type queuedRun struct {
	runID       string
	plan        kernel.ModelRequestPlannedEvent
	decisionRef string
	// admitCmdID is the command ID of an admission attempt whose outcome may be
	// unknown. It is reused until the attempt is definitely rejected, so a retry
	// never mints a second command for the same admission.
	admitCmdID string
}

// sessionQueue is the in-memory FIFO of runs accepted by this incarnation. Only
// the session actor touches it.
type sessionQueue struct {
	items []*queuedRun
}

func newSessionQueue() *sessionQueue {
	return &sessionQueue{items: nil}
}

func (q *sessionQueue) push(runID, decisionRef string, plan *kernel.ModelRequestPlannedEvent) {
	item := &queuedRun{runID: runID, plan: *plan, decisionRef: decisionRef, admitCmdID: ""}
	item.plan.ToolDeclarationRefs = slices.Clone(plan.ToolDeclarationRefs)
	q.items = append(q.items, item)
}

func (q *sessionQueue) head() (*queuedRun, bool) {
	if len(q.items) == 0 {
		return nil, false
	}
	return q.items[0], true
}

func (q *sessionQueue) pop() (*queuedRun, bool) {
	item, ok := q.head()
	if !ok {
		return nil, false
	}
	q.items = q.items[1:]
	return item, true
}

func (q *sessionQueue) empty() bool {
	return len(q.items) == 0
}

// admitMetadata is the command metadata an admission is stored with. The field
// names are the ones the execution store's metadata allowlist accepts.
type admitMetadata struct {
	Token        uint64 `json:"token"`
	ContextEpoch uint64 `json:"ContextEpoch"`
}

func (s *Session) buildAdmitTransition(item *queuedRun, cmdID string, token, epoch uint64, events []kernel.Event) (*Transition, error) {
	metadata, err := codec.MarshalCanonical(admitMetadata{Token: token, ContextEpoch: epoch})
	if err != nil {
		return nil, fmt.Errorf("marshal admit metadata: %w", err)
	}
	return &Transition{
		SessionID: s.cfg.SessionID,
		RunID:     item.runID,
		CommandID: cmdID,
		Kind:      "AdmitRun",
		Principal: defaultOriginSession,
		Target:    item.runID,
		Fence: Fence{
			AdmissionToken:   token,
			OwnerIncarnation: s.cfg.OwnerIncarnation,
			QueueRevision:    0,
		},
		ExpectedRunRevision:       s.runRevs[item.runID],
		ExpectedAdmissionRevision: s.expectedAdmissionRevision(),
		Events:                    events,
		Result: codec.ReceiptResult{
			FirstSequence: events[0].EventEnvelope().Sequence,
			LastSequence:  events[len(events)-1].EventEnvelope().Sequence,
		},
		MetadataInput:  metadata,
		ProtectedInput: nil,
		CorrelationID:  "",
	}, nil
}

// maybeAdmitNext admits the head of the queue when no run is active. A failure
// of any kind leaves the run at the head; the next committed command tries again.
func (s *Session) maybeAdmitNext(ctx context.Context) {
	item, ok := s.queue.head()
	if !ok || s.admission.ActiveRunID != "" || s.busy(item.runID) {
		return
	}
	state := s.runs[item.runID]
	if state == nil {
		state = kernel.NewState()
	}

	initial, known := s.initialContext(item.runID)
	if !known {
		s.deferAdmission(ctx, item, nil)
		return
	}

	if item.admitCmdID == "" {
		cmdID, err := s.cfg.IDs.NewID()
		if err != nil {
			return
		}
		item.admitCmdID = cmdID
	}

	token := s.admission.AdmissionToken + 1
	cmd, facts := s.admitDecision(item, state, token, initial)
	events, err := kernel.Decide(state, cmd, &facts)
	if err != nil || len(events) == 0 {
		s.deferAdmission(ctx, item, err)
		return
	}

	tr, err := s.buildAdmitTransition(item, item.admitCmdID, token, initial.epoch, events)
	if err != nil {
		s.deferAdmission(ctx, item, err)
		return
	}
	s.commit(ctx, &step{
		t:       tr,
		events:  events,
		attempt: 0,
		done: func(context.Context, *AppendResult) {
			s.admitted(item, token, initial)
		},
		fail: func(ctx context.Context, err error) {
			s.deferAdmission(ctx, item, err)
		},
	})
}

// admitDecision builds the admission command and its facts from what the
// AcceptRun request supplied and what the admission row holds.
func (s *Session) admitDecision(
	item *queuedRun,
	state *kernel.State,
	token uint64,
	initial contextStart,
) (*kernel.AdmitRunCommand, kernel.DecisionFacts) {
	cmd := &kernel.AdmitRunCommand{
		CommandID:              item.admitCmdID,
		RunID:                  item.runID,
		Origin:                 defaultOriginSession,
		Token:                  token,
		OwnerIncarnation:       s.cfg.OwnerIncarnation,
		QueueDecisionRef:       item.decisionRef,
		ContextEpoch:           initial.epoch,
		StartRunID:             initial.startRunID,
		InitialContextRevision: initial.revision,
	}
	plan := item.plan
	plan.ToolDeclarationRefs = slices.Clone(item.plan.ToolDeclarationRefs)
	facts := kernel.DecisionFacts{
		Envelope: kernel.Envelope{
			RunID:       item.runID,
			Sequence:    state.LastSequence + 1,
			CommandID:   item.admitCmdID,
			CausationID: item.admitCmdID,
			Origin:      defaultOriginSession,
			Token:       token,
			Timestamp:   s.cfg.Clock.Now(),
		},
		OwnerIncarnation: s.cfg.OwnerIncarnation,
		ModelPlan:        &plan,
	}
	return cmd, facts
}

// deferAdmission leaves the run at the head of the queue after a failed
// admission. The command ID is dropped because the attempt is definitely over.
func (s *Session) deferAdmission(ctx context.Context, item *queuedRun, err error) {
	item.admitCmdID = ""
	attrs := []any{"run_id", item.runID}
	var rej kernel.Rejection
	if errors.As(err, &rej) {
		attrs = append(attrs, "code", string(rej.Code))
	}
	slog.WarnContext(ctx, "admission deferred", attrs...)
}

func (s *Session) admitted(item *queuedRun, token uint64, initial contextStart) {
	if head, ok := s.queue.head(); ok && head == item {
		s.queue.pop()
	}
	s.admission.ActiveRunID = item.runID
	s.admission.AdmissionToken = token
	s.admission.OwnerIncarnation = s.cfg.OwnerIncarnation
	s.admission.ContextEpoch = initial.epoch
	s.admission.ContextRevision = initial.revision
	s.admission.ContextStartRunID = initial.startRunID
	s.notifyCommitted(item.runID)
}

// contextStart is the context continuity a run is admitted with.
type contextStart struct {
	epoch      uint64
	revision   uint64
	startRunID string
}

// initialContext decides the context a run is admitted with. A session with no
// history has epoch and revision 0 on its admission row; its first run starts
// epoch 1 at revision 1 and is its own start run, and the first RunAdmitted
// initializes the row (event-storage-recovery §3, context control). Once the
// row has history, a run continues it with the row's epoch, revision and start
// run. The start run is unknown when the row carries none, and then the run is
// not admitted rather than given an invented one (roadmap Q-21).
func (s *Session) initialContext(runID string) (contextStart, bool) {
	if s.admission.ContextEpoch == 0 {
		return contextStart{epoch: 1, revision: 1, startRunID: runID}, true
	}
	start := contextStart{
		epoch:      s.admission.ContextEpoch,
		revision:   s.admission.ContextRevision,
		startRunID: s.admission.ContextStartRunID,
	}
	return start, start.startRunID != ""
}

// validateAccept refuses an AcceptRun that lacks data only the caller can
// supply. Nothing is defaulted, because a default would be written into a
// durable event as if the caller had decided it.
func (s *Session) validateAccept(accept *kernel.AcceptRunCommand, req *Request) error {
	if accept == nil {
		return fmt.Errorf("%w: nil command", ErrInvalidAccept)
	}
	if accept.InputRef == "" {
		return fmt.Errorf("%w: input ref", ErrInvalidAccept)
	}
	if accept.QueueItemRef == "" {
		return fmt.Errorf("%w: queue item ref", ErrInvalidAccept)
	}
	if accept.ProtocolBinding == "" {
		return fmt.Errorf("%w: protocol binding", ErrInvalidAccept)
	}
	if accept.SessionID != "" && accept.SessionID != s.cfg.SessionID {
		return fmt.Errorf("%w: session id", ErrInvalidAccept)
	}
	if req.QueueDecisionRef == "" {
		return fmt.Errorf("%w: queue decision ref", ErrInvalidAccept)
	}
	if req.Facts.ModelPlan == nil {
		return fmt.Errorf("%w: model plan", ErrInvalidAccept)
	}
	return nil
}
