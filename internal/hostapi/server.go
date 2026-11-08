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

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
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

// connPhase is the hello lifecycle of one connection. HTTP/2 multiplexes
// concurrent requests on one connection, so the phases move under the
// per-connection mutex: none, acquiring, leased, closed.
type connPhase int

const (
	phaseNone connPhase = iota
	phaseAcquiring
	phaseLeased
	phaseClosed
)

// connState is the per-connection state attached to the connection's context:
// the connection's id, its hello phase and the negotiated client kind, which
// is the connection's principal for topic authorization (host-api §7).
type connState struct {
	id    string
	kind  app.ClientKind
	mu    sync.Mutex
	phase connPhase
}

// beginHello moves none to acquiring; any other phase is a repeated hello.
func (st *connState) beginHello() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.phase != phaseNone {
		return ErrAlreadyHelloed
	}
	st.phase = phaseAcquiring
	return nil
}

// leaseHello records a successful hello — the negotiated kind becomes the
// connection's principal — and reports whether the lease was kept. When the
// connection closed while the backend calls ran, the close hook has already
// marked it closed and released nothing — the hello releases the lease itself
// and reports that it was not kept.
func (st *connState) leaseHello(kind app.ClientKind, release func()) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.phase == phaseClosed {
		release()
		return false
	}
	st.kind = kind
	st.phase = phaseLeased
	return true
}

// principal returns the connection's authenticated client kind, set only
// after a successful hello.
func (st *connState) principal() app.ClientKind {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.kind
}

// failHello returns a failed hello to none so the client may retry.
func (st *connState) failHello() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.phase == phaseAcquiring {
		st.phase = phaseNone
	}
}

// markClosed marks the connection closed and reports whether it held a lease
// that the close hook must release.
func (st *connState) markClosed() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	leased := st.phase == phaseLeased
	st.phase = phaseClosed
	return leased
}

func (st *connState) isLeased() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.phase == phaseLeased
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
	inflight  sync.WaitGroup
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
	handler := http.NewServeMux()
	handler.Handle(hostv1connect.NewHostServiceHandler(&serviceHandler{backend: backend}))
	// The admission gate and the in-flight wrapper let the drain wait for
	// handlers that are still running when the deadline force-closes their
	// connections, so none of them can outlive the store the host closes
	// after this drain.
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.admit(w) {
			return
		}
		defer s.inflight.Done()
		handler.ServeHTTP(w, r)
	})
	// Unencrypted HTTP/2 on the private socket (Q-24 A); http/1.1 stays
	// enabled for plain clients.
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	// No connection timeouts by decision (host-api §3): a leased connection
	// survives arbitrary idle periods and the uid check is the gate; F5.2
	// revisits keep-alive. The explicit zero makes the decision visible.
	s.httpSrv = &http.Server{
		ReadHeaderTimeout: 0,
		Handler:           mux,
		Protocols:         &protos,
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

// admit gates a request into the handler: after the drain started it is
// rejected with 503 and not counted, otherwise it is registered in-flight
// under the same lock. Drain sets drained under that lock before it shuts
// the http server down, so no request can be added after the drain waits for
// the in-flight ones.
func (s *Server) admit(w http.ResponseWriter) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drained {
		w.WriteHeader(http.StatusServiceUnavailable)
		return false
	}
	s.inflight.Add(1)
	return true
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
		if st == nil {
			return
		}
		if st.markClosed() {
			s.backend.LeaseReleased(baseCtx, st.id)
		}
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
	// End every event stream before the connections close: each handler
	// delivers its end message and returns, so the in-flight wait below sees
	// them finish (a host drain ends every stream cleanly, host-api §6).
	if ender, ok := s.backend.(app.StreamEnder); ok {
		ender.EndStreams()
	}
	var drainErr error
	if force {
		if err := s.httpSrv.Close(); err != nil {
			drainErr = errcode.Wrap(errcode.Internal, "close the host http server", err)
		}
	} else if err := s.httpSrv.Shutdown(ctx); err != nil {
		// Past the deadline Shutdown leaves active handlers running; Close
		// ends their connections and cancels the serve context, which ends
		// the handlers that honor their context.
		closeErr := s.httpSrv.Close()
		drainErr = errcode.Wrap(errcode.Internal,
			"shut the host http server down", errors.Join(err, closeErr))
	}
	// Wait for the handlers that were still running, so none of them runs
	// against the store the host closes after this drain returns.
	s.inflight.Wait()
	// Wait for the serve loop so no stdlib goroutine outlives the drain; a
	// serve failure is part of the drain's outcome, joined with any shutdown
	// failure above.
	if err := s.serveTask.Wait(); err != nil {
		drainErr = errors.Join(drainErr,
			errcode.Wrap(errcode.Internal, "the host api serve loop failed", err))
	}
	return drainErr
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
