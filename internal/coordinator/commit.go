package coordinator

import (
	"context"
	"time"

	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func resolveDispatchFence(defaultFence uint64, events []kernel.Event) uint64 {
	fence := defaultFence
	for _, ev := range events {
		switch dev := ev.(type) {
		case *kernel.ModelDispatchStartedEvent:
			fence = dev.ExecutorFence
		case *kernel.ToolDispatchStartedEvent:
			fence = dev.ExecutorFence
		}
	}
	return fence
}

func (s *Session) resolveSynchronous(
	ctx context.Context,
	sub *submission,
	t *Transition,
	ref string,
	events []kernel.Event,
	prep *kernel.PrepareDispatchCommand,
) bool {
	s.mu.Lock()
	onR := s.onResolve
	s.mu.Unlock()
	if onR != nil {
		onR(s.cfg.Clock.Now())
	}

	res, receipt, err := s.cfg.Store.ResolveCommit(ctx, t.CommandID)
	if err != nil {
		return false
	}
	if res == CommitResolutionCommitted {
		fence := resolveDispatchFence(t.Fence.AdmissionToken, events)
		s.onDispatchCommitted(t.RunID, prep, ref, fence, events, &AppendResult{
			Duplicate:         false,
			Receipt:           receipt,
			RunRevision:       t.ExpectedRunRevision + 1,
			AdmissionRevision: t.ExpectedAdmissionRevision,
		}, sub)
		return true
	}
	if res == CommitResolutionNotCommitted {
		s.mu.Lock()
		if err := s.slots.release(ref); err != nil {
			s.mu.Unlock()
			return false
		}
		s.mu.Unlock()

		newRef, ok := s.slots.reserve()
		if ok {
			s.executeDispatch(ctx, sub, newRef, prep)
		} else {
			s.mu.Lock()
			s.parked = append(s.parked, *sub)
			s.mu.Unlock()
		}
		return true
	}
	return false
}

func (s *Session) handleUnknownCommit(
	ctx context.Context,
	sub *submission,
	t *Transition,
	ref string,
	events []kernel.Event,
	prep *kernel.PrepareDispatchCommand,
) {
	if s.resolveSynchronous(ctx, sub, t, ref, events, prep) {
		return
	}

	s.mu.Lock()
	s.halted = true
	s.mu.Unlock()

	supervise.Go(ctx, "coordinator.commit_resolver", func(rCtx context.Context) error {
		s.runCommitResolver(rCtx, sub, t, ref, events, prep)
		return nil
	})
}

func (s *Session) runCommitResolver(
	rCtx context.Context,
	sub *submission,
	t *Transition,
	ref string,
	events []kernel.Event,
	prep *kernel.PrepareDispatchCommand,
) {
	delay := 1 * time.Second
	for {
		timer := s.cfg.Clock.NewTimer(delay)
		s.mu.Lock()
		onArm := s.onTimerArmed
		s.mu.Unlock()
		if onArm != nil {
			onArm()
		}

		select {
		case <-rCtx.Done():
			timer.Stop()
			return
		case <-s.stopped:
			timer.Stop()
			return
		case <-timer.C():
			if s.checkResolverCommit(rCtx, sub, t, ref, events, prep) {
				return
			}
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func (s *Session) checkResolverCommit(
	rCtx context.Context,
	sub *submission,
	t *Transition,
	ref string,
	events []kernel.Event,
	prep *kernel.PrepareDispatchCommand,
) bool {
	s.mu.Lock()
	onResolveHook := s.onResolve
	s.mu.Unlock()
	if onResolveHook != nil {
		onResolveHook(s.cfg.Clock.Now())
	}

	cRes, cReceipt, cErr := s.cfg.Store.ResolveCommit(rCtx, t.CommandID)
	if cErr != nil {
		return false
	}

	if cRes == CommitResolutionCommitted {
		s.mu.Lock()
		s.halted = false
		s.mu.Unlock()

		fence := resolveDispatchFence(t.Fence.AdmissionToken, events)
		s.onDispatchCommitted(t.RunID, prep, ref, fence, events, &AppendResult{
			Duplicate:         false,
			Receipt:           cReceipt,
			RunRevision:       t.ExpectedRunRevision + 1,
			AdmissionRevision: t.ExpectedAdmissionRevision,
		}, sub)
		return true
	}

	if cRes == CommitResolutionNotCommitted {
		s.mu.Lock()
		s.halted = false
		if err := s.slots.release(ref); err != nil {
			s.mu.Unlock()
			return false
		}
		s.mu.Unlock()

		newRef, ok := s.slots.reserve()
		if ok {
			s.executeDispatch(rCtx, sub, newRef, prep)
		} else {
			s.mu.Lock()
			s.parked = append(s.parked, *sub)
			s.mu.Unlock()
		}
		return true
	}

	return false
}
