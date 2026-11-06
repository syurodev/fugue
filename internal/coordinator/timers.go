package coordinator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// SetScheduler attaches a scheduler to this session actor.
func (s *Session) SetScheduler(sched *Scheduler) {
	s.scheduler.Store(sched)
	if sched != nil {
		sched.Wake()
	}
}

func (s *Session) armTimer(t *ArmedTimer) {
	sched := s.scheduler.Load()
	if sched != nil {
		sched.Arm(t)
	}
}

// armCommittedTimers inspects committed events and arms any newly scheduled timers.
func (s *Session) armCommittedTimers(runID string, events []kernel.Event) {
	for _, ev := range events {
		if ts, ok := ev.(*kernel.TimerScheduledEvent); ok {
			s.armTimer(&ArmedTimer{
				TimerID:    ts.TimerID,
				SessionID:  s.cfg.SessionID,
				RunID:      runID,
				Generation: ts.Generation,
				Purpose:    ts.Purpose,
				Deadline:   ts.SchedulingDeadline,
			})
		}
	}
}

const (
	defaultTimerOrigin = "timer"
	timerRetryInterval = 50 * time.Millisecond
)

var errUnroutableSession = errors.New("unroutable session")

type scheduledTimer struct {
	ArmedTimer
	retryAfter time.Time
}

type fireResult struct {
	timer *scheduledTimer
	reply *Reply
	err   error
}

// SchedulerOption configures the scheduler upon startup.
type SchedulerOption func(*Scheduler)

// WithOnFire registers a hook called whenever a timer successfully fires.
func WithOnFire(fn func(*ArmedTimer)) SchedulerOption {
	return func(s *Scheduler) {
		s.onFire = fn
	}
}

// WithOnArm registers a hook called whenever a timer is processed into the scheduler.
func WithOnArm(fn func(*ArmedTimer)) SchedulerOption {
	return func(s *Scheduler) {
		s.onArm = fn
	}
}

// Scheduler coordinates durable timers across sessions using a single clock.Timer
// set to the earliest deadline among active armed timers.
type Scheduler struct {
	store Store
	clk   clock.Clock
	route func(sessionID string) (*Session, bool)

	armMu       sync.Mutex
	pendingArms []*ArmedTimer
	wakeCh      chan struct{}
	fireResCh   chan fireResult
	stopCh      chan struct{}
	task        *supervise.Task
	stopOnce    sync.Once
	fireWg      sync.WaitGroup

	mu     sync.Mutex
	onFire func(*ArmedTimer)
	onArm  func(*ArmedTimer)
}

// StartScheduler loads armed timers from store, starts the timer supervision loop,
// and returns a running Scheduler.
func StartScheduler(
	ctx context.Context,
	store Store,
	clk clock.Clock,
	route func(sessionID string) (*Session, bool),
	opts ...SchedulerOption,
) (*Scheduler, error) {
	if store == nil {
		return nil, errcode.New(errcode.InvalidArgument, "store is nil")
	}
	if clk == nil {
		return nil, errcode.New(errcode.InvalidArgument, "clock is nil")
	}
	if route == nil {
		return nil, errcode.New(errcode.InvalidArgument, "route is nil")
	}

	initial, err := store.ListArmedTimers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list armed timers: %w", err)
	}

	sched := &Scheduler{
		store:       store,
		clk:         clk,
		route:       route,
		armMu:       sync.Mutex{},
		pendingArms: nil,
		wakeCh:      make(chan struct{}, 1),
		fireResCh:   make(chan fireResult, 64),
		stopCh:      make(chan struct{}),
		task:        nil,
		stopOnce:    sync.Once{},
		fireWg:      sync.WaitGroup{},
		mu:          sync.Mutex{},
		onFire:      nil,
		onArm:       nil,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(sched)
		}
	}

	sched.task = supervise.Go(ctx, "coordinator.timers", func(loopCtx context.Context) error {
		return sched.loop(loopCtx, initial)
	})

	return sched, nil
}

// Arm requests scheduling or re-arming a timer. It never blocks the caller.
func (s *Scheduler) Arm(t *ArmedTimer) {
	if t == nil {
		return
	}
	s.armMu.Lock()
	s.pendingArms = append(s.pendingArms, t)
	s.armMu.Unlock()

	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

// Wake prompts the scheduler loop to re-evaluate due timers and pending registrations.
func (s *Scheduler) Wake() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

// Stop terminates the scheduler task and waits for all fire tasks to finish.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		if s.task != nil {
			s.task.Stop()
			<-s.task.Done()
		}
		s.fireWg.Wait()
	})
}

func (s *Scheduler) notifyFire(t *ArmedTimer) {
	s.mu.Lock()
	fn := s.onFire
	s.mu.Unlock()
	if fn != nil {
		fn(t)
	}
}

func (s *Scheduler) notifyArm(t *ArmedTimer) {
	s.mu.Lock()
	fn := s.onArm
	s.mu.Unlock()
	if fn != nil {
		fn(t)
	}
}

func (s *Scheduler) loop(ctx context.Context, initial []ArmedTimer) error {
	active := make(map[string]*scheduledTimer)
	fired := make(map[string]uint64)
	inFlight := make(map[string]uint64)

	for _, t := range initial {
		tCopy := t
		if cur, exists := active[t.TimerID]; !exists || t.Generation >= cur.Generation {
			active[t.TimerID] = &scheduledTimer{ArmedTimer: tCopy, retryAfter: time.Time{}}
		}
	}

	var (
		timer   clock.Timer
		timerCh <-chan time.Time
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	recompute := func() {
		s.fireDueTimers(ctx, active, fired, inFlight)
		timer, timerCh = s.updateTimer(active, inFlight, timer)
	}

	recompute()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stopCh:
			return nil
		case <-s.wakeCh:
			s.drainArms(active, fired)
			recompute()
		case res := <-s.fireResCh:
			s.onFireCompleted(res, active, fired, inFlight)
			recompute()
		case <-timerCh:
			recompute()
		}
	}
}

func (s *Scheduler) onFireCompleted(
	res fireResult,
	active map[string]*scheduledTimer,
	fired, inFlight map[string]uint64,
) {
	delete(inFlight, res.timer.TimerID)
	retried := s.handleFireResult(res, active, fired)
	if retried {
		s.notifyArm(&res.timer.ArmedTimer)
	} else if res.err == nil {
		s.notifyFire(&res.timer.ArmedTimer)
	}
}

func (s *Scheduler) drainArms(active map[string]*scheduledTimer, fired map[string]uint64) {
	s.armMu.Lock()
	arms := s.pendingArms
	s.pendingArms = nil
	s.armMu.Unlock()

	for _, t := range arms {
		s.handleArm(t, active, fired)
		s.notifyArm(t)
	}
}

func (s *Scheduler) handleArm(t *ArmedTimer, active map[string]*scheduledTimer, fired map[string]uint64) {
	if lastFired, ok := fired[t.TimerID]; ok && t.Generation > lastFired {
		delete(fired, t.TimerID)
	}
	if fired[t.TimerID] >= t.Generation {
		return
	}
	cur, exists := active[t.TimerID]
	if !exists || t.Generation >= cur.Generation {
		active[t.TimerID] = &scheduledTimer{ArmedTimer: *t, retryAfter: time.Time{}}
	}
}

func (s *Scheduler) dueTimers(active map[string]*scheduledTimer, now time.Time) []*scheduledTimer {
	var due []*scheduledTimer
	for _, t := range active {
		if !t.Deadline.After(now) && (t.retryAfter.IsZero() || !t.retryAfter.After(now)) {
			due = append(due, t)
		}
	}
	if len(due) == 0 {
		return nil
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].Deadline.Equal(due[j].Deadline) {
			return due[i].TimerID < due[j].TimerID
		}
		return due[i].Deadline.Before(due[j].Deadline)
	})
	return due
}

func (s *Scheduler) fireDueTimers(
	ctx context.Context,
	active map[string]*scheduledTimer,
	fired map[string]uint64,
	inFlight map[string]uint64,
) {
	due := s.dueTimers(active, s.clk.Now())
	for _, t := range due {
		if fired[t.TimerID] >= t.Generation {
			delete(active, t.TimerID)
			continue
		}
		if inFlight[t.TimerID] >= t.Generation {
			continue
		}
		inFlight[t.TimerID] = t.Generation
		s.launchFire(ctx, t)
	}
}

func (s *Scheduler) launchFire(ctx context.Context, t *scheduledTimer) {
	s.fireWg.Add(1)
	supervise.Go(ctx, "coordinator.timer.fire", func(fireCtx context.Context) error {
		defer s.fireWg.Done()
		sess, ok := s.route(t.SessionID)
		if !ok || sess == nil {
			s.postFireResult(fireResult{timer: t, reply: nil, err: errUnroutableSession})
			return nil
		}

		now := s.clk.Now()
		req := &Request{
			Command: &kernel.TimerFiredCommand{
				CommandID:    "",
				RunID:        t.RunID,
				Origin:       defaultTimerOrigin,
				TimerID:      t.TimerID,
				Generation:   t.Generation,
				ObservedTime: now,
			},
			Facts:            kernel.DecisionFacts{},
			Origin:           defaultTimerOrigin,
			QueueDecisionRef: "",
			Kind:             "",
			Principal:        defaultTimerOrigin,
			Target:           t.RunID,
			MetadataInput:    nil,
			ProtectedInput:   nil,
			Payloads:         nil,
		}

		rep, err := sess.Submit(fireCtx, req)
		s.postFireResult(fireResult{timer: t, reply: &rep, err: err})
		return nil
	})
}

func (s *Scheduler) postFireResult(res fireResult) {
	select {
	case <-s.stopCh:
	case s.fireResCh <- res:
	}
}

func (s *Scheduler) handleFireResult(
	res fireResult,
	active map[string]*scheduledTimer,
	fired map[string]uint64,
) bool {
	t := res.timer
	if res.err != nil {
		var rej kernel.Rejection
		if errors.As(res.err, &rej) && rej.Code == kernel.RejectionCodeEarlyTimer {
			return false
		}
		t.retryAfter = s.clk.Now().Add(timerRetryInterval)
		active[t.TimerID] = t
		return true
	}

	fired[t.TimerID] = t.Generation
	delete(active, t.TimerID)
	return false
}

func (s *Scheduler) updateTimer(
	active map[string]*scheduledTimer,
	inFlight map[string]uint64,
	timer clock.Timer,
) (clock.Timer, <-chan time.Time) {
	var (
		earliestTime  time.Time
		earliestTimer *scheduledTimer
	)

	for _, t := range active {
		if inFlight[t.TimerID] >= t.Generation {
			continue
		}
		target := t.Deadline
		if !t.retryAfter.IsZero() && t.retryAfter.After(target) {
			target = t.retryAfter
		}
		if earliestTimer == nil || target.Before(earliestTime) ||
			(target.Equal(earliestTime) && t.TimerID < earliestTimer.TimerID) {
			earliestTime = target
			earliestTimer = t
		}
	}

	if earliestTimer == nil {
		if timer != nil {
			timer.Stop()
		}
		return nil, nil
	}

	d := max(0, earliestTime.Sub(s.clk.Now()))
	if timer == nil {
		timer = s.clk.NewTimer(d)
	} else {
		timer.Reset(d)
	}
	return timer, timer.C()
}
