package errcode

import (
	"context"
	"errors"
)

// Code is a stable, machine-readable error identifier. Codes are part of the
// Host API contract and never change meaning.
type Code string

// The codes. Each must appear in docs/error-codes.md as public or hidden;
// TestErrorCodesPublished enforces it and TestRegistryCoversEveryCode keeps
// Registry in step.
const (
	Internal           Code = "internal"
	InvalidArgument    Code = "invalid_argument"
	NotFound           Code = "not_found"
	AlreadyExists      Code = "already_exists"
	FailedPrecondition Code = "failed_precondition"
	PermissionDenied   Code = "permission_denied"
	Unavailable        Code = "unavailable"
	Canceled           Code = "canceled"
	DeadlineExceeded   Code = "deadline_exceeded"
	UnsupportedVersion Code = "unsupported_version"
)

// Entry classifies one code. Hidden codes are never shown to a client; the
// reason says why.
type Entry struct {
	Code   Code
	Public bool
	Note   string
}

// Registry lists every code with its classification.
func Registry() []Entry {
	return []Entry{
		{Internal, false, "an unexpected failure; its detail stays in the host's logs"},
		{InvalidArgument, true, "the request is malformed or a value is out of range"},
		{NotFound, true, "the named thing does not exist"},
		{AlreadyExists, true, "the thing to create already exists"},
		{FailedPrecondition, true, "the system is not in a state that allows the operation"},
		{PermissionDenied, true, "the caller is not allowed to do this"},
		{Unavailable, true, "a dependency is temporarily out of reach; retrying may help"},
		{Canceled, true, "the operation was canceled by its caller"},
		{DeadlineExceeded, true, "the operation took longer than its deadline"},
		{UnsupportedVersion, true, "the client and the host share no Host API version; the typed detail names the host's supported versions"},
	}
}

// IsPublic reports whether clients may see the code.
func (c Code) IsPublic() bool {
	for _, e := range Registry() {
		if e.Code == c {
			return e.Public
		}
	}
	return false
}

// Error is an error with a stable Code. Message is for people and must never
// contain a secret or protected payload.
type Error struct {
	code    Code
	message string
	cause   error
}

// New returns an Error with no underlying cause.
func New(code Code, message string) *Error {
	return &Error{code: code, message: message, cause: nil}
}

// Wrap returns an Error that wraps cause; errors.Is and errors.As see through it.
func Wrap(code Code, message string, cause error) *Error {
	return &Error{code: code, message: message, cause: cause}
}

// Code returns the error's code.
func (e *Error) Code() Code { return e.code }

// Message returns the human-readable message without the cause.
func (e *Error) Message() string { return e.message }

// Error implements error. The cause is included for logs; use Expose for
// anything a client sees.
func (e *Error) Error() string {
	if e.cause == nil {
		return string(e.code) + ": " + e.message
	}
	return string(e.code) + ": " + e.message + ": " + e.cause.Error()
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.cause }

// CodeOf returns the code of the first Error in err's chain. Context
// cancellation and deadline errors map to their codes; anything else is Internal.
func CodeOf(err error) Code {
	var coded *Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &coded):
		return coded.code
	case errors.Is(err, context.Canceled):
		return Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return DeadlineExceeded
	default:
		return Internal
	}
}

// genericMessage is all a client learns of a hidden or uncoded failure.
const genericMessage = "internal error"

// Expose returns the code and message a client may see for err. A public code
// keeps its message; everything else, including uncoded errors, becomes Internal
// with a generic message so no cause or detail leaks.
func Expose(err error) (Code, string) {
	var coded *Error
	code := CodeOf(err)
	if code.IsPublic() && errors.As(err, &coded) {
		return code, coded.message
	}
	if code.IsPublic() {
		return code, string(code)
	}
	return Internal, genericMessage
}
