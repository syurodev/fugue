package app

// The scopes a SessionCapacityError names: the machine-wide cap or one
// account's cap.
const (
	CapacityScopeMachine = "machine"
	CapacityScopeAccount = "account"
)

// SessionCapacityError is ErrVendorSessionCapacity with the numbers a client
// needs to explain the refusal: the sessions counted against the tier that
// tripped and that tier's limit. It carries no path, id or account name.
type SessionCapacityError struct {
	Running int
	Limit   int
	Scope   string
}

// Error implements error.
func (e *SessionCapacityError) Error() string { return ErrVendorSessionCapacity.Error() }

// Unwrap makes errors.Is(err, ErrVendorSessionCapacity) hold.
func (e *SessionCapacityError) Unwrap() error { return ErrVendorSessionCapacity }

// RunningSessions is the count the Host API error detail carries.
func (e *SessionCapacityError) RunningSessions() int64 { return int64(e.Running) }

// SessionLimit is the cap the Host API error detail carries.
func (e *SessionCapacityError) SessionLimit() int64 { return int64(e.Limit) }

// SessionLimitScope names the tier that tripped.
func (e *SessionCapacityError) SessionLimitScope() string { return e.Scope }
