package process

import (
	"github.com/syurodev/fugue/internal/platform/errcode"
)

var (
	// ErrBinaryNotFound indicates the executable binary could not be found.
	ErrBinaryNotFound = errcode.New(errcode.NotFound, "process binary not found")

	// ErrNotExecutable indicates the file exists but lacks executable permission.
	ErrNotExecutable = errcode.New(errcode.PermissionDenied, "binary is not executable")

	// ErrStartFailed indicates process execution failed during launch.
	ErrStartFailed = errcode.New(errcode.Internal, "process start failed")

	// ErrFrameTooLarge indicates a stdout JSON frame exceeded the 8 MiB size limit.
	ErrFrameTooLarge = errcode.New(errcode.FrameTooLarge, "frame exceeds 8 MiB limit")

	// ErrUnsupportedPlatform indicates process supervision is unavailable on this OS.
	ErrUnsupportedPlatform = errcode.New(errcode.Unavailable, "process supervisor is unsupported on this platform")

	// ErrIdentityMismatch indicates a live PID's start token does not match the
	// recorded process identity or the identity cannot be verified safely.
	ErrIdentityMismatch = errcode.New(errcode.FailedPrecondition, "process identity does not match live process")

	// ErrStdinClosed indicates a write was attempted after CloseStdin or process termination.
	ErrStdinClosed = errcode.New(errcode.FailedPrecondition, "process stdin is closed")
)
