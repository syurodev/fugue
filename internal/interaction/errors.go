package interaction

import "github.com/syurodev/fugue/internal/platform/errcode"

// Exported sentinel errors for interaction domain operations.
var (
	ErrRequestNotFound    = errcode.New(errcode.NotFound, "interaction request not found")
	ErrOperationNotFound  = errcode.New(errcode.NotFound, "interaction operation not found")
	ErrAlreadyAnswered    = errcode.New(errcode.FailedPrecondition, "interaction request has already been answered")
	ErrStaleIncarnation   = errcode.New(errcode.FailedPrecondition, "interaction request belongs to a previous incarnation")
	ErrOptionNotOffered   = errcode.New(errcode.InvalidArgument, "interaction option was not offered")
	ErrOutcomeNotOffered  = errcode.New(errcode.InvalidArgument, "interaction outcome was not offered")
	ErrSessionGrantDenied = errcode.New(errcode.InvalidArgument, "requests that always ask never grant session grants")
	ErrRelayedGrantDenied = errcode.New(errcode.InvalidArgument, "relayed requests never create a Fugue approval")
	ErrTextTooLarge       = errcode.New(errcode.InvalidArgument, "interaction text exceeds size limit")
	ErrVaultLocked        = errcode.New(errcode.FailedPrecondition, "vault is locked")
)
