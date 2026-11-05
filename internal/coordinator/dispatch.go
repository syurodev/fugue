package coordinator

import (
	"context"
	"errors"
	"fmt"

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

// handleDispatch reserves a worker slot before the dispatch is decided. Without
// capacity the request waits locally, with no event and no cancellation, and the
// actor keeps serving other commands.
func (s *Session) handleDispatch(ctx context.Context, sub *submission, prep *kernel.PrepareDispatchCommand) {
	if s.halted() {
		s.respond(sub, nil, ErrCommitUnresolved)
		return
	}

	ref, err := s.slots.reserve()
	switch {
	case errors.Is(err, ErrNoCapacity):
		s.parked = append(s.parked, *sub)
		if s.onPark != nil {
			s.onPark()
		}
		return
	case err != nil:
		s.respond(sub, nil, err)
		return
	}

	s.executeDispatch(ctx, sub, ref, prep)
}

func (s *Session) resolveDispatchMetadata(
	state *kernel.State,
	sub *submission,
	runID, cmdID, origin string,
) (string, uint64, error) {
	if cmdID == "" {
		newID, err := s.cfg.IDs.NewID()
		if err != nil {
			return "", 0, fmt.Errorf("new id: %w", err)
		}
		cmdID = newID
	}
	var token uint64
	if state.Admitted {
		token = s.admission.AdmissionToken
	}
	stamped, err := stampCommand(sub.req.Command, cmdID, runID, origin, token)
	if err != nil {
		return "", 0, err
	}
	sub.req.Command = stamped
	return cmdID, token, nil
}

// populateDispatchFacts fills the facts the coordinator owns. The dispatch
// authorization stays whatever the trusted caller supplied: the kernel refuses a
// dispatch without it, and a made-up value would defeat that guard.
func (s *Session) populateDispatchFacts(
	sub *submission,
	ref string,
	prep *kernel.PrepareDispatchCommand,
	cmdID, runID, origin string,
	token, nextSeq uint64,
) {
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
		dispatch := *sub.req.Facts.ModelDispatch
		dispatch.ExecutorFence = nextSeq
		dispatch.AttemptID = prep.AttemptID
		dispatch.InvocationID = prep.InvocationID
		dispatch.Token = token
		sub.req.Facts.ModelDispatch = &dispatch
	}
}

func (s *Session) executeDispatch(
	ctx context.Context,
	sub *submission,
	ref string,
	prep *kernel.PrepareDispatchCommand,
) {
	cmdID, runID, _ := commandIdentity(sub.req.Command)
	if runID == "" {
		runID = sub.req.Target
	}
	origin := sub.req.Origin
	state, err := s.ensureRunLoaded(ctx, runID)
	if err != nil {
		s.abortDispatch(ctx, sub, ref, err)
		return
	}

	cmdID, token, err := s.resolveDispatchMetadata(state, sub, runID, cmdID, origin)
	if err != nil {
		s.abortDispatch(ctx, sub, ref, err)
		return
	}

	nextSeq := state.LastSequence + 1
	s.populateDispatchFacts(sub, ref, prep, cmdID, runID, origin, token, nextSeq)

	events, decErr := kernel.Decide(state, sub.req.Command, &sub.req.Facts)
	if decErr != nil {
		s.abortDispatch(ctx, sub, ref, decErr)
		return
	}

	s.commit(ctx, &step{
		t:       s.buildTransition(runID, cmdID, origin, state, events, &sub.req),
		events:  events,
		attempt: 0,
		done: func(ctx context.Context, res *AppendResult) {
			s.dispatchCommitted(ctx, sub, prep, ref, runID, events, res)
		},
		fail: func(ctx context.Context, err error) {
			s.abortDispatch(ctx, sub, ref, err)
		},
	})
}

// abortDispatch returns the reserved slot and reports why nothing was dispatched.
func (s *Session) abortDispatch(ctx context.Context, sub *submission, ref string, err error) {
	s.releaseSlot(ctx, ref)
	s.respond(sub, nil, err)
}

// dispatchCommitted runs after the dispatch batch is confirmed: only now does the
// work go to a worker. A batch without a dispatch event (a budget block) hands
// nothing off and returns the slot.
func (s *Session) dispatchCommitted(
	ctx context.Context,
	sub *submission,
	prep *kernel.PrepareDispatchCommand,
	ref, runID string,
	events []kernel.Event,
	res *AppendResult,
) {
	dispatchEvt := findDispatchEvent(events)
	if dispatchEvt == nil {
		s.releaseSlot(ctx, ref)
		s.respond(sub, &Reply{Events: events, Duplicate: res.Duplicate, Receipt: res.Receipt}, nil)
		s.notifyCommitted()
		return
	}

	w := Work{
		Lease: Lease{
			SessionID:        s.cfg.SessionID,
			RunID:            runID,
			InvocationID:     prep.InvocationID,
			AttemptID:        prep.AttemptID,
			AdmissionToken:   s.admission.AdmissionToken,
			OwnerIncarnation: s.cfg.OwnerIncarnation,
			ExecutorFence:    dispatchFence(dispatchEvt),
		},
		Dispatch: dispatchEvt,
	}
	if err := s.slots.handoff(ref, &w); err != nil {
		s.respond(sub, nil, err)
		return
	}
	s.attemptSlots[attemptKey{runID: runID, invocationID: prep.InvocationID, attemptID: prep.AttemptID}] = ref

	s.respond(sub, &Reply{Events: events, Duplicate: res.Duplicate, Receipt: res.Receipt}, nil)
	s.notifyCommitted()
}

func (s *Session) releaseSlot(ctx context.Context, ref string) {
	if err := s.slots.release(ref); err != nil {
		return
	}
	s.retryParked(ctx)
}

// retryParked gives freed capacity to the requests waiting for it, oldest first.
// Nothing is dispatched while a commit is unresolved.
func (s *Session) retryParked(ctx context.Context) {
	for len(s.parked) > 0 && !s.halted() {
		ref, err := s.slots.reserve()
		if err != nil {
			return
		}
		head := s.parked[0]
		s.parked = s.parked[1:]

		if prep, ok := head.req.Command.(*kernel.PrepareDispatchCommand); ok {
			s.executeDispatch(ctx, &head, ref, prep)
			continue
		}
		s.respond(&head, nil, ErrHandoffFailed)
		if err = s.slots.release(ref); err != nil {
			return
		}
	}
}

func findDispatchEvent(events []kernel.Event) kernel.Event {
	for _, ev := range events {
		switch ev.(type) {
		case *kernel.ModelDispatchStartedEvent, *kernel.ToolDispatchStartedEvent:
			return ev
		}
	}
	return nil
}

func dispatchFence(ev kernel.Event) uint64 {
	switch dev := ev.(type) {
	case *kernel.ModelDispatchStartedEvent:
		return dev.ExecutorFence
	case *kernel.ToolDispatchStartedEvent:
		return dev.ExecutorFence
	}
	return 0
}
