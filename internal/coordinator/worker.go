package coordinator

import (
	"context"
	"sync/atomic"

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
// served to completion: the executor decides what to report, and whatever it
// did not report is abandoned below.
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

// serve runs one dispatched item and posts its outcome. Report is expected
// from the Execute call stack; the atomic guards the ordering against a late
// report. An executor that reported nothing terminal abandoned the attempt:
// no commit will ever complete its slot, so the capacity is reclaimed.
func (s *Session) serve(wCtx context.Context, w *Work) {
	terminal := &atomic.Bool{}
	s.cfg.Executor.Execute(wCtx, w, func(obs *Observation) {
		if obs != nil && isTerminalObservation(obs.Command) {
			terminal.Store(true)
		}
		s.report(obs)
	})
	if !terminal.Load() {
		s.abandon(&w.Lease)
	}
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
func (s *Session) abandon(lease *Lease) {
	sub := submission{
		req:       Request{},
		obs:       nil,
		resolved:  nil,
		abandoned: lease,
		replyCh:   nil,
	}
	select {
	case s.mailbox <- sub:
	case <-s.stopped:
	}
}

// handleAbandoned reclaims the slot of an attempt whose executor abandoned it.
// The attempt keeps whatever state its dispatch committed — no events are
// appended, nothing retries — so only the capacity comes back.
func (s *Session) handleAbandoned(ctx context.Context, lease *Lease) {
	s.completeSlot(ctx, attemptKey{runID: lease.RunID, invocationID: lease.InvocationID, attemptID: lease.AttemptID})
	s.retryParked(ctx)
}
