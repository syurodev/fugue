package hostconn

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

// State is the connection state the status bar shows (desktop-app §4).
type State string

// The connection states. Incompatible means the host answered but speaks no
// Host API version in common with the app: retrying cannot fix it, and the
// version handling of cli §2.3 (F10.2b) decides what happens next.
const (
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateDisconnected State = "disconnected"
	StateIncompatible State = "incompatible"
)

// Update is one message for the window goroutine: either a connection-state
// change (Snapshot and Event nil) or one topic delivery.
type Update struct {
	State    State
	Reason   string
	Topic    string
	Snapshot *client.Snapshot
	Event    *client.Event
}

// Config configures the Manager.
type Config struct {
	// DataDir locates the host's discovery file.
	DataDir string
	// Params are the Hello identity; the app dials with KindApp (DA-01).
	Params client.Params
	// AutoStart launches a local host when discovery is absent.
	AutoStart bool
	// ExecutablePath is a test seam; empty resolves the current app executable.
	ExecutablePath string
	// Topics are the event topics to stream, one stream goroutine each.
	Topics []string
	// Updates receives state changes and topic deliveries. It is the
	// caller's channel and is never closed by the Manager.
	Updates chan<- Update
	// Clock drives the reconnect backoff; nil uses RealClock.
	Clock Clock
}

// streamFailure is a topic stream's end reason, reported to the run loop.
type streamFailure struct {
	topic string
	err   error
}

// topicReq asks the stream loop to start or stop one topic's pump.
type topicReq struct {
	topic string
	add   bool
}

// topicReqBuffer bounds the request queue. Requests only pile up while no
// connection is running the stream loop; a dropped request is harmless
// because every topic added to the dynamic set starts on the next connect.
const topicReqBuffer = 64

// Manager owns the app's connection to the host: one leased client
// connection, one event stream per topic, reconnect with bounded backoff.
// Start runs it; Stop ends it and waits for its goroutines.
type Manager struct {
	cfg Config
	// cancel cancels the connection run loop started in Start.
	cancel context.CancelFunc
	wg     sync.WaitGroup

	failures  chan streamFailure
	topicReqs chan topicReq
	children  launchedChildren

	mu      sync.Mutex
	state   State
	reason  string
	conn    *client.Client
	cursors map[string]client.Cursor
	// streamsCancel cancels the current connection's stream pumps; a network
	// drop and an explicit disconnect end through the same cancel.
	streamsCancel context.CancelFunc
	// dynamic holds the topics added and removed after Start (the per-session
	// transcript topics of F10.4a), guarded by mu.
	dynamic map[string]bool
}

// NewManager builds a Manager; Start runs it.
func NewManager(cfg *Config) *Manager {
	c := Config{}
	if cfg != nil {
		c = *cfg
	}
	return &Manager{
		cfg:           c,
		cancel:        nil,
		wg:            sync.WaitGroup{},
		failures:      make(chan streamFailure),
		topicReqs:     make(chan topicReq, topicReqBuffer),
		mu:            sync.Mutex{},
		state:         "",
		reason:        "",
		conn:          nil,
		cursors:       map[string]client.Cursor{},
		dynamic:       map[string]bool{},
		streamsCancel: nil,
	}
}

// Start runs the connection loop. It returns at once; the loop ends with
// Stop or when the passed context ends.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go m.run(runCtx)
}

// Stop ends the Manager's goroutines and waits for them. It is idempotent.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	streamsCancel := m.streamsCancel
	m.streamsCancel = nil
	m.mu.Unlock()
	if streamsCancel != nil {
		streamsCancel()
	}
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

// State returns the current connection state and its reason, for callers
// that poll; the pushed Updates carry the same information.
func (m *Manager) State() (State, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.reason
}

// AddTopic streams one more topic from the current connection, subscribing
// from its stored cursor when it has one and from a snapshot otherwise. The
// topic survives reconnects until RemoveTopic. Safe from the window
// goroutine: while the stream loop is not running the request buffers, and a
// buffered request the loop never sees is harmless because the dynamic set,
// not the request, is what a connect starts from.
func (m *Manager) AddTopic(topic string) {
	m.mu.Lock()
	first := !m.dynamic[topic] && !slices.Contains(m.cfg.Topics, topic)
	m.dynamic[topic] = true
	m.mu.Unlock()
	if first {
		select {
		case m.topicReqs <- topicReq{topic: topic, add: true}:
		default:
		}
	}
}

// RemoveTopic stops one dynamic topic's stream. Its cursor is kept, so a
// later AddTopic catches up instead of reloading. Safe from the window
// goroutine.
func (m *Manager) RemoveTopic(topic string) {
	m.mu.Lock()
	delete(m.dynamic, topic)
	m.mu.Unlock()
	select {
	case m.topicReqs <- topicReq{topic: topic, add: false}:
	default:
	}
}

// Client returns the leased connection the streams run on, or nil while
// disconnected. The caller may use it for unary reads; a connection being
// torn down fails the call, which the caller retries.
func (m *Manager) Client() *client.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conn
}

// dynamicTopics snapshots the dynamic set.
func (m *Manager) dynamicTopics() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Sorted(maps.Keys(m.dynamic))
}

// cursorFor returns the stored cursor for a topic, or nil on first subscribe.
func (m *Manager) cursorFor(topic string) *client.Cursor {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.cursors[topic]; ok {
		return &c
	}
	return nil
}

// setCursor records where the topic's stream has read to. A snapshot
// replaces the whole cursor: its incarnation is the one the stream now
// belongs to (a new host incarnation resets the topic's state).
func (m *Manager) setCursor(topic string, c client.Cursor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursors[topic] = c
}

// clearCursor drops the stored cursor so the next subscribe starts from a
// fresh snapshot (a resync requirement, host-api §6).
func (m *Manager) clearCursor(topic string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cursors, topic)
}

// setState stores the state and, when it changed, pushes it to the window.
func (m *Manager) setState(ctx context.Context, s State, reason string) {
	m.mu.Lock()
	unchanged := m.state == s && m.reason == reason
	if !unchanged {
		m.state, m.reason = s, reason
	}
	m.mu.Unlock()
	if unchanged {
		return
	}
	m.deliver(ctx, Update{State: s, Reason: reason})
}

// deliver pushes one update to the window goroutine, giving up when the
// window context ends. A stalled window backs the stream pumps up, which is
// the designed backpressure: the host's hub resynchronizes a subscriber that
// cannot keep up (host-api §6).
func (m *Manager) deliver(ctx context.Context, u Update) {
	select {
	case m.cfg.Updates <- u:
	case <-ctx.Done():
	}
}

// run is the connection loop: dial, run the topic streams, and back off
// before the next attempt on any failure. It ends when Stop or the window
// context ends, and freezes on an incompatible host, which retrying cannot
// fix (F10.2b owns the version handling of cli §2.3).
func (m *Manager) run(ctx context.Context) {
	defer m.wg.Done()
	m.setState(ctx, StateConnecting, "")
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := m.dial(ctx)
		switch {
		case err == nil:
			if !m.connected(ctx, conn) {
				return
			}
			attempt = 0
		case isVersionMismatch(err):
			m.setState(ctx, StateIncompatible, err.Error())
			return
		case errors.Is(err, ErrStartupTimeout):
			m.setState(ctx, StateDisconnected, err.Error())
			return
		case errors.Is(err, client.ErrHostNotRunning):
			m.setState(ctx, StateDisconnected, "host not running")
		case isUntrusted(err):
			// An unsafe executable or an untrusted discovery file or peer
			// does not heal by retrying; the status bar keeps the reason.
			m.setState(ctx, StateDisconnected, err.Error())
			return
		default:
			// A failed Hello or a refused dial can be transient (a host
			// draining or restarting): back off and try again.
			m.setState(ctx, StateDisconnected, "connection failed")
		}
		attempt++
		if !m.backoff(ctx, attempt) {
			return
		}
	}
}

func (m *Manager) connected(ctx context.Context, conn *client.Client) bool {
	defer conn.Close()
	if m.cfg.AutoStart {
		status, err := m.hostStatus(ctx, conn)
		if err != nil {
			// A stalled or failed status read is retried like a failed dial;
			// only an incompatible version below freezes the manager.
			m.setState(ctx, StateDisconnected, "host status unavailable")
			return ctx.Err() == nil
		}
		if reason := versionAdvice(m.cfg.Params.Version, status.HostVersion, status.Clients); reason != "" {
			m.setState(ctx, StateIncompatible, reason)
			return false
		}
	}
	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()
	m.setState(ctx, StateConnected, "")
	m.runStreams(ctx, conn)
	if ctx.Err() != nil {
		return false
	}
	m.setState(ctx, StateDisconnected, "connection lost")
	return true
}

// dial reads the host's discovery file and says Hello on its socket.
func (m *Manager) dial(ctx context.Context) (*client.Client, error) {
	if !m.cfg.AutoStart {
		return m.dialOnce(ctx, false)
	}
	launched := false
	return waitForHost(ctx, m.clock(), func(callCtx context.Context) (*client.Client, error) {
		conn, err := m.dialOnce(callCtx, true)
		if !errors.Is(err, client.ErrHostNotRunning) || launched {
			return conn, err
		}
		path := m.cfg.ExecutablePath
		if path == "" {
			path, err = os.Executable()
			if err != nil {
				return nil, fmt.Errorf("resolve app executable: %w", err)
			}
		}
		children, launchErr := launchDetached(callCtx, path, m.cfg.DataDir, os.Environ())
		if launchErr != nil {
			return nil, launchErr
		}
		m.mu.Lock()
		m.children = children
		m.mu.Unlock()
		launched = true
		return nil, client.ErrHostNotRunning
	})
}

func (m *Manager) dialOnce(ctx context.Context, trusted bool) (*client.Client, error) {
	var hf client.HostFile
	var err error
	if trusted {
		hf, err = client.ReadTrustedHostFile(m.cfg.DataDir)
	} else {
		hf, err = client.ReadHostFile(m.cfg.DataDir)
	}
	if err != nil {
		return nil, fmt.Errorf("find the host: %w", err)
	}
	var conn *client.Client
	if trusted {
		conn, err = client.DialTrusted(ctx, hf.SocketPath, m.cfg.Params)
	} else {
		conn, err = client.Dial(ctx, hf.SocketPath, m.cfg.Params)
	}
	if err != nil {
		return nil, fmt.Errorf("dial the host: %w", err)
	}
	return conn, nil
}

// isVersionMismatch reports whether err is the host's unsupported_version
// rejection (Q-26 B).
func isVersionMismatch(err error) bool {
	var mismatch *client.VersionMismatchError
	return errors.As(err, &mismatch)
}

// backoff waits before the nth retry on the injected clock. It reports
// whether the loop should continue: it returns false when the context ended
// while waiting.
func (m *Manager) backoff(ctx context.Context, attempt int) bool {
	timer := m.clock().NewTimer(backoffFor(attempt))
	defer timer.Stop()
	select {
	case <-timer.C():
		return true
	case <-ctx.Done():
		return false
	}
}

// clock returns the configured clock; nil uses RealClock.
func (m *Manager) clock() Clock {
	if m.cfg.Clock != nil {
		return m.cfg.Clock
	}
	return RealClock{}
}

// runStreams runs one stream goroutine per topic on the leased connection
// and returns when a stream fails at the transport level, the window context
// ends, or a topic needs a full reconnect. A stream that ends because the
// client fell behind (resync) or the host ended it cleanly is restarted in
// place: the connection, and every other topic's stream, stay up.
func (m *Manager) runStreams(ctx context.Context, conn *client.Client) {
	// The pumps live on their own context so the whole stream set can be
	// torn down — by a transport failure or a dropped connection — without
	// ending the run loop.
	streamsCtx, streamsCancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.streamsCancel = streamsCancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.streamsCancel = nil
		m.mu.Unlock()
	}()
	pumps := &pumpSet{mgr: m, conn: conn, pumps: map[string]*pump{}}
	defer pumps.stopAll()
	for _, topic := range m.cfg.Topics {
		pumps.start(streamsCtx, topic, false)
	}
	for _, topic := range m.dynamicTopics() {
		if !slices.Contains(m.cfg.Topics, topic) {
			pumps.start(streamsCtx, topic, false)
		}
	}
	for {
		f, ok := pumps.await(streamsCtx)
		if !ok {
			return
		}
		if m.restartInPlace(streamsCtx, f) {
			pumps.start(streamsCtx, f.topic, errors.Is(f.err, client.ErrResyncRequired))
			continue
		}
		// A transport failure means the connection is gone: every stream
		// ends and the run loop reconnects with a fresh Hello.
		return
	}
}

// restartInPlace reports whether a failed stream should restart on the same
// connection: a resync or a clean end is stream-level, and the backoff
// guards the restart so a host that ends streams as fast as they are opened
// cannot spin the manager hot. It reports false when the context ended while
// backing off.
func (m *Manager) restartInPlace(ctx context.Context, f streamFailure) bool {
	if !errors.Is(f.err, client.ErrResyncRequired) && !errors.Is(f.err, client.ErrStreamEnded) {
		return false
	}
	return m.backoff(ctx, 1)
}
