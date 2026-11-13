package teams

import (
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

var (
	// ErrInvalidTransition indicates a state transition is not permitted from the current state.
	ErrInvalidTransition = errors.New("teams: invalid state transition")
	// ErrChildrenNotTerminal indicates a team manager tried to enter checking with non-terminal children.
	ErrChildrenNotTerminal = errors.New("teams: manager children are not terminal")
	// ErrInvalidRole indicates an unknown or disallowed team hierarchy role.
	ErrInvalidRole = errors.New("teams: invalid role")
	// ErrMemberCannotHaveChildren indicates a member or solo task was assigned child tasks.
	ErrMemberCannotHaveChildren = errors.New("teams: member cannot have children")
	// ErrDepthExceeded indicates the task hierarchy exceeds the maximum depth of 2.
	ErrDepthExceeded = errors.New("teams: hierarchy depth exceeds 2")
	// ErrInvalidSpec indicates a task specification failed validation.
	ErrInvalidSpec = errors.New("teams: invalid task spec")
	// ErrCyclicDependency indicates a cycle in depends_on or parent links.
	ErrCyclicDependency = errors.New("teams: cyclic dependency")
	// ErrMissingDependency indicates depends_on references a missing or cross-team task.
	ErrMissingDependency = errors.New("teams: missing or cross-team dependency")
	// ErrInvalidCatalog indicates a role catalog entry failed validation.
	ErrInvalidCatalog = errors.New("teams: invalid role catalog")
)

// TransitionError describes a rejected state transition for a task or team.
type TransitionError struct {
	Entity string
	From   string
	Event  string
	Reason error
}

// Error implements error without exposing protected payload content.
func (e *TransitionError) Error() string {
	if e.Reason != nil {
		return fmt.Sprintf("teams: %s cannot apply %q from %q: %v", e.Entity, e.Event, e.From, e.Reason)
	}
	return fmt.Sprintf("teams: %s cannot apply %q from %q", e.Entity, e.Event, e.From)
}

// Unwrap returns the underlying sentinel error.
func (e *TransitionError) Unwrap() error {
	if e.Reason != nil {
		return e.Reason
	}
	return ErrInvalidTransition
}

// Is allows errors.Is matching against ErrInvalidTransition as well as the specific Reason.
func (e *TransitionError) Is(target error) bool {
	return target == ErrInvalidTransition
}

// Code maps transition errors to errcode.FailedPrecondition.
func (e *TransitionError) Code() errcode.Code {
	return errcode.FailedPrecondition
}

// ValidationError describes a domain validation failure with a field name and reason.
type ValidationError struct {
	Field  string
	Detail string
	Reason error
}

// Error implements error without including secret or protected field values.
func (e *ValidationError) Error() string {
	if e.Detail == "" {
		return "teams: invalid " + e.Field
	}
	return fmt.Sprintf("teams: invalid %s: %s", e.Field, e.Detail)
}

// Unwrap returns the underlying sentinel error.
func (e *ValidationError) Unwrap() error {
	return e.Reason
}

// Code maps validation errors to errcode.InvalidArgument.
func (e *ValidationError) Code() errcode.Code {
	return errcode.InvalidArgument
}
