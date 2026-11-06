package coordinator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// SubmitObservation serializes a worker observation through the session actor.
func (s *Session) SubmitObservation(ctx context.Context, obs *Observation) (Reply, error) {
	if obs == nil {
		return Reply{}, errcode.New(errcode.InvalidArgument, "observation is nil")
	}
	return s.enqueue(ctx, &submission{
		req:      Request{},
		obs:      obs,
		resolved: nil,
		replyCh:  make(chan submitReply, 1),
	})
}

func isTerminalObservation(cmd kernel.Command) bool {
	switch cmd.(type) {
	case *kernel.ObserveModelResponseCommand,
		*kernel.ObserveModelFailureCommand,
		*kernel.ObserveToolResultCommand:
		return true
	default:
		return false
	}
}

func (s *Session) validateObservationLease(lease *Lease, state *kernel.State) error {
	if lease.RunID == "" || lease.ExecutorFence == 0 {
		return ErrStaleLease
	}

	if s.cfg.OwnerIncarnation != lease.OwnerIncarnation || s.admission.OwnerIncarnation != lease.OwnerIncarnation {
		return ErrStaleLease
	}
	if s.admission.AdmissionToken != lease.AdmissionToken {
		return ErrStaleLease
	}
	if s.admission.ActiveRunID != "" && s.admission.ActiveRunID != lease.RunID {
		return ErrStaleLease
	}
	if state == nil {
		return ErrStaleLease
	}

	fence, ok := attemptDispatchedFence(state, lease.InvocationID, lease.AttemptID)
	if !ok || fence != lease.ExecutorFence {
		return ErrStaleLease
	}
	return nil
}

func attemptDispatchedFence(state *kernel.State, invID, attID string) (uint64, bool) {
	if inv := state.ModelInvocations[invID]; inv != nil {
		if att := inv.Attempts[attID]; att != nil {
			return att.Dispatch.ExecutorFence, true
		}
	}
	if tool := state.ToolInvocations[invID]; tool != nil {
		if att := tool.Attempts[attID]; att != nil {
			return att.Dispatch.ExecutorFence, true
		}
	}
	return 0, false
}

// observationIdentity stamps a worker observation. Its origin is always worker,
// whatever the command claims, and the caller's observation is left untouched.
func (s *Session) observationIdentity(obs *Observation, state *kernel.State) (kernel.Command, kernel.DecisionFacts, error) {
	cmdID, runID, token := commandIdentity(obs.Command)
	if runID == "" {
		runID = obs.Lease.RunID
	}
	if token == 0 {
		token = obs.Lease.AdmissionToken
	}
	if cmdID == "" {
		newID, err := s.cfg.IDs.NewID()
		if err != nil {
			return nil, kernel.DecisionFacts{}, fmt.Errorf("new id: %w", err)
		}
		cmdID = newID
	}
	cmd, err := stampCommand(obs.Command, cmdID, runID, originWorker, token)
	if err != nil {
		return nil, kernel.DecisionFacts{}, err
	}

	facts := obs.Facts
	facts.Envelope = kernel.Envelope{
		RunID:       runID,
		Sequence:    state.LastSequence + 1,
		CommandID:   cmdID,
		CausationID: cmdID,
		Origin:      originWorker,
		Token:       token,
		Timestamp:   s.cfg.Clock.Now(),
	}
	facts.OwnerIncarnation = s.cfg.OwnerIncarnation
	facts.ExecutorFence = obs.Lease.ExecutorFence
	return cmd, facts, nil
}

func (s *Session) handleObservation(ctx context.Context, sub *submission, obs *Observation) {
	state, err := s.prepareObservation(ctx, obs)
	if err != nil {
		s.respond(sub, nil, err)
		return
	}

	cmd, facts, prepErr := s.observationIdentity(obs, state)
	if prepErr != nil {
		s.respond(sub, nil, prepErr)
		return
	}
	cmdID, runID, _ := commandIdentity(cmd)

	events, decErr := kernel.Decide(state, cmd, &facts)
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
				Kind:      cmd.CommandName(),
				Principal: originWorker,
				Target:    runID,
			},
		}, nil)
		return
	}

	req := Request{
		Command:          cmd,
		Facts:            facts,
		Origin:           originWorker,
		QueueDecisionRef: "",
		Kind:             cmd.CommandName(),
		Principal:        originWorker,
		Target:           runID,
		MetadataInput:    obs.MetadataInput,
		ProtectedInput:   obs.ProtectedInput,
		Payloads:         obs.Payloads,
	}

	s.commit(ctx, &step{
		t:       s.buildTransition(runID, cmdID, originWorker, state, events, &req),
		events:  events,
		attempt: 0,
		done: func(ctx context.Context, res *AppendResult) {
			s.observationCommitted(ctx, &obs.Lease, cmd, runID, events, res, sub)
		},
		fail: func(_ context.Context, err error) {
			s.respond(sub, nil, err)
		},
	})
}

// prepareObservation checks that the observation may be decided at all: no
// unresolved commit on its run, a fresh view of the run and the admission row, a
// live lease, and a command a worker is allowed to report.
func (s *Session) prepareObservation(ctx context.Context, obs *Observation) (*kernel.State, error) {
	if s.busy(obs.Lease.RunID) {
		return nil, ErrCommitUnresolved
	}
	if err := s.refreshAdmission(ctx, false); err != nil {
		return nil, err
	}
	state, err := s.ensureRunLoaded(ctx, obs.Lease.RunID)
	if err != nil {
		return nil, err
	}
	if err = s.validateObservationLease(&obs.Lease, state); err != nil {
		return nil, err
	}
	if !isWorkerObservation(obs.Command) {
		return nil, ErrObservationNotAllowed
	}
	return state, nil
}

func (s *Session) observationCommitted(
	ctx context.Context,
	lease *Lease,
	cmd kernel.Command,
	runID string,
	events []kernel.Event,
	res *AppendResult,
	sub *submission,
) {
	terminal := isTerminalObservation(cmd)
	if terminal {
		s.completeSlot(ctx, attemptKey{runID: lease.RunID, invocationID: lease.InvocationID, attemptID: lease.AttemptID})
	}

	if state := s.runs[runID]; state != nil && state.Finished && s.admission.ActiveRunID == runID {
		s.admission.ActiveRunID = ""
	}

	if terminal {
		s.retryParked(ctx)
	}
	s.maybeAdmitNext(ctx)

	s.respond(sub, &Reply{
		Events:    events,
		Duplicate: res.Duplicate,
		Receipt:   res.Receipt,
	}, nil)

	s.notifyCommitted()
}

// completeSlot frees the capacity of an attempt whose terminal observation is
// committed. The commit stands whatever happens here, so a slot that cannot be
// completed is logged and the observation still succeeds.
func (s *Session) completeSlot(ctx context.Context, key attemptKey) {
	slotRef := s.attemptSlots[key]
	delete(s.attemptSlots, key)
	if slotRef == "" {
		return
	}
	if err := s.slots.complete(slotRef); err != nil {
		slog.ErrorContext(ctx, "worker slot could not be completed", "run_id", key.runID, "attempt_id", key.attemptID)
	}
}
