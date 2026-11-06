package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	timerBackoffStart  = time.Second
	timerBackoffMax    = 30 * time.Second
	firedTTL           = 10 * time.Minute
)

var errUnroutableSession = errcode.New(errcode.NotFound, "unroutable session")

type (
	firedEntry struct {
		generation uint64
		firedAt    time.Time
	}
	scheduledTimer struct {
		ArmedTimer
		retryAfter time.Time
		backoff    time.Duration
		parked     bool
	}
	fireResult struct {
		timer *scheduledTimer
		reply *Reply
		err   error
	}
	// SchedulerOption configures the scheduler upon startup.
	SchedulerOption func(*Scheduler)
)

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
	fireCancel  context.CancelFunc
	task        *supervise.Task
	stopOnce    sync.Once
	fireWg      sync.WaitGroup

	onFire    func(*ArmedTimer)
	onArm     func(*ArmedTimer)
	onParked  func(*ArmedTimer)
	onDropped func(*ArmedTimer)
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

	fireCtx, fireCancel := context.WithCancel(ctx)
	sched := &Scheduler{
		store:       store,
		clk:         clk,
		route:       route,
		armMu:       sync.Mutex{},
		pendingArms: nil,
		wakeCh:      make(chan struct{}, 1),
		fireResCh:   make(chan fireResult, 64),
		stopCh:      make(chan struct{}),
		fireCancel:  fireCancel,
		task:        nil,
		stopOnce:    sync.Once{},
		fireWg:      sync.WaitGroup{},
		onFire:      nil,
		onArm:       nil,
		onParked:    nil,
		onDropped:   nil,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(sched)
		}
	}

	sched.task = supervise.Go(ctx, "coordinator.timers", func(loopCtx context.Context) error {
		return sched.loop(loopCtx, fireCtx, initial)
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
		if s.fireCancel != nil {
			s.fireCancel()
		}
		if s.task != nil {
			s.task.Stop()
			<-s.task.Done()
		}
		s.fireWg.Wait()
	})
}

func (s *Scheduler) notifyArm(t *ArmedTimer) {
	if s.onArm != nil {
		s.onArm(t)
	}
}

func (s *Scheduler) handleWake(active map[string]*scheduledTimer, fired map[string]firedEntry) []*ArmedTimer {
	for _, t := range active {
		t.parked = false
	}
	return s.drainArms(active, fired)
}

func (s *Scheduler) notifySettledFire(firedT, retriedT, parkedT, droppedT *ArmedTimer) {
	switch {
	case retriedT != nil && s.onArm != nil:
		s.onArm(retriedT)
	case firedT != nil && s.onFire != nil:
		s.onFire(firedT)
	case parkedT != nil && s.onParked != nil:
		s.onParked(parkedT)
	case droppedT != nil && s.onDropped != nil:
		s.onDropped(droppedT)
	}
}

func (s *Scheduler) loop(ctx, fireCtx context.Context, initial []ArmedTimer) error {
	active := make(map[string]*scheduledTimer)
	fired := make(map[string]firedEntry)
	inFlight := make(map[string]uint64)

	for _, t := range initial {
		tCopy := t
		if cur, exists := active[t.TimerID]; !exists || t.Generation >= cur.Generation {
			active[t.TimerID] = &scheduledTimer{ArmedTimer: tCopy}
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
		s.cleanFired(fired, s.clk.Now())
		s.fireDueTimers(fireCtx, active, fired, inFlight)
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
			arms := s.handleWake(active, fired)
			recompute()
			for _, t := range arms {
				s.notifyArm(t)
			}
		case res := <-s.fireResCh:
			firedT, retriedT, parkedT, droppedT := s.onFireCompleted(res, active, fired, inFlight)
			recompute()
			s.notifySettledFire(firedT, retriedT, parkedT, droppedT)
		case <-timerCh:
			recompute()
		}
	}
}

func (s *Scheduler) cleanFired(fired map[string]firedEntry, now time.Time) {
	for id, entry := range fired {
		if now.Sub(entry.firedAt) > firedTTL {
			delete(fired, id)
		}
	}
}

func (s *Scheduler) onFireCompleted(
	res fireResult,
	active map[string]*scheduledTimer,
	fired map[string]firedEntry,
	inFlight map[string]uint64,
) (*ArmedTimer, *ArmedTimer, *ArmedTimer, *ArmedTimer) {
	delete(inFlight, res.timer.TimerID)
	return s.handleFireResult(res, active, fired)
}

func (s *Scheduler) drainArms(active map[string]*scheduledTimer, fired map[string]firedEntry) []*ArmedTimer {
	s.cleanFired(fired, s.clk.Now())
	s.armMu.Lock()
	arms := s.pendingArms
	s.pendingArms = nil
	s.armMu.Unlock()

	for _, t := range arms {
		if entry, ok := fired[t.TimerID]; ok && t.Generation > entry.generation {
			delete(fired, t.TimerID)
		}
		if fired[t.TimerID].generation < t.Generation {
			cur, exists := active[t.TimerID]
			if !exists || t.Generation >= cur.Generation {
				active[t.TimerID] = &scheduledTimer{ArmedTimer: *t}
			}
		}
	}
	return arms
}

func (s *Scheduler) dueTimers(active map[string]*scheduledTimer, now time.Time) []*scheduledTimer {
	var due []*scheduledTimer
	for _, t := range active {
		if t.parked {
			continue
		}
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
	fireCtx context.Context,
	active map[string]*scheduledTimer,
	fired map[string]firedEntry,
	inFlight map[string]uint64,
) {
	due := s.dueTimers(active, s.clk.Now())
	for _, t := range due {
		if entry, ok := fired[t.TimerID]; ok && entry.generation >= t.Generation {
			delete(active, t.TimerID)
			continue
		}
		if inFlight[t.TimerID] >= t.Generation {
			continue
		}
		inFlight[t.TimerID] = t.Generation
		s.launchFire(fireCtx, t)
	}
}

func (s *Scheduler) launchFire(fireCtx context.Context, t *scheduledTimer) {
	s.fireWg.Add(1)
	supervise.Go(fireCtx, "coordinator.timer.fire", func(taskCtx context.Context) error {
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
			Kind:             "TimerFired",
			Principal:        defaultTimerOrigin,
			Target:           t.RunID,
			MetadataInput:    []byte("{}"),
			ProtectedInput:   nil,
			Payloads:         nil,
		}

		rep, err := sess.Submit(taskCtx, req)
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
	fired map[string]firedEntry,
) (firedT, retriedT, parkedT, droppedT *ArmedTimer) {
	t := res.timer
	if res.err == nil {
		fired[t.TimerID] = firedEntry{generation: t.Generation, firedAt: s.clk.Now()}
		delete(active, t.TimerID)
		return &t.ArmedTimer, nil, nil, nil
	}

	var rej kernel.Rejection
	if errors.As(res.err, &rej) {
		if rej.Code == kernel.RejectionCodeEarlyTimer {
			t.retryAfter = time.Time{}
			t.backoff = 0
			active[t.TimerID] = t
			return nil, &t.ArmedTimer, nil, nil
		}
		slog.Warn("timer fire rejected", "timer_id", t.TimerID, "code", string(rej.Code))
		delete(active, t.TimerID)
		return nil, nil, nil, &t.ArmedTimer
	}

	if errors.Is(res.err, ErrRecoveryRequired) || errors.Is(res.err, errUnroutableSession) {
		t.parked = true
		t.retryAfter = time.Time{}
		t.backoff = 0
		active[t.TimerID] = t
		return nil, nil, &t.ArmedTimer, nil
	}

	if errors.Is(res.err, context.Canceled) {
		select {
		case <-s.stopCh:
			delete(active, t.TimerID)
			return nil, nil, nil, nil
		default:
		}
	}

	if t.backoff == 0 {
		t.backoff = timerBackoffStart
	} else {
		t.backoff = min(t.backoff*2, timerBackoffMax)
	}
	t.retryAfter = s.clk.Now().Add(t.backoff)
	active[t.TimerID] = t
	return nil, &t.ArmedTimer, nil, nil
}

func earliestTarget(active map[string]*scheduledTimer, inFlight map[string]uint64) (time.Time, bool) {
	var (
		earliestTime  time.Time
		earliestTimer *scheduledTimer
	)
	for _, t := range active {
		if t.parked || inFlight[t.TimerID] >= t.Generation {
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
	return earliestTime, earliestTimer != nil
}

func (s *Scheduler) updateTimer(
	active map[string]*scheduledTimer,
	inFlight map[string]uint64,
	timer clock.Timer,
) (clock.Timer, <-chan time.Time) {
	earliestTime, ok := earliestTarget(active, inFlight)
	if !ok {
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
