package coordinator

import (
	"context"

	"github.com/syurodev/fugue/internal/platform/supervise"
)

// The worker side of the session: the goroutines that run dispatched work and
// the mailbox messages they post. Everything here funnels into the mailbox, so
// the actor stays the single writer of the session state.

func (s *Session) startWorkers(actorCtx context.Context) {
	for range s.cfg.WorkerCapacity {
		workerTask := supervise.Go(actorCtx, "coordinator.worker", s.work)
		s.workerTasks = append(s.workerTasks, workerTask)
	}
}

// work drains the hand-off channel until the session stops. Every item is
// served to completion: the executor decides what to report, and after it
// returns the slot's fate is settled below.
func (s *Session) work(wCtx context.Context) error {
	for {
		select {
		case <-wCtx.Done():
			return nil
		case w, ok := <-s.slots.handoffCh:
			if !ok {
				return nil
			}
			s.serve(wCtx, w)
		}
	}
}

// serve runs one dispatched item, posts its outcome and — always, whatever the
// outcome was — tells the actor the attempt is over. Every dispatched slot is
// then freed exactly once: a terminal observation that committed completes the
// slot itself and this message becomes a no-op; one that the actor rejected,
// and one that was never sent, are completed here. Reports post before this
// message from the same goroutine, so the mailbox orders them.
func (s *Session) serve(wCtx context.Context, w *Work) {
	s.cfg.Executor.Execute(wCtx, w, s.report)
	s.served(&w.Lease)
}

func (s *Session) report(obs *Observation) {
	if obs == nil {
		return
	}
	sub := submission{
		req:      Request{},
		obs:      obs,
		resolved: nil,
		served:   nil,
		replyCh:  nil,
	}
	select {
	case s.mailbox <- sub:
	case <-s.stopped:
	}
}

// served tells the actor that an executor finished its attempt. The attempt
// keeps whatever state its dispatch committed — no events are appended,
// nothing retries — so only the capacity comes back.
func (s *Session) served(lease *Lease) {
	sub := submission{
		req:      Request{},
		obs:      nil,
		resolved: nil,
		served:   lease,
		replyCh:  nil,
	}
	select {
	case s.mailbox <- sub:
	case <-s.stopped:
	}
}

// handleServed reclaims the slot of a finished attempt. completeSlot is
// idempotent: a terminal observation that committed already took the slot, so
// this is a no-op there and the real reclaim everywhere else.
func (s *Session) handleServed(ctx context.Context, lease *Lease) {
	s.completeSlot(ctx, attemptKey{runID: lease.RunID, invocationID: lease.InvocationID, attemptID: lease.AttemptID})
	s.retryParked(ctx)
}
