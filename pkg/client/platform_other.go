//go:build !linux && !darwin

package client

import (
	"context"
	"net"
	"os"
)

// BackgroundLock is unsupported on this platform.
type BackgroundLock struct{}

// BackgroundEnabled fails closed on unsupported platforms.
func BackgroundEnabled(_ string) (bool, error) { return false, ErrUntrustedHost }

// SetBackgroundEnabled fails closed on unsupported platforms.
func SetBackgroundEnabled(_ string, _ bool) error { return ErrUntrustedHost }

// TryBackgroundLock fails closed on unsupported platforms.
func TryBackgroundLock(_ string) (*BackgroundLock, error) { return nil, ErrUntrustedHost }

// Close releases the lock.
func (*BackgroundLock) Close() error { return ErrUntrustedHost }

// OpenHostLog fails closed on unsupported platforms.
func OpenHostLog(_ string) (*os.File, error) { return nil, ErrUntrustedHost }

func dialTrustedSocket(_ context.Context, _ string) (net.Conn, error) { return nil, ErrUntrustedHost }
