//go:build !linux && !darwin

package sandbox

import "context"

func defaultBackend() Backend {
	return &unavailableBackend{}
}

type unavailableBackend struct{}

func (unavailableBackend) Name() string { return "unavailable" }

func (unavailableBackend) Available(context.Context) error {
	return ErrUnsupportedPlatform
}

func (unavailableBackend) Start(context.Context, *Policy, Cmd) (*Proc, error) {
	return nil, ErrUnsupportedPlatform
}

func (unavailableBackend) StartPiped(context.Context, *Policy, Cmd) (*Proc, error) {
	return nil, ErrUnsupportedPlatform
}
