package hostapi

import (
	"errors"
	"net"
	"testing"
)

// tempNetError is a temporary net.Error, the kind http.Server.Serve recovers
// from with an accept backoff; a wrapped copy would end Serve instead.
type tempNetError struct{}

func (tempNetError) Error() string   { return "temporary accept failure" }
func (tempNetError) Timeout() bool   { return false }
func (tempNetError) Temporary() bool { return true }

// failListener always fails Accept with its error.
type failListener struct{ err error }

func (l *failListener) Accept() (net.Conn, error) { return nil, l.err }
func (l *failListener) Close() error              { return nil }
func (l *failListener) Addr() net.Addr            { return unixAddr{} }

func TestAcceptReturnsTheInnerErrorUnchanged(t *testing.T) {
	t.Parallel()
	tempErr := tempNetError{}
	l := NewAcceptListener(ListenerOptions{Inner: &failListener{err: tempErr}, HostUID: 1000})
	_, err := l.Accept()
	if !errors.Is(err, tempErr) {
		t.Fatalf("Accept error = %v, want the inner failure", err)
	}
	// The concrete type must survive unchanged: errors.As against the very
	// dynamic type fails on any wrapping, and http.Server.Serve would exit
	// instead of backing off.
	var asTemp tempNetError
	if !errors.As(err, &asTemp) {
		t.Fatalf("Accept error = %v (%T), want the temporary net.Error unchanged", err, err)
	}
	l2 := NewAcceptListener(ListenerOptions{Inner: &failListener{err: net.ErrClosed}, HostUID: 1000})
	_, err2 := l2.Accept()
	if !errors.Is(err2, net.ErrClosed) {
		t.Fatalf("Accept error = %v, want net.ErrClosed detectable", err2)
	}
}
