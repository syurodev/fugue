package hostapi

import (
	"log/slog"
	"net"
	"sync"
)

// ListenerOptions configures NewAcceptListener.
type ListenerOptions struct {
	// Inner is the listener to wrap.
	Inner net.Listener
	// HostUID is the uid the host process runs as; only connections from it
	// are served (host-api §7).
	HostUID int64
	// PeerCredentials reads the peer's uid; nil uses the platform
	// implementation. Tests inject it so the reject path needs no second user.
	PeerCredentials func(net.Conn) (PeerCredentials, error)
	// OnClosed runs once, with the wrapped connection, on the first Close of
	// each accepted connection; the server releases the lease there.
	OnClosed func(net.Conn)
	// OnReject receives the cumulative rejection count; the caller logs it
	// rate-limited and without per-connection detail.
	OnReject func(count uint64)
}

// AcceptListener wraps a listener and rejects every connection whose peer uid
// differs from the host user or whose credentials cannot be read, before any
// HTTP byte is parsed. Accept is called serially by the HTTP server, so the
// counters need no mutex.
type AcceptListener struct {
	inner    net.Listener
	hostUID  int64
	creds    func(net.Conn) (PeerCredentials, error)
	onClosed func(net.Conn)
	onReject func(uint64)
	rejects  uint64

	closeMu sync.Mutex
	closed  bool
}

// NewAcceptListener wraps inner with the peer-credential check.
func NewAcceptListener(opts ListenerOptions) *AcceptListener {
	creds := opts.PeerCredentials
	if creds == nil {
		creds = peerCredentials
	}
	return &AcceptListener{
		inner:    opts.Inner,
		hostUID:  opts.HostUID,
		creds:    creds,
		onClosed: opts.OnClosed,
		onReject: opts.OnReject,
	}
}

// Accept returns the next connection of the inner listener whose peer uid
// matches the host user. Rejected connections are closed and skipped; the
// loop keeps accepting so one bad connector cannot stall the host.
func (l *AcceptListener) Accept() (net.Conn, error) {
	for {
		// The inner error must keep its concrete type: http.Server.Serve
		// backs off on a temporary net.Error and detects net.ErrClosed, and
		// wrapping would turn a recoverable accept failure into a fatal exit.
		c, err := l.inner.Accept()
		if err != nil {
			return nil, err
		}
		creds, cerr := l.creds(c)
		if cerr != nil {
			l.reject(c)
			continue
		}
		if uerr := uidAllowed(creds.UID, l.hostUID); uerr != nil {
			l.reject(c)
			continue
		}
		return &trackedConn{Conn: c, onClosed: l.onClosed, peerPID: creds.PID}, nil
	}
}

func (l *AcceptListener) reject(c net.Conn) {
	// The connection is rejected either way; a close failure is logged once
	// with the running count, never with per-connection detail.
	if err := c.Close(); err != nil {
		slog.Warn("close rejected connection failed", "count", l.rejects+1)
	}
	l.rejects++
	if l.onReject != nil {
		l.onReject(l.rejects)
	}
}

// Close closes the inner listener: the first call closes it and returns its
// error unchanged, for the same reason Accept does, and every later call
// returns nil. Server.Drain closes the listener before the http server shuts
// down, and the shutdown closes the wrapper again, so Close must be safe to
// repeat.
func (l *AcceptListener) Close() error {
	l.closeMu.Lock()
	defer l.closeMu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.inner.Close()
}

// Addr returns the inner listener's address.
func (l *AcceptListener) Addr() net.Addr { return l.inner.Addr() }

// trackedConn closes over the server's close notification: the first Close
// runs it exactly once, later closes only pass through.
type trackedConn struct {
	net.Conn
	onClosed func(net.Conn)
	once     sync.Once
	// peerPID is the peer's process ID read at accept time; zero if unknown.
	peerPID int
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		if c.onClosed != nil {
			c.onClosed(c)
		}
	})
	return err
}
