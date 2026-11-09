package app

import (
	"context"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// Start starts background tasks for the interaction service, including
// the automatic expiration timer loop. It returns the supervised task.
func (s *InteractionService) Start(ctx context.Context) *supervise.Task {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.task != nil {
		return s.task
	}
	subCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.task = supervise.Go(subCtx, "app.interaction-expiry", s.expiryLoop)
	return s.task
}

// Close stops the interaction service background tasks and waits for them to terminate.
func (s *InteractionService) Close() error {
	s.taskMu.Lock()
	cancel := s.cancel
	task := s.task
	s.taskMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if task != nil {
		if err := task.Wait(); err != nil {
			return errcode.Wrap(errcode.Internal, "interaction expiry task wait", err)
		}
	}
	return nil
}

func (s *InteractionService) expiryLoop(ctx context.Context) error {
	var (
		timer   clock.Timer
		timerCh <-chan time.Time
	)
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			timerCh = nil
		}
	}
	defer stopTimer()

	rearm := func() {
		stopTimer()
		earliest, hasExpiry := s.owner.EarliestPendingExpiry()
		if !hasExpiry {
			return
		}
		now := s.clk.Now().UTC()
		var d time.Duration
		if earliest.After(now) {
			d = earliest.Sub(now)
		}
		timer = s.clk.NewTimer(d)
		timerCh = timer.C()
	}

	rearm()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.wakeExpiry:
			rearm()
		case <-timerCh:
			s.owner.ExpireExpired()
			rearm()
		}
	}
}
