package hostconn

import (
	"errors"
	"fmt"

	"github.com/syurodev/fugue/pkg/client"
)

// The reasons a refused host start leaves on the Disconnected state, stable
// text the window matches to pick the failure banner (activation §3).
const (
	// ReasonDataDirInUse names a start refused because another process holds
	// the data directory.
	ReasonDataDirInUse = "data directory in use"
	// ReasonStoreUnsupported names a start refused because the data
	// directory holds a schema this Fugue cannot serve.
	ReasonStoreUnsupported = "data directory schema not supported"
)

// ErrDataDirInUse and ErrStoreUnsupported are the typed causes of a launched
// host that exited with the matching public exit code.
var (
	ErrDataDirInUse     = errors.New("the data directory is held by another process")
	ErrStoreUnsupported = errors.New("the data directory schema is not supported by this Fugue")
)

// HostExitError is a launched host child's unsuccessful exit.
type HostExitError struct{ Code int }

// Error implements error.
func (e *HostExitError) Error() string {
	return fmt.Sprintf("host child exited with code %d", e.Code)
}

// exitSettlePolls is how many failed dials a launched host's refusal waits
// out before it is believed: two apps racing to start one host both launch a
// child, the loser exits refused while the winner is still writing its
// discovery file, and only a dial that keeps failing means no host won.
const exitSettlePolls = 10

// hostExit is a launched host child's exit, readable without consuming the
// channel the lifecycle callers drain: err is set before done closes.
type hostExit struct {
	done chan struct{}
	err  error
}

// launchWatch turns a launched host child's refusal into its typed cause
// after the settle.
type launchWatch struct {
	exit  *hostExit
	polls int
}

// failure reports the refusal cause once the child exited with a public
// refusal code and the settle passed; nil means keep waiting for the host.
func (w *launchWatch) failure() error {
	if w.exit == nil {
		return nil
	}
	select {
	case <-w.exit.done:
	default:
		return nil
	}
	var exit *HostExitError
	if !errors.As(w.exit.err, &exit) {
		return nil
	}
	w.polls++
	if w.polls < exitSettlePolls {
		return nil
	}
	switch exit.Code {
	case client.ExitDataDirInUse:
		return ErrDataDirInUse
	case client.ExitStoreUnsupported:
		return ErrStoreUnsupported
	}
	return nil
}

// AppParams returns the identity the app says Hello with, for the failure UX
// that names the app's own version next to the host's (activation §3).
func (m *Manager) AppParams() client.Params { return m.cfg.Params }

// refusalReason maps a launched host's typed refusal onto its stable reason,
// empty for every other error.
func refusalReason(err error) string {
	switch {
	case errors.Is(err, ErrDataDirInUse):
		return ReasonDataDirInUse
	case errors.Is(err, ErrStoreUnsupported):
		return ReasonStoreUnsupported
	}
	return ""
}
