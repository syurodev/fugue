package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	sessiontoolsv1 "github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1/sessiontoolsv1connect"
)

const bridgeTestToken = "bridge-test-token"

// hostText is an error whose text is all a client sees, as the host's
// transport makes them.
type hostText string

func (m hostText) Error() string { return string(m) }

// stubHost is the session tool service as the bridge sees it: one token, one
// tool, and a call that can be held to prove the bridge keeps serving.
type stubHost struct {
	mu       sync.Mutex
	attached []string
	hold     chan struct{}
}

func (s *stubHost) Attach(_ context.Context, req *connect.Request[sessiontoolsv1.AttachRequest]) (*connect.Response[sessiontoolsv1.AttachResponse], error) {
	s.mu.Lock()
	s.attached = append(s.attached, req.Msg.SessionToken)
	s.mu.Unlock()
	if req.Msg.SessionToken != bridgeTestToken {
		return nil, connect.NewError(connect.CodePermissionDenied, hostText("the session token is not valid"))
	}
	return connect.NewResponse(&sessiontoolsv1.AttachResponse{SessionId: "s1", Role: "assistant"}), nil
}

func (s *stubHost) ListTools(context.Context, *connect.Request[sessiontoolsv1.ListToolsRequest]) (*connect.Response[sessiontoolsv1.ListToolsResponse], error) {
	return connect.NewResponse(&sessiontoolsv1.ListToolsResponse{Tools: []*sessiontoolsv1.Tool{
		{Name: "with_schema", Description: "d", InputSchema: []byte(`{"type":"object","required":["a"]}`)},
		{Name: "without_schema"},
	}}), nil
}

func (s *stubHost) CallTool(ctx context.Context, req *connect.Request[sessiontoolsv1.CallToolRequest]) (*connect.Response[sessiontoolsv1.CallToolResponse], error) {
	switch req.Msg.Name {
	case "refused":
		return nil, connect.NewError(connect.CodeNotFound, hostText("the tool is not available to this session"))
	case "held":
		select {
		case <-s.hold:
		case <-ctx.Done():
			return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
		}
	}
	return connect.NewResponse(&sessiontoolsv1.CallToolResponse{Text: req.Msg.Name + ":" + string(req.Msg.Arguments)}), nil
}

// serveStub serves the stub over a unix socket with unencrypted HTTP/2, as
// the host does, and returns the socket path.
func serveStub(t *testing.T, stub *stubHost) string {
	t.Helper()
	socket := filepath.Join(serveDir(t), "stub.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(sessiontoolsv1connect.NewSessionToolServiceHandler(stub))
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = listener
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)
	return socket
}

type bridgeSession struct {
	in   *io.PipeWriter
	out  *bufferedLines
	exit chan int
}

// bufferedLines hands the bridge's output to the test line by line.
type bufferedLines struct {
	lines chan []byte
	rest  bytes.Buffer
}

func (b *bufferedLines) Write(p []byte) (int, error) {
	b.rest.Write(p)
	for bytes.IndexByte(b.rest.Bytes(), '\n') >= 0 {
		line, err := b.rest.ReadBytes('\n')
		if err != nil {
			return 0, fmt.Errorf("read a bridge line: %w", err)
		}
		b.lines <- line
	}
	return len(p), nil
}

func startBridge(t *testing.T, socket, token string) *bridgeSession {
	t.Helper()
	reader, writer := io.Pipe()
	s := &bridgeSession{in: writer, out: &bufferedLines{lines: make(chan []byte, 16)}, exit: make(chan int, 1)}
	supervise.Go(t.Context(), "test.bridge", func(ctx context.Context) error {
		s.exit <- RunBridge(ctx, reader, s.out, config.MapEnv{app.SessionTokenEnv: token}, socket, "test-version")
		return nil
	})
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Errorf("close the bridge input: %v", err)
		}
	})
	return s
}

func (s *bridgeSession) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

func (s *bridgeSession) reply(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	select {
	case line := <-s.out.lines:
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("reply %q: %v", line, err)
		}
		if string(msg["jsonrpc"]) != `"2.0"` {
			t.Fatalf("reply %q is not JSON-RPC 2.0", line)
		}
		return msg
	case <-t.Context().Done():
		t.Fatal("no reply from the bridge")
		return nil
	}
}

func TestBridgeSpeaksTheMCPSubset(t *testing.T) {
	t.Parallel()
	stub := &stubHost{hold: make(chan struct{})}
	b := startBridge(t, serveStub(t, stub), bridgeTestToken)

	b.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`)
	if got := string(b.reply(t)["result"]); !strings.Contains(got, `"protocolVersion":"`+mcpProtocolVersion+`"`) ||
		!strings.Contains(got, `"tools":{}`) || !strings.Contains(got, `"version":"test-version"`) {
		t.Errorf("initialize = %s", got)
	}
	// Notifications and noise get no answer; the next reply is the list's.
	b.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	b.send(t, `not json at all`)
	b.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	list := b.reply(t)
	if string(list["id"]) != "2" || !strings.Contains(string(list["result"]), `"inputSchema":{"type":"object","required":["a"]}`) ||
		!strings.Contains(string(list["result"]), `{"name":"without_schema","inputSchema":{"type":"object"}}`) {
		t.Errorf("tools/list = %s", list["result"])
	}

	// A call that waits for the user does not hold up the next request.
	b.send(t, `{"jsonrpc":"2.0","id":"held","method":"tools/call","params":{"name":"held","arguments":{}}}`)
	b.send(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"a":1}}}`)
	call := b.reply(t)
	if string(call["id"]) != "3" || string(call["result"]) != `{"content":[{"type":"text","text":"echo:{\"a\":1}"}],"isError":false}` {
		t.Errorf("tools/call = %s", call["result"])
	}
	close(stub.hold)
	if held := b.reply(t); string(held["id"]) != `"held"` {
		t.Errorf("held call = %v", held)
	}

	assertBridgeErrorsAndClose(t, b)
}

// assertBridgeErrorsAndClose: a host refusal is a readable tool result,
// protocol mistakes are JSON-RPC errors, and closing the input ends the run.
func assertBridgeErrorsAndClose(t *testing.T, b *bridgeSession) {
	t.Helper()
	b.send(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"refused"}}`)
	if got := string(b.reply(t)["result"]); got != `{"content":[{"type":"text","text":"the tool is not available to this session"}],"isError":true}` {
		t.Errorf("a host refusal = %s, want an error result with the host's public text", got)
	}
	b.send(t, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{}}`)
	if got := string(b.reply(t)["error"]); !strings.Contains(got, `"code":-32602`) {
		t.Errorf("tools/call without a name = %s", got)
	}
	b.send(t, `{"jsonrpc":"2.0","id":6,"method":"prompts/list"}`)
	if got := string(b.reply(t)["error"]); !strings.Contains(got, `"code":-32601`) {
		t.Errorf("an unknown method = %s", got)
	}
	b.send(t, `{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	if got := string(b.reply(t)["result"]); got != `{}` {
		t.Errorf("ping = %s", got)
	}

	if err := b.in.Close(); err != nil {
		t.Fatal(err)
	}
	if code := <-b.exit; code != exitOK {
		t.Fatalf("exit after the input closed = %d, want 0", code)
	}
}

func TestBridgeCancelsRunningCallsWhenInputCloses(t *testing.T) {
	t.Parallel()
	stub := &stubHost{hold: make(chan struct{})}
	b := startBridge(t, serveStub(t, stub), bridgeTestToken)
	b.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"held"}}`)
	if err := b.in.Close(); err != nil {
		t.Fatal(err)
	}
	if code := <-b.exit; code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestBridgeRefusesToRunWithoutAttaching(t *testing.T) {
	t.Parallel()
	stub := &stubHost{}
	socket := serveStub(t, stub)
	cases := map[string]struct{ socket, token string }{
		"no token":               {socket, ""},
		"unexpanded placeholder": {socket, "${FUGUE_SESSION_TOKEN}"},
		"no socket":              {"", bridgeTestToken},
		"wrong token":            {socket, "another-token"},
		"no host there":          {filepath.Join(serveDir(t), "absent.sock"), bridgeTestToken},
	}
	for name, tc := range cases {
		var out bytes.Buffer
		code := RunBridge(t.Context(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), &out,
			config.MapEnv{app.SessionTokenEnv: tc.token}, tc.socket, "v")
		if code != exitFailure || out.Len() != 0 {
			t.Errorf("%s: exit %d with output %q, want a failure that writes nothing to the vendor", name, code, out.String())
		}
	}
}

func TestBridgeArguments(t *testing.T) {
	t.Parallel()
	if socket, ok := bridgeArguments([]string{bridgeCommand, bridgeSocketFlag, "/run/fugue.sock"}); !ok || socket != "/run/fugue.sock" {
		t.Errorf("bridgeArguments = %q, %v", socket, ok)
	}
	for _, args := range [][]string{{bridgeCommand}, {bridgeCommand, bridgeSocketFlag}, {bridgeCommand, bridgeSocketFlag, ""}, {bridgeCommand, "--token", "x"}, {"serve"}} {
		if _, ok := bridgeArguments(args); ok {
			t.Errorf("bridgeArguments(%v) accepted", args)
		}
	}
	var out bytes.Buffer
	if code := runMain(t.Context(), []string{bridgeCommand, "--token", "x"}, &out, io.Discard); code != exitUsage || !strings.Contains(out.String(), "mcp-bridge --socket") {
		t.Errorf("usage = %d %q", code, out.String())
	}
}

// A bridge without a usable token is a typed failure that names no value and
// never opens the host socket.
func TestBridgeMissingTokenFailsClosedAndLogsNoValue(t *testing.T) {
	t.Parallel()
	if _, err := sessionToken(config.MapEnv{}); !errors.Is(err, errBridgeNoToken) {
		t.Fatalf("no variable = %v", err)
	}
	if _, err := sessionToken(config.MapEnv{app.SessionTokenEnv: ""}); !errors.Is(err, errBridgeNoToken) {
		t.Fatalf("empty variable = %v", err)
	}
	if cred, err := sessionToken(config.MapEnv{app.SessionTokenEnv: bridgeTestToken}); err != nil || cred.IsZero() {
		t.Fatalf("a real credential was refused: %v", err)
	}
	if errcode.CodeOf(errBridgeNoToken) != errcode.PermissionDenied {
		t.Fatalf("the missing-token code = %q", errcode.CodeOf(errBridgeNoToken))
	}
}
