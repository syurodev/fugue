package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"

	"connectrpc.com/connect"

	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
)

// errUnknownKind is the static cause behind a Hello with a kind the Host API
// does not define; it is wrapped, never constructed, at the call site.
var errUnknownKind = errors.New("client kind is not one the Host API defines")

// Kind is the client kind a Dial claims in its Hello (host-api §8). It is
// informational: the host authorizes by the connection's uid, never by the
// claim.
type Kind string

// The client kinds, mirroring hostv1.ClientKind.
const (
	KindTUI           Kind = "tui"
	KindCLI           Kind = "cli"
	KindApp           Kind = "app"
	KindAppBackground Kind = "app-background"
)

// proto maps the kind to the wire enum; an unknown kind is invalid rather
// than a guess, and the host rejects an unspecified kind.
func (k Kind) proto() (hostv1.ClientKind, error) {
	switch k {
	case KindTUI:
		return hostv1.ClientKind_CLIENT_KIND_TUI, nil
	case KindCLI:
		return hostv1.ClientKind_CLIENT_KIND_CLI, nil
	case KindApp:
		return hostv1.ClientKind_CLIENT_KIND_APP, nil
	case KindAppBackground:
		return hostv1.ClientKind_CLIENT_KIND_APP_BACKGROUND, nil
	default:
		return hostv1.ClientKind_CLIENT_KIND_UNSPECIFIED, fmt.Errorf("%w: %q", errUnknownKind, string(k))
	}
}

// Params are the Hello fields a client identifies itself with (host-api §8).
type Params struct {
	// Kind is the client kind; it must be one of the Kind constants.
	Kind Kind
	// Name is the client's product name, informational.
	Name string
	// Version is the client's own version, informational.
	Version string
	// APIVersions are the Host API versions the client supports, ascending,
	// most preferred last; the host picks the highest common one or rejects
	// the Hello with unsupported_version (Q-26 B).
	APIVersions []string
}

// Client is one leased connection to the host: Dial opened the dedicated
// HTTP/2 connection and said Hello on it. Its methods block; streams are
// driven by the caller. Close releases the transport once the caller has
// closed every stream it holds.
type Client struct {
	hc   *http.Client
	base string
}

// Dial connects to the host socket and says Hello, acquiring the lease.
// Everything a caller needs before this — finding the socket — comes from
// ReadHostFile. A socket nobody listens on, and one whose file is gone, fail
// with ErrHostNotRunning so a caller can retry; a Hello without a common API
// version fails with VersionMismatchError.
func Dial(ctx context.Context, socket string, p Params) (*Client, error) {
	kind, err := p.Kind.proto()
	if err != nil {
		return nil, err
	}
	c := &Client{
		hc:   &http.Client{Transport: newTransport(socket)},
		base: "http://fugue",
	}
	svc := hostv1connect.NewHostServiceClient(c.hc, c.base)
	_, err = svc.Hello(ctx, connect.NewRequest(&hostv1.HelloRequest{
		ClientName:           p.Name,
		ClientVersion:        p.Version,
		SupportedApiVersions: p.APIVersions,
		ClientKind:           kind,
	}))
	if err != nil {
		c.Close()
		return nil, dialError(err)
	}
	return c, nil
}

// Subscribe opens a server-streaming subscription to one topic (host-api §6).
// A nil cursor starts from the topic's snapshot; a cursor the host can still
// catch up from skips the snapshot, and one it cannot is answered with a fresh
// snapshot. The stream lives while ctx does; Recv turns the wire messages into
// typed values and the stream's endings into typed errors. A stream abandoned
// before its end must be closed with Close, or its HTTP/2 stream holds the
// connection open (F5.2 review).
func (c *Client) Subscribe(ctx context.Context, topic string, cursor *Cursor) (*Stream, error) {
	req := connect.NewRequest(&hostv1.SubscribeRequest{Topic: topic})
	if cursor != nil {
		req.Msg.Cursor = &hostv1.EventCursor{
			Topic:         cursor.Topic,
			IncarnationId: cursor.Incarnation,
			Sequence:      cursor.Sequence,
		}
	}
	inner, err := hostv1connect.NewHostServiceClient(c.hc, c.base).Subscribe(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("subscribe to %s: %w", topic, err)
	}
	return &Stream{inner: inner}, nil
}

// HostStatus describes the running host the client is connected to (cli §7):
// its versions, so the version handling of cli §2.3 can compare them with the
// client's own, and the incarnation of this host process.
type HostStatus struct {
	HostVersion          string
	SupportedAPIVersions []string
	Incarnation          string
}

// HostStatus asks the host for its status. It fails on a connection the host
// no longer considers helloed — one sign that the leased connection is gone.
func (c *Client) HostStatus(ctx context.Context) (HostStatus, error) {
	res, err := hostv1connect.NewHostServiceClient(c.hc, c.base).HostStatus(ctx, connect.NewRequest(&hostv1.HostStatusRequest{}))
	if err != nil {
		return HostStatus{}, fmt.Errorf("read the host status: %w", err)
	}
	return HostStatus{
		HostVersion:          res.Msg.GetHostVersion(),
		SupportedAPIVersions: res.Msg.GetSupportedApiVersions(),
		Incarnation:          res.Msg.GetIncarnationId(),
	}, nil
}

// Close releases the connection's idle transport state. It does not end open
// streams: close them first, or the lease outlives the Client.
func (c *Client) Close() {
	c.hc.CloseIdleConnections()
}

// newTransport builds the dedicated connection's transport: unencrypted
// HTTP/2 over the unix socket, one connection at most, and no idle timeout —
// the lease rides this connection and must survive arbitrary idle periods
// (host-api §3, F5.1). The dial succeeds at most once: after the Client's
// connection is established, any further dial attempt returns
// ErrDisconnected without touching the socket, so a dropped lease surfaces as
// "disconnected" and never as a silent new connection the host has not
// helloed.
func newTransport(socket string) *http.Transport {
	var protos http.Protocols
	protos.SetUnencryptedHTTP2(true)
	var dialMu sync.Mutex
	dialed := false
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialMu.Lock()
			defer dialMu.Unlock()
			if dialed {
				return nil, fmt.Errorf("the Client owns one connection: %w", ErrDisconnected)
			}
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
			if err != nil {
				return nil, fmt.Errorf("dial the host socket: %w", err)
			}
			dialed = true
			return conn, nil
		},
		Protocols:       &protos,
		MaxConnsPerHost: 1,
		IdleConnTimeout: 0,
	}
}

// dialError classifies a failed Hello. A transport failure whose cause is a
// missing socket file or a socket nobody accepts on is ErrHostNotRunning; a
// host rejection of the version negotiation is VersionMismatchError; anything
// else passes through as the connect error it already is.
func dialError(err error) error {
	if connect.CodeOf(err) == connect.CodeFailedPrecondition {
		if mismatch, ok := versionMismatch(err); ok {
			return mismatch
		}
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && (errno == syscall.ENOENT || errno == syscall.ECONNREFUSED) {
		return fmt.Errorf("dial the host socket: %w: %w", ErrHostNotRunning, err)
	}
	return err
}

// versionMismatch extracts the host's unsupported_version detail (Q-26 B):
// the detail is the only place the host names its versions on a rejection.
func versionMismatch(err error) (*VersionMismatchError, bool) {
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		return nil, false
	}
	for _, d := range cerr.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		detail, ok := msg.(*hostv1.ErrorDetail)
		if !ok || !strings.EqualFold(detail.GetCode(), "unsupported_version") {
			continue
		}
		return &VersionMismatchError{HostAPIVersions: detail.GetSupportedApiVersions()}, true
	}
	return nil, false
}
