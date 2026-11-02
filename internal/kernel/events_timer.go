package kernel

import "time"

// TimeBudgetWindowOpenedEvent records the opening of an active execution time window.
type TimeBudgetWindowOpenedEvent struct {
	Envelope                   Envelope
	WindowID                   string
	Generation                 uint64
	Incarnation                uint64
	TrancheReservationDuration time.Duration
	Policy                     string
	MonotonicBaselineNano      int64
}

// EventName returns the canonical event name.
func (*TimeBudgetWindowOpenedEvent) EventName() string { return "TimeBudgetWindowOpened" }

// EventEnvelope returns the event envelope.
func (e *TimeBudgetWindowOpenedEvent) EventEnvelope() Envelope { return e.Envelope }

// TimeBudgetWindowClosedEvent records reconciliation and closure of an active execution time window.
type TimeBudgetWindowClosedEvent struct {
	Envelope               Envelope
	WindowID               string
	MeasuredElapsed        time.Duration
	TailCoverage           string
	ReservationDisposition string
}

// EventName returns the canonical event name.
func (*TimeBudgetWindowClosedEvent) EventName() string { return "TimeBudgetWindowClosed" }

// EventEnvelope returns the event envelope.
func (e *TimeBudgetWindowClosedEvent) EventEnvelope() Envelope { return e.Envelope }

// TimerScheduledEvent records durable scheduling of a timer for retries, deadlines, or expirations.
type TimerScheduledEvent struct {
	Envelope           Envelope
	TimerID            string
	Generation         uint64
	Purpose            string
	TargetID           string
	ClockDomain        string
	SchedulingDeadline time.Time
	Duration           time.Duration
	PolicyVersion      string
}

// EventName returns the canonical event name.
func (*TimerScheduledEvent) EventName() string { return "TimerScheduled" }

// EventEnvelope returns the event envelope.
func (e *TimerScheduledEvent) EventEnvelope() Envelope { return e.Envelope }

// TimerElapsedEvent records that a scheduled timer has elapsed and resolved its dependency.
type TimerElapsedEvent struct {
	Envelope     Envelope
	TimerID      string
	Generation   uint64
	ObservedTime time.Time
}

// EventName returns the canonical event name.
func (*TimerElapsedEvent) EventName() string { return "TimerElapsed" }

// EventEnvelope returns the event envelope.
func (e *TimerElapsedEvent) EventEnvelope() Envelope { return e.Envelope }

// TimerCancelledEvent records cancellation of a pending timer before it fired.
type TimerCancelledEvent struct {
	Envelope   Envelope
	TimerID    string
	Generation uint64
	CauseRef   string
}

// EventName returns the canonical event name.
func (*TimerCancelledEvent) EventName() string { return "TimerCancelled" }

// EventEnvelope returns the event envelope.
func (e *TimerCancelledEvent) EventEnvelope() Envelope { return e.Envelope }

// BudgetObservedEvent records usage observations and conservative accounting checkpoints.
type BudgetObservedEvent struct {
	Envelope     Envelope
	AttemptID    string
	WindowID     string
	TokensUsed   int
	DurationUsed time.Duration
	Basis        string
}

// EventName returns the canonical event name.
func (*BudgetObservedEvent) EventName() string { return "BudgetObserved" }

// EventEnvelope returns the event envelope.
func (e *BudgetObservedEvent) EventEnvelope() Envelope { return e.Envelope }
