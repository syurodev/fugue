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
	FrameTooLarge      Code = "frame_too_large"

	InteractionNotFound         Code = "interaction.not_found"
	InteractionAlreadyAnswered  Code = "interaction.already_answered"
	InteractionStaleIncarnation Code = "interaction.stale_incarnation"
	InteractionOptionNotOffered Code = "interaction.option_not_offered"
	InteractionTextTooLarge     Code = "interaction.text_too_large"
	WorkspaceNotFound           Code = "workspace.not_found"
	WorkspaceAlreadyExists      Code = "workspace.already_exists"
	WorkspaceConfigInvalid      Code = "workspace.config_invalid"
	WorkspaceConfigChanged      Code = "workspace.config_changed"
	WorkspaceConfigUnconfirmed  Code = "workspace.config_unconfirmed"
	WorkspaceRootInvalid        Code = "workspace.root_invalid"
	WorkspaceRootMissing        Code = "workspace.root_missing"
	WorkspacePathDenied         Code = "workspace.path_denied"
	WorkspaceOwnershipInvalid   Code = "workspace.ownership_invalid"
	WorkspaceConfigTooLarge     Code = "workspace.config_too_large"
	VendorUnsupported           Code = "vendor.unsupported"
	VendorSessionNotFound       Code = "vendor.session_not_found"
	VendorSessionState          Code = "vendor.session_state"
	VendorTurnNotActive         Code = "vendor.turn_not_active"
	VendorCapacity              Code = "vendor.capacity"
	VendorVaultLocked           Code = "vendor.vault_locked"
	VendorInvalidProfile        Code = "vendor.invalid_profile"
	VendorUnknownCommand        Code = "vendor.unknown_command"
	VendorCommandConflict       Code = "vendor.command_conflict"
	VendorWorkspaceMismatch     Code = "vendor.workspace_mismatch"
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
		{FrameTooLarge, true, "a stream frame exceeded the size limit"},
		{InteractionNotFound, true, "the interaction request does not exist or is no longer available"},
		{InteractionAlreadyAnswered, true, "the interaction request has already been answered"},
		{InteractionStaleIncarnation, true, "the interaction request belongs to a previous host incarnation"},
		{InteractionOptionNotOffered, true, "the selected outcome or option was not offered"},
		{InteractionTextTooLarge, true, "the interaction answer text or deny note exceeds the size limit"},
		{WorkspaceNotFound, true, "the workspace does not exist"},
		{WorkspaceAlreadyExists, true, "the workspace already exists"},
		{WorkspaceConfigInvalid, true, "the workspace configuration is invalid"},
		{WorkspaceConfigChanged, true, "the workspace configuration changed since it was reviewed"},
		{WorkspaceConfigUnconfirmed, true, "the workspace configuration requires confirmation"},
		{WorkspaceRootInvalid, true, "the workspace root is invalid"},
		{WorkspaceRootMissing, true, "the workspace root is missing"},
		{WorkspacePathDenied, true, "the workspace path is not allowed"},
		{WorkspaceOwnershipInvalid, true, "workspace ownership or permissions are invalid"},
		{WorkspaceConfigTooLarge, true, "the workspace configuration exceeds the size limit"},
		{VendorUnsupported, true, "the vendor or operation is not supported in this phase"},
		{VendorSessionNotFound, true, "the vendor session does not exist or is not live"},
		{VendorSessionState, true, "the vendor session state does not allow the operation (busy, closed or not resumable)"},
		{VendorTurnNotActive, true, "no vendor turn is running"},
		{VendorCapacity, true, "the host already runs the maximum number of vendor sessions"},
		{VendorVaultLocked, true, "the vault is locked; vendor sessions cannot start or take input"},
		{VendorInvalidProfile, true, "a session profile value is not allowed"},
		{VendorUnknownCommand, true, "the /fugue: command is not available; it never reaches the vendor"},
		{VendorCommandConflict, true, "the command_id was already used for a different command"},
		{VendorWorkspaceMismatch, true, "the session is bound to the workspace root it was created in"},
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
