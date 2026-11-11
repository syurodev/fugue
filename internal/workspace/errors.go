package workspace

import (
	"errors"
	"fmt"
)

// Code is a machine-readable error identifier for workspace operations.
type Code string

// Workspace error codes.
const (
	ErrOwnershipInvalid Code = "workspace.ownership_invalid"
	ErrPathDenied       Code = "workspace.path_denied"
	ErrRootMissing      Code = "workspace.root_missing"
	ErrRootInvalid      Code = "workspace.root_invalid"
	ErrNotFound         Code = "workspace.not_found"
	ErrAlreadyExists    Code = "workspace.already_exists"
)

// Error is a typed workspace error with a stable machine-readable Code.
type Error struct {
	Code    Code
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	return e.Cause
}

// NewError returns a workspace Error with code and message.
func NewError(code Code, message string) *Error { return &Error{Code: code, Message: message} }

// WrapError returns a workspace Error wrapping an underlying cause.
func WrapError(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}

// CodeOf returns the workspace error code from err, or "" if err is not a workspace Error.
func CodeOf(err error) Code {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}
