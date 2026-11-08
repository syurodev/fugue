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
)
