package coordinator

import (
	"context"
	"time"

	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func (s *Session) handleUnknownCommit(
	ctx context.Context,
	sub submission,
	t *Transition,
	ref string,
	events []kernel.Event,
	prep *kernel.PrepareDispatchCommand,
) {
	s.mu.Lock()
	onR := s.onResolve
	s.mu.Unlock()
	if onR != nil {
		onR(s.cfg.Clock.Now())
	}

	res, receipt, err := s.cfg.Store.ResolveCommit(ctx, t.CommandID)
	if err == nil {
		if res == CommitResolutionCommitted {
			fence := t.Fence.AdmissionToken
			for _, ev := range events {
				switch dev := ev.(type) {
				case *kernel.ModelDispatchStartedEvent:
					fence = dev.ExecutorFence
				case *kernel.ToolDispatchStartedEvent:
					fence = dev.ExecutorFence
				}
			}
			s.onDispatchCommitted(t.RunID, prep, ref, fence, events, AppendResult{
				Duplicate:         false,
				Receipt:           receipt,
				RunRevision:       t.ExpectedRunRevision + 1,
				AdmissionRevision: t.ExpectedAdmissionRevision,
			}, sub)
			return
		}
		if res == CommitResolutionNotCommitted {
			s.mu.Lock()
			_ = s.slots.release(ref)
			s.mu.Unlock()

			newRef, ok := s.slots.reserve()
			if ok {
				s.executeDispatch(ctx, sub, newRef, prep)
			} else {
				s.mu.Lock()
				s.parked = append(s.parked, sub)
				s.mu.Unlock()
			}
			return
		}
	}

	s.mu.Lock()
	s.halted = true
	s.mu.Unlock()

	supervise.Go(ctx, "coordinator.commit_resolver", func(rCtx context.Context) error {
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
				return nil
			case <-s.stopped:
				timer.Stop()
				return nil
			case <-timer.C():
				now := s.cfg.Clock.Now()
				s.mu.Lock()
				onResolveHook := s.onResolve
				s.mu.Unlock()
				if onResolveHook != nil {
					onResolveHook(now)
				}

				cRes, cReceipt, cErr := s.cfg.Store.ResolveCommit(rCtx, t.CommandID)
				if cErr == nil {
					if cRes == CommitResolutionCommitted {
						s.mu.Lock()
						s.halted = false
						s.mu.Unlock()

						fence := t.Fence.AdmissionToken
						for _, ev := range events {
							switch dev := ev.(type) {
							case *kernel.ModelDispatchStartedEvent:
								fence = dev.ExecutorFence
							case *kernel.ToolDispatchStartedEvent:
								fence = dev.ExecutorFence
							}
						}
						s.onDispatchCommitted(t.RunID, prep, ref, fence, events, AppendResult{
							Duplicate:         false,
							Receipt:           cReceipt,
							RunRevision:       t.ExpectedRunRevision + 1,
							AdmissionRevision: t.ExpectedAdmissionRevision,
						}, sub)
						return nil
					}
					if cRes == CommitResolutionNotCommitted {
						s.mu.Lock()
						s.halted = false
						_ = s.slots.release(ref)
						s.mu.Unlock()

						newRef, ok := s.slots.reserve()
						if ok {
							s.executeDispatch(rCtx, sub, newRef, prep)
						} else {
							s.mu.Lock()
							s.parked = append(s.parked, sub)
							s.mu.Unlock()
						}
						return nil
					}
				}

				delay *= 2
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
			}
		}
	})
}
