package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	sessiontoolsv1 "github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1/sessiontoolsv1connect"
)

const sessionTestValue = "session-token-for-the-client-test"

type hostText string

func (m hostText) Error() string { return string(m) }

// sessionHost serves the session tool service and records which token
// attached to which connection.
type sessionHost struct {
	sessiontoolsv1connect.UnimplementedSessionToolServiceHandler
	attached chan string
}

func (h *sessionHost) Attach(_ context.Context, req *connect.Request[sessiontoolsv1.AttachRequest]) (*connect.Response[sessiontoolsv1.AttachResponse], error) {
	if h.attached != nil {
		h.attached <- req.Msg.SessionToken
	}
	if req.Msg.SessionToken != sessionTestValue {
		return nil, connect.NewError(connect.CodePermissionDenied, hostText("the session token is not valid"))
	}
	return connect.NewResponse(&sessiontoolsv1.AttachResponse{SessionId: "s1", Role: "lead"}), nil
}

func (*sessionHost) ListTools(context.Context, *connect.Request[sessiontoolsv1.ListToolsRequest]) (*connect.Response[sessiontoolsv1.ListToolsResponse], error) {
	return connect.NewResponse(&sessiontoolsv1.ListToolsResponse{Tools: []*sessiontoolsv1.Tool{{Name: "echo", Description: "d", InputSchema: []byte(`{}`)}}}), nil
}

func (*sessionHost) CallTool(_ context.Context, req *connect.Request[sessiontoolsv1.CallToolRequest]) (*connect.Response[sessiontoolsv1.CallToolResponse], error) {
	if req.Msg.Name == "refused" {
		return nil, connect.NewError(connect.CodeNotFound, hostText("the tool is not available to this session"))
	}
	return connect.NewResponse(&sessiontoolsv1.CallToolResponse{Text: req.Msg.Name + ":" + string(req.Msg.Arguments), IsError: req.Msg.Name == "failing"}), nil
}

func serveSessionHost(t *testing.T, h *sessionHost) string {
	t.Helper()
	socket := filepath.Join(shortDir(t), "fugue.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(sessiontoolsv1connect.NewSessionToolServiceHandler(h))
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)
	return socket
}

func TestDialSessionAttachesAndCallsTools(t *testing.T) {
	t.Parallel()
	h := &sessionHost{attached: make(chan string, 4)}
	c, err := client.DialSession(testCtx(t), serveSessionHost(t, h), client.NewSessionToken(sessionTestValue))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if got := <-h.attached; got != sessionTestValue || c.SessionID() != "s1" || c.Role() != "lead" {
		t.Fatalf("attached token matched = %v, session %q role %q", got == sessionTestValue, c.SessionID(), c.Role())
	}
	assertToolListAndCalls(t, c)
}

func assertToolListAndCalls(t *testing.T, c *client.SessionClient) {
	t.Helper()
	tools, err := c.ListTools(testCtx(t))
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" || string(tools[0].InputSchema) != `{}` {
		t.Fatalf("ListTools = %+v, %v", tools, err)
	}
	if res, callErr := c.CallTool(testCtx(t), "echo", []byte(`{"a":1}`)); callErr != nil || res.IsError || res.Text != `echo:{"a":1}` {
		t.Fatalf("CallTool = %+v, %v", res, callErr)
	}
	if res, callErr := c.CallTool(testCtx(t), "failing", nil); callErr != nil || !res.IsError {
		t.Fatalf("an error result = %+v, %v", res, callErr)
	}
}

// errLocalOnly stands for a local failure whose text names a path.
var errLocalOnly = errors.New("dial unix /secret/path: refused")

func TestHostMessageIsThePublicTextOfARefusal(t *testing.T) {
	t.Parallel()
	c, err := client.DialSession(testCtx(t), serveSessionHost(t, &sessionHost{}), client.NewSessionToken(sessionTestValue))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	_, err = c.CallTool(testCtx(t), "refused", nil)
	if connect.CodeOf(err) != connect.CodeNotFound || client.HostMessage(err) != "the tool is not available to this session" {
		t.Fatalf("a refusal = %v (message %q)", err, client.HostMessage(err))
	}
	for _, localErr := range []error{
		errLocalOnly,
		connect.NewError(connect.CodeUnavailable, errLocalOnly),
		fmt.Errorf("call the session tool: %w", connect.NewError(connect.CodeUnavailable, errLocalOnly)),
	} {
		if got := client.HostMessage(localErr); strings.Contains(got, "secret") || got != "the fugue host could not be reached" {
			t.Fatalf("a local error %v reached the vendor: %q", localErr, got)
		}
	}
}

func TestDialSessionFailsClosedWithoutAUsableToken(t *testing.T) {
	t.Parallel()
	h := &sessionHost{attached: make(chan string, 4)}
	socket := serveSessionHost(t, h)
	if _, err := client.DialSession(testCtx(t), socket, client.NewSessionToken("")); !errors.Is(err, client.ErrSessionTokenMissing) {
		t.Fatalf("an empty token = %v, want ErrSessionTokenMissing", err)
	}
	if len(h.attached) != 0 {
		t.Fatal("an empty token reached the host")
	}
	_, err := client.DialSession(testCtx(t), socket, client.NewSessionToken("a-wrong-token-value"))
	if connect.CodeOf(err) != connect.CodePermissionDenied || strings.Contains(err.Error(), "a-wrong-token-value") {
		t.Fatalf("a wrong token = %v, want permission_denied without the token", err)
	}
	_, err = client.DialSession(testCtx(t), filepath.Join(shortDir(t), "absent.sock"), client.NewSessionToken(sessionTestValue))
	if !errors.Is(err, client.ErrHostNotRunning) || strings.Contains(err.Error(), sessionTestValue) {
		t.Fatalf("no host = %v, want ErrHostNotRunning without the token", err)
	}
}

func TestSessionTokenNeverPrints(t *testing.T) {
	t.Parallel()
	cred := client.NewSessionToken(sessionTestValue)
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("dial", "cred", cred)
	encoded, err := json.Marshal(map[string]client.SessionToken{"cred": cred})
	if err != nil {
		t.Fatal(err)
	}
	shown := []string{
		cred.String(), fmt.Sprint(cred), fmt.Sprintf("%v %+v %#v %s %q %x", cred, cred, cred, cred, cred, cred),
		logs.String(), string(encoded),
	}
	for _, s := range shown {
		if strings.Contains(s, sessionTestValue) {
			t.Fatalf("the token printed itself: %q", s)
		}
	}
}
