//go:build !linux && !darwin

package hostconn

import (
	"context"
	"errors"
)

// ErrUnsafeExecutable means the host executable cannot be authenticated.
var ErrUnsafeExecutable = errors.New("unsafe host executable")

type launchedChildren struct {
	host, log <-chan error
	exit      *hostExit
}

func validateExecutable(_ string) error                { return ErrUnsafeExecutable }
func validateExecutableFor(_ string, _, _ int64) error { return ErrUnsafeExecutable }
func launchDetached(_ context.Context, _, _ string, _ []string) (launchedChildren, error) {
	return launchedChildren{}, ErrUnsafeExecutable
}
