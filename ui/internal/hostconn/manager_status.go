package hostconn

import (
	"context"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/pkg/client"
)

// hostStatus reads HostStatus under the same ten-second startup deadline as
// the dial, on the injected clock, so a host that answers Hello but stalls on
// HostStatus cannot keep the window connecting forever.
func (m *Manager) hostStatus(ctx context.Context, conn *client.Client) (client.HostStatus, error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	deadline := m.clock().NewTimer(startupDeadline)
	defer deadline.Stop()
	expired := make(chan struct{})
	go func() {
		select {
		case <-deadline.C():
			close(expired)
			cancel()
		case <-callCtx.Done():
		}
	}()
	status, err := conn.HostStatus(callCtx)
	if err != nil {
		select {
		case <-expired:
			return client.HostStatus{}, ErrStartupTimeout
		default:
		}
		return client.HostStatus{}, fmt.Errorf("host status: %w", err)
	}
	return status, nil
}

// isUntrusted reports the failures that retrying cannot fix: the host
// executable or the discovered host failed authentication.
func isUntrusted(err error) bool {
	return errors.Is(err, ErrUnsafeExecutable) || errors.Is(err, client.ErrUntrustedHost)
}
