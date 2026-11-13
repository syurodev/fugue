package workspace

import (
	"errors"
	"fmt"
)

// Code is a machine-readable error identifier for workspace operations.
type Code string

// Workspace error codes.
const (
	ErrOwnershipInvalid     Code = "workspace.ownership_invalid"
	ErrPathDenied           Code = "workspace.path_denied"
	ErrRootMissing          Code = "workspace.root_missing"
	ErrRootInvalid          Code = "workspace.root_invalid"
	ErrNotFound             Code = "workspace.not_found"
	ErrAlreadyExists        Code = "workspace.already_exists"
	ErrConfigInvalid        Code = "workspace.config_invalid"
	ErrConfigUnconfirmed    Code = "workspace.config_unconfirmed"
	ErrConfigChanged        Code = "workspace.config_changed"
	ErrIDInvalid            Code = "workspace.id_invalid"
	ErrConfirmationRequired Code = "workspace.confirmation_required"
	ErrDeleteFailed         Code = "workspace.delete_failed"
)

// Error is a typed workspace error with a stable machine-readable Code.
type Error struct {
	Code         Code
	Message      string
	File, Key    string
	Line, Column int
	Cause        error
}

func (e *Error) Error() string {
	var meta string
	if e.File != "" {
		meta += fmt.Sprintf("[file %s] ", e.File)
	}
	if e.Line > 0 && e.Column > 0 {
		meta += fmt.Sprintf("[line %d, col %d] ", e.Line, e.Column)
	}
	if e.Key != "" {
		meta += fmt.Sprintf("[key %s] ", e.Key)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s%s: %v", e.Code, meta, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s%s", e.Code, meta, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Position returns the 1-based line and column where the error occurred, if known.
func (e *Error) Position() (int, int, bool) {
	return e.Line, e.Column, e.Line > 0 && e.Column > 0
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
