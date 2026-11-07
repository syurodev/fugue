package hostapi

import (
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// TestAcceptListenerCloseIsIdempotent pins the drain contract: the first
// Close closes the inner listener and returns its error unchanged, and every
// later Close is nil — Server.Drain closes the listener and the http server
// then closes it again through the same wrapper.
func TestAcceptListenerCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	inner, err := new(net.ListenConfig).Listen(t.Context(), "unix", filepath.Join(dir, SocketFileName))
	if err != nil {
		t.Fatal(err)
	}
	l := NewAcceptListener(ListenerOptions{Inner: inner, HostUID: int64(1000)})
	if err := l.Close(); err != nil {
		t.Fatalf("first Close = %v, want the inner listener's nil", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("third Close = %v, want nil", err)
	}
	if _, err := inner.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after the first Close = %v, want net.ErrClosed", err)
	}
}

// failCloseListener reports a close error once and nil afterwards, so the
// test pins that only the first Close returns the inner error.
type failCloseListener struct {
	inner net.Listener
	calls int
}

func (l *failCloseListener) Accept() (net.Conn, error) {
	conn, err := l.inner.Accept()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "accept on the scripted listener", err)
	}
	return conn, nil
}

func (l *failCloseListener) Close() error {
	l.calls++
	if l.calls == 1 {
		return errCloseFailed
	}
	if err := l.inner.Close(); err != nil {
		return errcode.Wrap(errcode.Internal, "close the scripted listener", err)
	}
	return nil
}

func (l *failCloseListener) Addr() net.Addr { return l.inner.Addr() }

// errCloseFailed is the scripted close failure of the fail-close listener.
var errCloseFailed = errors.New("close failed")

func TestAcceptListenerCloseReturnsTheFirstErrorUnwrapped(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	inner, err := new(net.ListenConfig).Listen(t.Context(), "unix", filepath.Join(dir, SocketFileName))
	if err != nil {
		t.Fatal(err)
	}
	l := NewAcceptListener(ListenerOptions{Inner: &failCloseListener{inner: inner}, HostUID: int64(1000)})
	if err := l.Close(); !errors.Is(err, errCloseFailed) {
		t.Fatalf("first Close = %v, want the inner close failure unchanged", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}
