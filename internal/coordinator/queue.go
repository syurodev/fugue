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

func (q *sessionQueue) push(runID string, facts kernel.DecisionFacts) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, queuedRun{runID: runID, facts: facts})
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

func (s *Session) maybeAdmitNext(ctx context.Context) {
	s.mu.Lock()
	if s.admission.ActiveRunID != "" || s.queue.empty() {
		s.mu.Unlock()
		return
	}
	item, ok := s.queue.pop()
	if !ok {
		s.mu.Unlock()
		return
	}

	runID := item.runID
	state := s.runs[runID]
	if state == nil {
		state = kernel.NewState()
		s.runs[runID] = state
	}
	s.mu.Unlock()

	cmdID, err := s.cfg.IDs.NewID()
	if err != nil {
		return
	}

	newToken := s.admission.AdmissionToken + 1
	admitCmd := &kernel.AdmitRunCommand{
		CommandID:              cmdID,
		RunID:                  runID,
		Origin:                 "session",
		Token:                  newToken,
		OwnerIncarnation:       s.cfg.OwnerIncarnation,
		QueueDecisionRef:       "auto-admit",
		ContextEpoch:           1,
		StartRunID:             runID,
		InitialContextRevision: 1,
	}

	plan := item.facts.ModelPlan
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

	facts := kernel.DecisionFacts{
		Envelope: kernel.Envelope{
			RunID:       runID,
			Sequence:    state.LastSequence + 1,
			CommandID:   cmdID,
			CausationID: cmdID,
			Origin:      "session",
			Token:       newToken,
			Timestamp:   s.cfg.Clock.Now(),
		},
		OwnerIncarnation: s.cfg.OwnerIncarnation,
		ModelPlan:        plan,
	}

	events, decErr := kernel.Decide(state, admitCmd, &facts)
	if decErr != nil || len(events) == 0 {
		return
	}

	t := &Transition{
		SessionID: s.cfg.SessionID,
		RunID:     runID,
		CommandID: cmdID,
		Kind:      "AdmitRun",
		Principal: "session",
		Target:    runID,
		Fence: Fence{
			AdmissionToken:   newToken,
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

	appRes, appErr := s.cfg.Store.Append(ctx, t)
	if appErr != nil {
		return
	}

	s.mu.Lock()
	for _, ev := range events {
		s.runs[runID] = kernel.Evolve(s.runs[runID], ev)
	}
	s.runRevs[runID] = appRes.RunRevision
	s.admission.ActiveRunID = runID
	s.admission.AdmissionToken = newToken
	if appRes.AdmissionRevision > 0 {
		s.admission.Revision = appRes.AdmissionRevision
	}
	s.mu.Unlock()
}
