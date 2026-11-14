package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	sessiontoolsv1 "github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1/sessiontoolsv1connect"
)

// ErrSessionTokenMissing means DialSession got no usable token. The client
// never dials without one, so a session connection is never unauthenticated.
var ErrSessionTokenMissing = errors.New("the session token is missing")

// redactedText is what every formatting of a SessionToken shows.
const redactedText = "[REDACTED]"

// SessionToken is the per-session secret the host minted when it spawned a
// vendor session (host-api §7). Every way of printing it — fmt verbs, slog,
// text encoding — shows a mask; DialSession is the only code that reads the
// value.
type SessionToken struct{ value string }

// NewSessionToken wraps the token read from the process environment.
func NewSessionToken(value string) SessionToken { return SessionToken{value: value} }

// IsZero reports whether no token was supplied.
func (t SessionToken) IsZero() bool { return t.value == "" }

// String implements fmt.Stringer.
func (SessionToken) String() string { return redactedText }

// GoString implements fmt.GoStringer.
func (SessionToken) GoString() string { return "client.SessionToken(" + redactedText + ")" }

// Format implements fmt.Formatter for every verb.
func (SessionToken) Format(f fmt.State, _ rune) {
	if _, err := f.Write([]byte(redactedText)); err != nil {
		return
	}
}

// LogValue implements slog.LogValuer.
func (SessionToken) LogValue() slog.Value { return slog.StringValue(redactedText) }

// MarshalText implements encoding.TextMarshaler.
func (SessionToken) MarshalText() ([]byte, error) { return []byte(redactedText), nil }

// Tool is one Fugue MCP tool of the attached session.
type Tool struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema of the arguments; it may be empty.
	InputSchema []byte
}

// ToolResult is the answer to one tool call. A refused or denied call is a
// result with IsError set, not an error: it is text for the vendor's model.
type ToolResult struct {
	Text    string
	IsError bool
}

// SessionClient is the session principal's connection: DialSession attached
// the token to it, and it can call the Fugue tools of that session and
// nothing else (HA-06). Like Client it owns one connection.
type SessionClient struct {
	hc        *http.Client
	tools     sessiontoolsv1connect.SessionToolServiceClient
	sessionID string
	role      string
}

// DialSession connects to the host socket and attaches the session token. A
// missing token fails before the socket is touched with ErrSessionTokenMissing;
// a socket nobody listens on fails with ErrHostNotRunning; a token the host
// does not accept fails with the host's permission_denied. No error contains
// the token.
func DialSession(ctx context.Context, socket string, token SessionToken) (*SessionClient, error) {
	if token.IsZero() {
		return nil, ErrSessionTokenMissing
	}
	hc := &http.Client{Transport: newTransport(socket, false)}
	tools := sessiontoolsv1connect.NewSessionToolServiceClient(hc, "http://fugue")
	res, err := tools.Attach(ctx, connect.NewRequest(&sessiontoolsv1.AttachRequest{SessionToken: token.value}))
	if err != nil {
		hc.CloseIdleConnections()
		return nil, dialError(err)
	}
	return &SessionClient{hc: hc, tools: tools, sessionID: res.Msg.SessionId, role: res.Msg.Role}, nil
}

// SessionID is the session the token is bound to.
func (s *SessionClient) SessionID() string { return s.sessionID }

// Role is the session's role.
func (s *SessionClient) Role() string { return s.role }

// ListTools returns the Fugue tools of the session's role.
func (s *SessionClient) ListTools(ctx context.Context) ([]Tool, error) {
	res, err := s.tools.ListTools(ctx, connect.NewRequest(&sessiontoolsv1.ListToolsRequest{}))
	if err != nil {
		return nil, fmt.Errorf("list the session tools: %w", err)
	}
	out := make([]Tool, 0, len(res.Msg.Tools))
	for _, t := range res.Msg.Tools {
		out = append(out, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out, nil
}

// CallTool runs one tool call through the host's interaction policy. It
// blocks while the user is asked.
func (s *SessionClient) CallTool(ctx context.Context, name string, arguments []byte) (ToolResult, error) {
	res, err := s.tools.CallTool(ctx, connect.NewRequest(&sessiontoolsv1.CallToolRequest{Name: name, Arguments: arguments}))
	if err != nil {
		return ToolResult{}, fmt.Errorf("call the session tool: %w", err)
	}
	return ToolResult{Text: res.Msg.Text, IsError: res.Msg.IsError}, nil
}

// Close releases the connection.
func (s *SessionClient) Close() { s.hc.CloseIdleConnections() }

// HostMessage returns the public text of a host refusal, which the host has
// already reduced to a vetted message; an error that is not the host's yields
// a fixed text so no local detail reaches a vendor.
func HostMessage(err error) string {
	var cerr *connect.Error
	if errors.As(err, &cerr) && cerr.Message() != "" {
		return cerr.Message()
	}
	return "the fugue host could not be reached"
}
