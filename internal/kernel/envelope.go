package kernel

import "time"

// Envelope contains common envelope metadata present on every committed domain event.
type Envelope struct {
	RunID       string
	Sequence    uint64
	CommandID   string
	CausationID string
	Origin      string
	Token       uint64
	Timestamp   time.Time
}

// Event is the marker interface implemented by every kernel event struct.
type Event interface {
	EventName() string
	EventEnvelope() Envelope
	isNil() bool
}

// Command is the marker interface implemented by every kernel command struct.
type Command interface {
	CommandName() string
}
