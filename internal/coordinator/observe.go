package coordinator

import (
	"context"
	"fmt"

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
		req:     Request{},
		obs:     obs,
		replyCh: make(chan submitReply, 1),
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

	s.mu.Lock()
	defer s.mu.Unlock()

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

func (s *Session) prepareObservationCommand(
	obs *Observation,
	state *kernel.State,
) (string, string, string, uint64, error) {
	cmdID, runID, token := commandIdentity(obs.Command)
	origin := originWorker
	if runID == "" {
		runID = obs.Lease.RunID
	}
	if token == 0 {
		token = obs.Lease.AdmissionToken
	}
	if cmdID == "" {
		newID, idErr := s.cfg.IDs.NewID()
		if idErr != nil {
			return "", "", "", 0, fmt.Errorf("new id: %w", idErr)
		}
		cmdID = newID
	}
	stamped, err := stampCommand(obs.Command, cmdID, runID, origin, token)
	if err != nil {
		return "", "", "", 0, err
	}
	obs.Command = stamped

	obs.Facts.Envelope = kernel.Envelope{
		RunID:       runID,
		Sequence:    state.LastSequence + 1,
		CommandID:   cmdID,
		CausationID: cmdID,
		Origin:      origin,
		Token:       token,
		Timestamp:   s.cfg.Clock.Now(),
	}
	obs.Facts.OwnerIncarnation = s.cfg.OwnerIncarnation
	obs.Facts.ExecutorFence = obs.Lease.ExecutorFence

	return cmdID, runID, origin, token, nil
}

func (s *Session) handleObservation(ctx context.Context, sub *submission, obs *Observation) {
	state, err := s.ensureRunLoaded(ctx, obs.Lease.RunID)
	if err != nil {
		s.respond(sub, nil, err)
		return
	}

	if valErr := s.validateObservationLease(&obs.Lease, state); valErr != nil {
		s.respond(sub, nil, valErr)
		return
	}

	if !isWorkerObservation(obs.Command) {
		s.respond(sub, nil, ErrObservationNotAllowed)
		return
	}

	cmdID, runID, origin, _, prepErr := s.prepareObservationCommand(obs, state)
	if prepErr != nil {
		s.respond(sub, nil, prepErr)
		return
	}

	events, decErr := kernel.Decide(state, obs.Command, &obs.Facts)
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
				Kind:      obs.Command.CommandName(),
				Principal: origin,
				Target:    runID,
			},
		}, nil)
		return
	}

	req := Request{
		Command:        obs.Command,
		Facts:          obs.Facts,
		Kind:           obs.Command.CommandName(),
		Principal:      origin,
		Target:         runID,
		MetadataInput:  obs.MetadataInput,
		ProtectedInput: obs.ProtectedInput,
	}

	t := s.buildTransition(runID, cmdID, origin, state, events, &req)
	appRes, appErr := s.cfg.Store.Append(ctx, t)
	if appErr != nil {
		s.respond(sub, nil, appErr)
		return
	}

	s.applyObservationSuccess(ctx, &obs.Lease, obs.Command, runID, events, &appRes, sub)
}

func (s *Session) applyObservationSuccess(
	ctx context.Context,
	lease *Lease,
	cmd kernel.Command,
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

	if isTerminalObservation(cmd) {
		key := attemptKey{
			runID:        lease.RunID,
			invocationID: lease.InvocationID,
			attemptID:    lease.AttemptID,
		}
		slotRef := s.attemptSlots[key]
		delete(s.attemptSlots, key)
		if slotRef != "" {
			if err := s.slots.complete(slotRef); err != nil {
				s.mu.Unlock()
				return
			}
		}
	}

	if s.runs[runID] != nil && s.runs[runID].Finished {
		if s.admission.ActiveRunID == runID {
			s.admission.ActiveRunID = ""
		}
	}
	s.mu.Unlock()

	if isTerminalObservation(cmd) {
		s.retryParked(ctx)
	}
	s.maybeAdmitNext(ctx)

	s.respond(sub, &Reply{
		Events:    events,
		Duplicate: appRes.Duplicate,
		Receipt:   appRes.Receipt,
	}, nil)

	s.notifyCommitted()
}
