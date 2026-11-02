package kernel

import "fmt"

// RejectionCode represents a stable machine-readable rejection code.
type RejectionCode string

// Stable rejection codes returned by the kernel when refusing invalid transitions.
const (
	RejectionCodeInvalidState         RejectionCode = "invalid_state"
	RejectionCodeRunNotAdmitted       RejectionCode = "run_not_admitted"
	RejectionCodeRunAlreadyFinished   RejectionCode = "run_already_finished"
	RejectionCodeStaleToken           RejectionCode = "stale_token"
	RejectionCodeStepNotActive        RejectionCode = "step_not_active"
	RejectionCodeStepAlreadyActive    RejectionCode = "step_already_active"
	RejectionCodeAttemptNotActive     RejectionCode = "attempt_not_active"
	RejectionCodeDuplicateAttempt     RejectionCode = "duplicate_attempt"
	RejectionCodeInvalidTransition    RejectionCode = "invalid_transition"
	RejectionCodeStopRequested        RejectionCode = "stop_requested"
	RejectionCodeWaitDependencyActive RejectionCode = "wait_dependency_active"
	RejectionCodeEarlyTimer           RejectionCode = "early_timer"
	RejectionCodeUnrecognizedCommand  RejectionCode = "unrecognized_command"
	RejectionCodeCommandRejected      RejectionCode = "command_rejected"
)

// Rejection represents an explicit refusal of a command by the kernel.
type Rejection struct {
	Code    RejectionCode
	Message string
}

// Error returns a formatted error string for the rejection.
func (r Rejection) Error() string {
	return fmt.Sprintf("kernel rejection [%s]: %s", r.Code, r.Message)
}

// Reject creates a Rejection value with the given code and message.
func Reject(code RejectionCode, message string) Rejection {
	return Rejection{
		Code:    code,
		Message: message,
	}
}
