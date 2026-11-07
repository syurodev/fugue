package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/hostapi"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// The exit codes of a serve run (cli §5.2).
const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitInterrupted = 130 // 128+SIGINT, the conventional Ctrl-C code
)

// serveOpts are the seams of a serve run the tests inject.
type serveOpts struct {
	// ready, when not nil, is closed once the signal handlers are installed
	// and the host serves the socket.
	ready chan<- struct{}
	// sigs carries the stop signals; nil installs the process signal
	// handlers. Tests inject a channel so parallel tests cannot hear each
	// other's process-wide signals.
	sigs <-chan os.Signal
	// wrapTransport, when not nil, wraps the server as the host's drain
	// transport. The tests wrap the real drain with a blocking one.
	wrapTransport func(app.HostTransport) app.HostTransport
}

// RunServe runs the foreground host until its context is canceled or a
// signal arrives, then returns the exit code (cli §5.2): SIGINT 130, SIGTERM
// and explicit cancellation 0, a failed startup 1. Startup failures are
// logged; the exit code is the caller's outcome.
func RunServe(ctx context.Context, out io.Writer, cfg config.Config, version string) int {
	code, err := serve(ctx, out, cfg, version, serveOpts{})
	if err != nil {
		slog.Error("fugue serve stopped", "err", err)
	}
	return code
}

// serve is RunServe with the test seams. The socket and the server are built
// without serving: the store lock is the liveness authority, so nothing
// answers the socket before StartHost holds the lock (plan Task 6 wiring
// order).
func serve(ctx context.Context, out io.Writer, cfg config.Config, version string, opts serveOpts) (int, error) {
	sigs := opts.sigs
	if sigs == nil {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(c)
		sigs = c
	}

	sock, err := hostapi.ListenSocket(ctx, hostapi.SocketOptions{
		DataDir:    cfg.DataDir,
		RuntimeDir: cfg.RuntimeDir,
	})
	if err != nil {
		return exitFailure, startupErr(err, "bind the host socket")
	}
	srv := hostapi.NewServer(hostapi.ServerConfig{
		Socket:  sock,
		DataDir: cfg.DataDir,
		IDs:     ids.NewUUIDv7(clock.Real{}, ids.CryptoRandom{}),
		HostUID: int64(os.Getuid()),
	})
	transport := app.HostTransport(srv)
	if opts.wrapTransport != nil {
		transport = opts.wrapTransport(srv)
	}
	hp, err := app.StartHost(ctx, &app.HostDeps{
		DataDir: cfg.DataDir,
		Config: app.HostConfig{
			HostVersion: version,
			// F5.1 serves the one published Host API version.
			APIVersions: []string{"v1"},
			// Foreground serve never self-exits (Q-25 A); the drain waits
			// unbounded until the design names a deadline.
			IdleGrace:    0,
			DrainTimeout: 0,
		},
		Clock:     clock.Real{},
		IDs:       ids.NewUUIDv7(clock.Real{}, ids.CryptoRandom{}),
		Transport: transport,
	})
	if err != nil {
		return exitFailure, errors.Join(startupErr(err, "open the host store"), teardownSocket(sock))
	}
	if err := srv.Start(ctx, hp.Runtime()); err != nil {
		return exitFailure, errors.Join(startupErr(err, "serve the host api"), teardownHost(hp, cfg.DataDir, sock))
	}
	if opts.ready != nil {
		close(opts.ready)
	}
	if werr := say(out, "fugue host serving at %s", sock.Path); werr != nil {
		slog.Warn("write the serving line", "err", werr)
	}
	return runUntilStopped(ctx, hp, sigs), nil
}

// runUntilStopped serves until the context is canceled or a signal arrives,
// then stops the host and returns the exit code. The signal path is the same
// beginDrain entry the Shutdown RPC and the idle grace use; the drain closes
// the transport, then the store and the lock last.
func runUntilStopped(ctx context.Context, hp *app.HostProcess, sigs <-chan os.Signal) int {
	var reason string
	var code int
	select {
	case sig := <-sigs:
		reason, code = exitForSignal(sig)
	case <-ctx.Done():
		reason, code = "context canceled", exitOK
	}
	hp.Stop(reason)
	select {
	case <-hp.DrainDone():
	case sig := <-sigs:
		// With DrainTimeout 0 the drain has no deadline, so a second stop
		// signal must not queue up unheard: the wait ends and the process
		// exits with the second signal's code. The operating system releases
		// the data-directory lock when the process dies and the next start
		// replaces the stale socket and host.json.
		_, sigCode := exitForSignal(sig)
		slog.Warn("second stop signal; ending the drain wait", "signal", sig.String())
		return sigCode
	}
	if err := hp.Close(); err != nil {
		slog.Error("close the fugue host after drain", "err", err)
	}
	return code
}

// exitForSignal maps a caught signal to the stop reason and the exit code
// (cli §5.2): Ctrl-C is 130, a termination request is a clean 0.
func exitForSignal(sig os.Signal) (string, int) {
	if sig == os.Interrupt {
		return "interrupt", exitInterrupted
	}
	return "terminated", exitOK
}

// startupErr re-wraps a startup failure under its own public code, so a
// second host keeps failing typed with what the first can already show.
func startupErr(err error, msg string) error {
	return errcode.Wrap(errcode.CodeOf(err), msg, err)
}

// teardownSocket closes a socket that never served: a failed startup leaves
// no stale socket for a client to dial. The next start would replace the
// leftover anyway, but the startup owns what it bound.
func teardownSocket(sock *hostapi.Socket) error {
	lerr := sock.Listener.Close()
	if lerr != nil {
		lerr = errcode.Wrap(errcode.Internal, "close the host socket "+sock.Path, lerr)
	}
	if rerr := os.Remove(sock.Path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return errors.Join(lerr, errcode.Wrap(errcode.Internal, "remove the host socket "+sock.Path, rerr))
	}
	return lerr
}

// teardownHost tears down a host whose transport failed to start: the drain
// never ran, so Close closes the store, and the discovery files this startup
// wrote are removed.
func teardownHost(hp *app.HostProcess, dataDir string, sock *hostapi.Socket) error {
	cerr := hp.Close()
	if cerr != nil {
		cerr = errcode.Wrap(errcode.Internal, "close the host after a failed start", cerr)
	}
	return errors.Join(cerr, hostapi.RemoveHostFile(dataDir), teardownSocket(sock))
}
