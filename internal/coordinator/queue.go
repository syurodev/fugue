package coordinator

import (
	"context"
	"sync"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

type queuedRun struct {
	runID string
	facts kernel.DecisionFacts
}

type sessionQueue struct {
	mu    sync.Mutex
	items []queuedRun
}

func newSessionQueue() *sessionQueue {
	return &sessionQueue{
		mu:    sync.Mutex{},
		items: nil,
	}
}

func (q *sessionQueue) push(runID string, facts *kernel.DecisionFacts) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var f kernel.DecisionFacts
	if facts != nil {
		f = *facts
	}
	q.items = append(q.items, queuedRun{runID: runID, facts: f})
}

func (q *sessionQueue) pop() (queuedRun, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return queuedRun{}, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	return item, true
}

func (q *sessionQueue) empty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) == 0
}

func (s *Session) nextQueuedRun() (queuedRun, *kernel.State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admission.ActiveRunID != "" || s.queue.empty() {
		return queuedRun{}, nil, false
	}
	item, ok := s.queue.pop()
	if !ok {
		return queuedRun{}, nil, false
	}
	state := s.runs[item.runID]
	if state == nil {
		state = kernel.NewState()
		s.runs[item.runID] = state
	}
	return item, state, true
}

func buildAdmitCommand(cmdID, runID string, token, incarnation uint64) *kernel.AdmitRunCommand {
	return &kernel.AdmitRunCommand{
		CommandID:              cmdID,
		RunID:                  runID,
		Origin:                 defaultOriginSession,
		Token:                  token,
		OwnerIncarnation:       incarnation,
		QueueDecisionRef:       "auto-admit",
		ContextEpoch:           1,
		StartRunID:             runID,
		InitialContextRevision: 1,
	}
}

func (s *Session) buildAdmitFacts(
	cmdID, runID string,
	token, seq uint64,
	plan *kernel.ModelRequestPlannedEvent,
) kernel.DecisionFacts {
	if plan == nil {
		plan = &kernel.ModelRequestPlannedEvent{
			Envelope:              kernel.Envelope{},
			InvocationID:          runID + "-inv-1",
			FirstAttemptID:        runID + "-att-1",
			Purpose:               "turn",
			ModelRef:              "default-model",
			ContextRef:            "default-context",
			ToolDeclarationRefs:   nil,
			EstimatedBudgetTokens: 0,
		}
	}
	return kernel.DecisionFacts{
		Envelope: kernel.Envelope{
			RunID:       runID,
			Sequence:    seq,
			CommandID:   cmdID,
			CausationID: cmdID,
			Origin:      defaultOriginSession,
			Token:       token,
			Timestamp:   s.cfg.Clock.Now(),
		},
		OwnerIncarnation: s.cfg.OwnerIncarnation,
		ModelPlan:        plan,
	}
}

func (s *Session) buildAdmitTransition(
	runID, cmdID string,
	token uint64,
	events []kernel.Event,
) *Transition {
	return &Transition{
		SessionID: s.cfg.SessionID,
		RunID:     runID,
		CommandID: cmdID,
		Kind:      "AdmitRun",
		Principal: defaultOriginSession,
		Target:    runID,
		Fence: Fence{
			AdmissionToken:   token,
			OwnerIncarnation: s.cfg.OwnerIncarnation,
			QueueRevision:    0,
		},
		ExpectedRunRevision:       s.runRevs[runID],
		ExpectedAdmissionRevision: s.admission.Revision,
		Events:                    events,
		Result: codec.ReceiptResult{
			FirstSequence: events[0].EventEnvelope().Sequence,
			LastSequence:  events[len(events)-1].EventEnvelope().Sequence,
		},
		MetadataInput:  nil,
		ProtectedInput: nil,
		CorrelationID:  "",
	}
}

func (s *Session) applyAdmitSuccess(
	runID string,
	newToken uint64,
	events []kernel.Event,
	appRes *AppendResult,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range events {
		s.runs[runID] = kernel.Evolve(s.runs[runID], ev)
	}
	s.runRevs[runID] = appRes.RunRevision
	s.admission.ActiveRunID = runID
	s.admission.AdmissionToken = newToken
	if appRes.AdmissionRevision > 0 {
		s.admission.Revision = appRes.AdmissionRevision
	}
}

func (s *Session) maybeAdmitNext(ctx context.Context) {
	item, state, ok := s.nextQueuedRun()
	if !ok {
		return
	}

	cmdID, err := s.cfg.IDs.NewID()
	if err != nil {
		return
	}

	newToken := s.admission.AdmissionToken + 1
	admitCmd := buildAdmitCommand(cmdID, item.runID, newToken, s.cfg.OwnerIncarnation)
	facts := s.buildAdmitFacts(cmdID, item.runID, newToken, state.LastSequence+1, item.facts.ModelPlan)

	events, decErr := kernel.Decide(state, admitCmd, &facts)
	if decErr != nil || len(events) == 0 {
		return
	}

	t := s.buildAdmitTransition(item.runID, cmdID, newToken, events)
	appRes, appErr := s.cfg.Store.Append(ctx, t)
	if appErr != nil {
		return
	}

	s.applyAdmitSuccess(item.runID, newToken, events, &appRes)
}
