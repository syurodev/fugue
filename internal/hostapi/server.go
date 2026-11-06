package hostapi

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/host/v1/hostv1connect"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// ServerConfig configures the host api server. It carries no version fields:
// host.json and HostStatus read them from the backend (the runtime), which is
// their single source. It carries no timeouts: a leased connection must
// survive arbitrary idle periods and the uid check is the access gate.
type ServerConfig struct {
	// Socket is the bound socket from ListenSocket.
	Socket *Socket
	// DataDir holds host.json.
	DataDir string
	// IDs generates the per-connection ids.
	IDs ids.Generator
	// HostUID is the uid connections must come from (host-api §7).
	HostUID int64
	// PeerCredentials reads the peer's uid; nil uses the platform
	// implementation.
	PeerCredentials func(net.Conn) (PeerCredentials, error)
}

// connState is the per-connection state attached to the connection's context:
// the connection's id and whether it has negotiated a hello.
type connState struct {
	id      string
	helloed atomic.Bool
}

type connStateKey struct{}

// Server serves the Host API over the private socket. Drain is its stop path:
// discovery files, listener, http connections — the store and the lock are
// closed after it by the host's drain.
type Server struct {
	cfg ServerConfig

	mu      sync.Mutex
	conns   map[net.Conn]*connState
	drained bool

	backend   app.Backend
	httpSrv   *http.Server
	listener  *AcceptListener
	serveTask *supervise.Task
}

// NewServer builds a server for the bound socket; Start serves it.
func NewServer(cfg ServerConfig) *Server {
	return &Server{cfg: cfg, conns: map[net.Conn]*connState{}}
}

// Start writes host.json from the backend's status and serves the Connect
// handlers on the socket under a supervised serve loop. The base context is
// captured in the closures below; it is never stored.
func (s *Server) Start(ctx context.Context, backend app.Backend) error {
	status, err := backend.Status(ctx)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "read the host status", err)
	}
	hf := &HostFile{
		PID:         os.Getpid(),
		StartedAt:   status.StartedAt,
		SocketPath:  s.cfg.Socket.Path,
		APIVersions: status.SupportedAPIVersions,
		HostVersion: status.HostVersion,
	}
	if err := WriteHostFile(s.cfg.DataDir, hf); err != nil {
		return err
	}
	s.backend = backend
	s.listener = NewAcceptListener(ListenerOptions{
		Inner:           s.cfg.Socket.Listener,
		HostUID:         s.cfg.HostUID,
		PeerCredentials: s.cfg.PeerCredentials,
		OnClosed:        s.onConnClosed(ctx),
		OnReject:        onRejectWarn,
	})
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(&serviceHandler{backend: backend}))
	// Unencrypted HTTP/2 on the private socket (Q-24 A); http/1.1 stays
	// enabled for plain clients.
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	s.httpSrv = &http.Server{
		Handler:   mux,
		Protocols: &protos,
		ConnContext: func(connCtx context.Context, c net.Conn) context.Context {
			return context.WithValue(connCtx, connStateKey{}, s.registerConn(c))
		},
	}
	s.serveTask = supervise.Go(ctx, "hostapi.serve", func(context.Context) error {
		err := s.httpSrv.Serve(s.listener)
		// ErrServerClosed is Shutdown/Close; net.ErrClosed is our own
		// listener close during the drain. Both are clean stops.
		if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return errcode.Wrap(errcode.Internal, "serve the host api", err)
	})
	return nil
}

// registerConn creates the connection's state under its context value.
func (s *Server) registerConn(c net.Conn) *connState {
	st := &connState{}
	if s.cfg.IDs != nil {
		if id, err := s.cfg.IDs.NewID(); err == nil {
			st.id = id
		}
	}
	s.mu.Lock()
	s.conns[c] = st
	s.mu.Unlock()
	return st
}

// onConnClosed returns the close hook: the first close of an accepted
// connection releases its lease on the server's base context, if it had one.
func (s *Server) onConnClosed(baseCtx context.Context) func(net.Conn) {
	return func(c net.Conn) {
		s.mu.Lock()
		st := s.conns[c]
		delete(s.conns, c)
		s.mu.Unlock()
		if st == nil || !st.helloed.Load() {
			return
		}
		s.backend.LeaseReleased(baseCtx, st.id)
	}
}

// Drain stops serving: discovery files first, then the listener, then the
// http connections. It is idempotent — later calls are the same no-op reply.
func (s *Server) Drain(ctx context.Context, force bool) error {
	s.mu.Lock()
	if s.drained {
		s.mu.Unlock()
		return nil
	}
	s.drained = true
	s.mu.Unlock()
	if err := RemoveHostFile(s.cfg.DataDir); err != nil {
		return err
	}
	if err := removeSocket(s.cfg.Socket.Path); err != nil {
		return err
	}
	if err := s.listener.Close(); err != nil {
		return errcode.Wrap(errcode.Internal, "close the host listener", err)
	}
	if force {
		if err := s.httpSrv.Close(); err != nil {
			return errcode.Wrap(errcode.Internal, "close the host http server", err)
		}
	} else if err := s.httpSrv.Shutdown(ctx); err != nil {
		return errcode.Wrap(errcode.Internal, "shut the host http server down", err)
	}
	// Wait for the serve loop so no stdlib goroutine outlives the drain.
	_ = s.serveTask.Wait()
	return nil
}

// removeSocket unlinks the socket this server bound; an already-absent file
// is not an error.
func removeSocket(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errcode.Wrap(errcode.Internal, "remove the host socket at "+path, err)
	}
	return nil
}

// onRejectWarn logs the cumulative rejection count on the first occurrence
// and then every tenth, never with per-connection detail.
func onRejectWarn(count uint64) {
	if count == 1 || count%10 == 0 {
		slog.Warn("host socket connections rejected", "count", count)
	}
}
