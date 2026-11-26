//go:build !darwin && !linux

package osappearance

import "context"

type otherSource struct{}

// New returns a Source for platforms without OS appearance integration.
func New() Source {
	return otherSource{}
}

func (otherSource) Current() Appearance {
	return Unknown
}

func (otherSource) Watch(ctx context.Context, _ func(Appearance)) {
	<-ctx.Done()
}
