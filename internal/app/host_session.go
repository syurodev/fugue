package app

import (
	"context"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// SessionTools returns the service the session principal talks to.
func (rt *HostRuntime) SessionTools() (*SessionToolService, error) {
	if rt.sessionTools == nil {
		return nil, errcode.New(errcode.Unavailable, "the session tool service is unavailable")
	}
	return rt.sessionTools, nil
}

// VendorOwnsPeer reports whether pid belongs to a live vendor session. The
// transport denies such a peer the local principal (roadmap Q-41 A); an
// error means the answer is unknown and the transport fails closed.
func (rt *HostRuntime) VendorOwnsPeer(ctx context.Context, pid int) (bool, error) {
	if rt.vendors == nil {
		return false, nil
	}
	return rt.vendors.sessions.OwnsPeer(ctx, pid)
}
