package coordinator

import (
	"context"
	"errors"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

type attemptKey struct {
	runID        string
	invocationID string
	attemptID    string
}

func isDispatch(state *kernel.State, cmd kernel.Command) bool {
	prep, ok := cmd.(*kernel.PrepareDispatchCommand)
	if !ok || state == nil {
		return false
	}
	if state.CurrentModelInvocationID == prep.InvocationID {
		if inv := state.ModelInvocations[prep.InvocationID]; inv != nil {
			if att := inv.Attempts[prep.AttemptID]; att != nil {
				return att.Status == kernel.ModelAttemptStatusPlanned
			}
		}
	}
	if tool := state.ToolInvocations[prep.InvocationID]; tool != nil {
		if att := tool.Attempts[prep.AttemptID]; att != nil {
			return att.Status == kernel.ToolAttemptStatusReady
		}
	}
	return false
}

func (s *Session) handleDispatch(ctx context.Context, sub submission, prep *kernel.PrepareDispatchCommand) {
	s.mu.Lock()
	if s.halted {
		s.mu.Unlock()
		sub.replyCh <- submitReply{err: ErrCommitUnresolved}
		return
	}

	ref, ok := s.slots.reserve()
	if !ok {
		s.parked = append(s.parked, sub)
		onP := s.onPark
		s.mu.Unlock()
		if onP != nil {
			onP()
		}
		return
	}
	s.mu.Unlock()

	s.executeDispatch(ctx, sub, ref, prep)
}

func (s *Session) executeDispatch(
	ctx context.Context,
	sub submission,
	ref string,
	prep *kernel.PrepareDispatchCommand,
) {
	cmdID, runID, origin, token := commandMetadata(sub.req.Command)
	if runID == "" {
		runID = sub.req.Target
	}

	s.mu.Lock()
	state := s.runs[runID]
	s.mu.Unlock()

	var err error
	if cmdID == "" {
		cmdID, err = s.cfg.IDs.NewID()
		if err != nil {
			s.releaseSlotAndRetry(ctx, ref)
			sub.replyCh <- submitReply{err: err}
			return
		}
	}
	if origin == "" {
		origin = sub.req.Principal
		if origin == "" {
			origin = "session"
		}
	}
	if state.Admitted {
		token = s.admission.AdmissionToken
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
	sub.req.Facts.WorkerReservationRef = ref
	sub.req.Facts.ExecutorFence = nextSeq

	if sub.req.Facts.ModelDispatch != nil {
		sub.req.Facts.ModelDispatch.ExecutorFence = nextSeq
		sub.req.Facts.ModelDispatch.AttemptID = prep.AttemptID
		sub.req.Facts.ModelDispatch.InvocationID = prep.InvocationID
		sub.req.Facts.ModelDispatch.Token = token
	}
	if sub.req.Facts.DispatchAuthorizationRef == "" {
		sub.req.Facts.DispatchAuthorizationRef = "auth-" + ref
	}

	events, decErr := kernel.Decide(state, sub.req.Command, &sub.req.Facts)
	if decErr != nil {
		s.releaseSlotAndRetry(ctx, ref)
		sub.replyCh <- submitReply{err: decErr}
		return
	}

	if events == nil {
		s.releaseSlotAndRetry(ctx, ref)
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
		if errors.Is(appErr, ErrCommitUnknown) {
			s.handleUnknownCommit(ctx, sub, t, ref, events, prep)
			return
		}
		s.releaseSlotAndRetry(ctx, ref)
		sub.replyCh <- submitReply{err: appErr}
		return
	}

	s.onDispatchCommitted(runID, prep, ref, nextSeq, events, appRes, sub)
}

func (s *Session) onDispatchCommitted(
	runID string,
	prep *kernel.PrepareDispatchCommand,
	ref string,
	fence uint64,
	events []kernel.Event,
	appRes AppendResult,
	sub submission,
) {
	s.mu.Lock()
	for _, ev := range events {
		s.runs[runID] = kernel.Evolve(s.runs[runID], ev)
	}
	s.runRevs[runID] = appRes.RunRevision
	if appRes.AdmissionRevision > 0 {
		s.admission.Revision = appRes.AdmissionRevision
	}

	var dispatchEvt kernel.Event
	for _, ev := range events {
		switch ev.(type) {
		case *kernel.ModelDispatchStartedEvent, *kernel.ToolDispatchStartedEvent:
			dispatchEvt = ev
		}
	}

	if dispatchEvt != nil {
		w := Work{
			Lease: Lease{
				SessionID:        s.cfg.SessionID,
				RunID:            runID,
				InvocationID:     prep.InvocationID,
				AttemptID:        prep.AttemptID,
				AdmissionToken:   s.admission.AdmissionToken,
				OwnerIncarnation: s.cfg.OwnerIncarnation,
				ExecutorFence:    fence,
			},
			Dispatch: dispatchEvt,
		}
		_ = s.slots.handoff(ref, w)
		s.attemptSlots[attemptKey{
			runID:        runID,
			invocationID: prep.InvocationID,
			attemptID:    prep.AttemptID,
		}] = ref
	}
	s.mu.Unlock()

	sub.replyCh <- submitReply{
		reply: Reply{
			Events:    events,
			Duplicate: appRes.Duplicate,
			Receipt:   appRes.Receipt,
		},
	}
}

func (s *Session) releaseSlotAndRetry(ctx context.Context, ref string) {
	_ = s.slots.release(ref)
	s.retryParked(ctx)
}

func (s *Session) retryParked(ctx context.Context) {
	for {
		s.mu.Lock()
		if len(s.parked) == 0 {
			s.mu.Unlock()
			return
		}
		ref, ok := s.slots.reserve()
		if !ok {
			s.mu.Unlock()
			return
		}
		head := s.parked[0]
		s.parked = s.parked[1:]
		s.mu.Unlock()

		prep, _ := head.req.Command.(*kernel.PrepareDispatchCommand)
		s.executeDispatch(ctx, head, ref, prep)
	}
}
