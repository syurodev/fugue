package hostapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	sessiontoolsv1 "github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1/sessiontoolsv1connect"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
)

const (
	sessionTokenSeed = "hostapi-session-tokens"
	testEchoTool     = "fugue_test_echo"
)

// echoTool is registered only by tests, through HostDeps.TestFugueTools. It
// answers with the session it ran for, so a test sees whose tools a token
// opened.
func echoTool() *app.FugueTool {
	return app.NewReadOnlyTool(testEchoTool, "echo for tests", json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
		func(_ context.Context, sessionID string, args json.RawMessage) (string, error) {
			return "echo:" + sessionID + ":" + string(args), nil
		})
}

// startTokenHost starts a vendor host whose session tokens the test can
// derive: the randomness port is deterministic, so the n-th spawn's token is
// the n-th 32 bytes of the same stream. No test reads a token from the host.
func startTokenHost(t *testing.T, bridgeBinary string, tools ...*app.FugueTool) *vendorHost {
	t.Helper()
	if bridgeBinary == "" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		bridgeBinary = exe
	}
	return startVendorHostWith(t, true, func(deps *app.HostDeps, sockPath string) {
		deps.Random = ids.NewDeterministic(sessionTokenSeed)
		deps.Bridge = app.VendorBridge{Binary: bridgeBinary, Socket: sockPath}
		deps.TestFugueTools = tools
	})
}

// sessionTokens returns the first n tokens the host of startTokenHost mints.
func sessionTokens(t *testing.T, n int) []string {
	t.Helper()
	random := ids.NewDeterministic(sessionTokenSeed)
	tokens := make([]string, 0, n)
	for range n {
		raw := make([]byte, 32)
		if err := random.Read(raw); err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, base64.RawURLEncoding.EncodeToString(raw))
	}
	return tokens
}

// readySession starts a session in a fresh workspace root and waits until it
// is ready; it returns the session ID and its working directory.
func (v *vendorHost) readySession(t *testing.T, commandID, wsID, root string) string {
	t.Helper()
	stream := subscribeVendorSessions(t, v.host, nil)
	defer closeQuietly(stream)
	if wsID == "" {
		wsID = v.workspaceWithRoot(t, root)
	}
	session := v.start(t, commandID, wsID)
	awaitVendorState(t, stream, session.SessionId, "ready")
	return session.SessionId
}

// runFakeAction sends a prompt that makes the fake vendor act and returns
// what it wrote once the turn has ended.
func (v *vendorHost) runFakeAction(t *testing.T, sessionID, root, prompt, file string) []byte {
	t.Helper()
	stream := subscribeVendorSessions(t, v.host, nil)
	defer closeQuietly(stream)
	if err := v.send(t, "action-"+prompt, sessionID, prompt); err != nil {
		t.Fatal(err)
	}
	awaitVendorState(t, stream, sessionID, "running")
	awaitVendorState(t, stream, sessionID, "ready")
	raw, err := fs.ReadFile(os.DirFS(root), file)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// attachSession opens a new connection and attaches it with token.
func attachSession(t *testing.T, sockPath, token string) (sessiontoolsv1connect.SessionToolServiceClient, *http.Client, error) {
	t.Helper()
	_, httpClient := newTestClient(t, sockPath)
	client := sessiontoolsv1connect.NewSessionToolServiceClient(httpClient, "http://fugue")
	_, err := client.Attach(t.Context(), connect.NewRequest(&sessiontoolsv1.AttachRequest{SessionToken: token}))
	if err != nil {
		return client, httpClient, fmt.Errorf("attach the session: %w", err)
	}
	return client, httpClient, nil
}

func callEcho(t *testing.T, client sessiontoolsv1connect.SessionToolServiceClient) (*sessiontoolsv1.CallToolResponse, error) {
	t.Helper()
	res, err := client.CallTool(t.Context(), connect.NewRequest(&sessiontoolsv1.CallToolRequest{Name: testEchoTool, Arguments: []byte(`{"msg":"hi"}`)}))
	if err != nil {
		return nil, fmt.Errorf("call the echo tool: %w", err)
	}
	return res.Msg, nil
}

// localRPCs lists every unary RPC of the Host API outside the session tool
// service, straight from the registered descriptors, so an RPC added later
// is covered without touching this test.
func localRPCs(t *testing.T) []string {
	t.Helper()
	var paths []string
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		pkg := string(file.Package())
		if !strings.HasPrefix(pkg, "fugue.") || pkg == "fugue.sessiontools.v1" {
			return true
		}
		for i := range file.Services().Len() {
			service := file.Services().Get(i)
			for j := range service.Methods().Len() {
				method := service.Methods().Get(j)
				if !method.IsStreamingServer() && !method.IsStreamingClient() {
					paths = append(paths, "/"+string(service.FullName())+"/"+string(method.Name()))
				}
			}
		}
		return true
	})
	if len(paths) < 20 {
		t.Fatalf("only %d local RPCs found: %v", len(paths), paths)
	}
	return paths
}

// postEmpty calls a unary RPC with an empty request over the client's
// connection and returns the Connect error code of the reply.
func postEmpty(t *testing.T, httpClient *http.Client, path string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://fugue"+path, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(res.Body)
	var reply struct {
		Code string `json:"code"`
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode == http.StatusOK {
		return "ok"
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("%s: undecodable reply %q", path, raw)
	}
	return reply.Code
}

func TestSessionPrincipalCannotAnswerUnlockOrReadOtherSessions(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "", echoTool())
	wsID := v.workspaceWithRoot(t, t.TempDir())
	first := v.readySession(t, "ha06-start-1", wsID, "")
	second := v.readySession(t, "ha06-start-2", wsID, "")
	tokens := sessionTokens(t, 2)

	client, httpClient, err := attachSession(t, v.sockPath, tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	// The token opens the tools of its own session and nothing of another.
	res, err := callEcho(t, client)
	if err != nil || res.IsError || res.Text != "echo:"+first+`:{"msg":"hi"}` || strings.Contains(res.Text, second) {
		t.Fatalf("echo through the first token = %+v, %v", res, err)
	}
	assertEveryOtherRPCDenied(t, httpClient)
	assertTopicsDenied(t, httpClient, app.TopicVendorSessions, app.TopicInteraction, "transcript:"+first, "transcript:"+second)
	// It stays the principal it attached as, and a local client never
	// becomes one.
	_, err = client.Attach(t.Context(), connect.NewRequest(&sessiontoolsv1.AttachRequest{SessionToken: tokens[1]}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	if res, err = callEcho(t, client); err != nil || !strings.HasPrefix(res.Text, "echo:"+first+":") {
		t.Fatalf("echo after a refused re-attach = %+v, %v", res, err)
	}
	assertLocalClientCannotAttach(t, v, tokens[1])
}

// assertEveryOtherRPCDenied: answer, list pending, every vendor, workspace
// and host RPC — and whatever unlocks the vault once it has an RPC — is
// denied typed to the session principal.
func assertEveryOtherRPCDenied(t *testing.T, httpClient *http.Client) {
	t.Helper()
	for _, path := range localRPCs(t) {
		if code := postEmpty(t, httpClient, path); code != connect.CodePermissionDenied.String() {
			t.Errorf("%s as the session principal = %q, want permission_denied", path, code)
		}
	}
}

func assertTopicsDenied(t *testing.T, httpClient *http.Client, topics ...string) {
	t.Helper()
	host := hostv1connect.NewHostServiceClient(httpClient, "http://fugue")
	for _, topic := range topics {
		stream, err := host.Subscribe(streamCtx(t), connect.NewRequest(&hostv1.SubscribeRequest{Topic: topic}))
		if err != nil {
			t.Fatal(err)
		}
		if stream.Receive() || connect.CodeOf(stream.Err()) != connect.CodePermissionDenied {
			t.Errorf("subscribe %s as the session principal = %v, want permission_denied", topic, stream.Err())
		}
		closeQuietly(stream)
	}
}

func assertLocalClientCannotAttach(t *testing.T, v *vendorHost, token string) {
	t.Helper()
	_, localHTTP := newTestClient(t, v.sockPath)
	local := sessiontoolsv1connect.NewSessionToolServiceClient(localHTTP, "http://fugue")
	_, err := local.ListTools(t.Context(), connect.NewRequest(&sessiontoolsv1.ListToolsRequest{}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	if _, err = hostv1connect.NewHostServiceClient(localHTTP, "http://fugue").Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		t.Fatal(err)
	}
	_, err = local.Attach(t.Context(), connect.NewRequest(&sessiontoolsv1.AttachRequest{SessionToken: token}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
}

func TestTestOnlyToolIsRefusedByTheNormalHost(t *testing.T) {
	t.Parallel()
	// A host with no test configuration: only the built-in approve relay is served.
	v := startTokenHost(t, "")
	v.readySession(t, "normal-start", "", t.TempDir())
	client, _, err := attachSession(t, v.sockPath, sessionTokens(t, 1)[0])
	if err != nil {
		t.Fatal(err)
	}
	list, err := client.ListTools(t.Context(), connect.NewRequest(&sessiontoolsv1.ListToolsRequest{}))
	if err != nil || len(list.Msg.Tools) != 1 || list.Msg.Tools[0].Name != "approve" {
		t.Fatalf("tools of a normal host = %v, %v; want [approve]", list, err)
	}
	_, err = callEcho(t, client)
	assertConnectCode(t, err, connect.CodeNotFound, errcode.NotFound)
}

// lockedBuffer collects the host's log from every goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("buffer the log: %w", err)
	}
	return n, nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func filesContaining(t *testing.T, needle string, dirs ...string) []string {
	t.Helper()
	var hits []string
	for _, dir := range dirs {
		fsys := os.DirFS(dir)
		err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			raw, readErr := fs.ReadFile(fsys, path)
			if readErr != nil {
				return fmt.Errorf("read %s: %w", path, readErr)
			}
			if bytes.Contains(raw, []byte(needle)) {
				hits = append(hits, filepath.Join(dir, path))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return hits
}

func TestSessionTokenIsRevokedOnCloseAndNeverLeaks(t *testing.T) {
	t.Parallel()
	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(redact.NewJSONHandler(logs, slog.LevelDebug)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	v := startTokenHost(t, "", echoTool())
	root := t.TempDir()
	sessionID := v.readySession(t, "leak-start", "", root)
	token := sessionTokens(t, 1)[0]
	v.assertTokenOnlyInTheEnvironment(t, sessionID, root, token)

	client, _, err := attachSession(t, v.sockPath, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = callEcho(t, client); err != nil {
		t.Fatal(err)
	}
	_, _, wrongErr := attachSession(t, v.sockPath, token+"x")
	assertConnectCode(t, wrongErr, connect.CodePermissionDenied, errcode.PermissionDenied)

	stream := subscribeVendorSessions(t, v.host, nil)
	defer closeQuietly(stream)
	if _, err = v.vendors.CloseSession(t.Context(), connect.NewRequest(&vendorsv1.CloseSessionRequest{CommandId: "leak-close", SessionId: sessionID})); err != nil {
		t.Fatal(err)
	}
	awaitVendorState(t, stream, sessionID, "closed")

	// Closed: the attached connection and the token are both dead.
	_, callErr := callEcho(t, client)
	assertConnectCode(t, callErr, connect.CodePermissionDenied, errcode.PermissionDenied)
	_, _, attachErr := attachSession(t, v.sockPath, token)
	assertConnectCode(t, attachErr, connect.CodePermissionDenied, errcode.PermissionDenied)

	for _, shown := range []string{logs.String(), wrongErr.Error(), callErr.Error(), attachErr.Error()} {
		if strings.Contains(shown, token) {
			t.Fatalf("the session token leaked into a log or an error: %q", shown)
		}
	}
	// Audit, events and every other record live in these directories.
	if hits := filesContaining(t, token, v.dataDir, v.fugueHome, root); len(hits) != 0 {
		t.Fatalf("the session token is on disk in %v", hits)
	}
}

// assertTokenOnlyInTheEnvironment: the vendor process has the token in its
// environment and nowhere else.
func (v *vendorHost) assertTokenOnlyInTheEnvironment(t *testing.T, sessionID, root, token string) {
	t.Helper()
	digest := sha256.Sum256([]byte(token))
	var report fakeVendorReport
	if err := json.Unmarshal(v.runFakeAction(t, sessionID, root, fakeReport, fakeReportFile), &report); err != nil {
		t.Fatal(err)
	}
	if report.TokenSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("the vendor process did not receive the session token in its environment")
	}
	if strings.Contains(strings.Join(report.Args, "\x00"), token) {
		t.Fatal("the session token is in the vendor argv")
	}
	for _, key := range report.EnvKeys {
		if strings.HasPrefix(key, "FUGUE_") && key != app.SessionTokenEnv || strings.HasPrefix(key, "SYNX_") {
			t.Errorf("the vendor environment carries %s", key)
		}
	}
}

func buildFugue(t *testing.T) string {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "fugue")
	var out bytes.Buffer
	cmd := &exec.Cmd{Path: goTool, Args: []string{"go", "build", "-o", binary, "github.com/syurodev/fugue/cmd/fugue"}, Stdout: &out, Stderr: &out}
	if err := cmd.Run(); err != nil {
		t.Fatalf("build fugue: %v\n%s", err, out.String())
	}
	return binary
}

type mcpReply struct {
	ID     json.RawMessage `json:"id"`
	Result struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools *struct{} `json:"tools"`
		} `json:"capabilities"`
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func TestMCPBridgeListsAndCallsToolsOverTheSocket(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, buildFugue(t), echoTool())
	root := t.TempDir()
	sessionID := v.readySession(t, "bridge-start", "", root)

	// The fake vendor spawns the bridge its --mcp-config names, with the
	// environment Claude Code builds, exactly as the vendor CLI does.
	var run fakeBridgeRun
	if err := json.Unmarshal(v.runFakeAction(t, sessionID, root, fakeBridge, fakeBridgeFile), &run); err != nil {
		t.Fatal(err)
	}
	if run.Error != "" || run.Exit != 0 || len(run.Replies) != len(fakeBridgeRequests()) {
		t.Fatalf("bridge run = exit %d, error %q, %d replies", run.Exit, run.Error, len(run.Replies))
	}
	replies := make([]mcpReply, len(run.Replies))
	for i, raw := range run.Replies {
		if err := json.Unmarshal(raw, &replies[i]); err != nil {
			t.Fatalf("reply %d %q: %v", i, raw, err)
		}
	}
	assertBridgeLifecycle(t, &run, replies[:2])
	assertBridgeToolCalls(t, &run, replies[2:], sessionID)
}

func assertBridgeLifecycle(t *testing.T, run *fakeBridgeRun, replies []mcpReply) {
	t.Helper()
	initialize, list := replies[0], replies[1]
	if initialize.Result.ProtocolVersion != "2025-06-18" || initialize.Result.Capabilities.Tools == nil {
		t.Errorf("initialize = %s", run.Replies[0])
	}
	if len(list.Result.Tools) != 2 || list.Result.Tools[0].Name != "approve" || !json.Valid(list.Result.Tools[0].InputSchema) ||
		list.Result.Tools[1].Name != testEchoTool || !json.Valid(list.Result.Tools[1].InputSchema) {
		t.Errorf("tools/list = %s", run.Replies[1])
	}
}

func assertBridgeToolCalls(t *testing.T, run *fakeBridgeRun, replies []mcpReply, sessionID string) {
	t.Helper()
	call, absent, unknown, ping := replies[0], replies[1], replies[2], replies[3]
	if string(call.ID) != `"call-3"` || call.Result.IsError || len(call.Result.Content) != 1 ||
		call.Result.Content[0].Type != "text" || call.Result.Content[0].Text != "echo:"+sessionID+`:{"msg":"hi"}` {
		t.Errorf("tools/call = %s", run.Replies[2])
	}
	if !absent.Result.IsError || len(absent.Result.Content) != 1 {
		t.Errorf("tools/call of an unregistered tool = %s, want an error result", run.Replies[3])
	}
	if unknown.Error == nil || unknown.Error.Code != -32601 {
		t.Errorf("an unknown method = %s, want method not found", run.Replies[4])
	}
	if ping.Error != nil || string(ping.ID) != "6" {
		t.Errorf("ping = %s", run.Replies[5])
	}
}

// assertVendorChildHelloDenied is roadmap Q-41 A end to end: a process of a
// live vendor session dials the socket and says it is the desktop app.
func assertVendorChildHelloDenied(t *testing.T) {
	t.Helper()
	v := startTokenHost(t, "")
	root := t.TempDir()
	sessionID := v.readySession(t, "q41-start", "", root)
	got := string(v.runFakeAction(t, sessionID, root, fakeHello, fakeHelloFile))
	if !strings.HasPrefix(got, connect.CodePermissionDenied.String()+":") {
		t.Fatalf("a vendor process's local hello = %q, want permission_denied", got)
	}
	// The same hello from a process outside every vendor session is served.
	host, _ := newTestClient(t, v.sockPath)
	if _, err := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		t.Fatalf("a local client's hello = %v", err)
	}
}

func newPreInitSessionReq(cmdID, wsID string) *connect.Request[vendorsv1.StartSessionRequest] {
	return connect.NewRequest(&vendorsv1.StartSessionRequest{
		CommandId: cmdID, Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, WorkspaceId: wsID,
		Profile: &vendorsv1.SessionProfile{Model: "fake-model", Effort: "high"},
	})
}

// TestVendorPeerDeniedBeforeHandshakeCompletes verifies that the spawned vendor
// process is registered with localPeerAllowed before the handshake completes
// and removed if spawn fails before reaching a live actor (Q-41 window).
func TestVendorPeerDeniedBeforeHandshakeCompletes(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "")
	root := t.TempDir()
	wsID := v.workspaceWithRoot(t, root)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", filepath.Join(shortDir(t), "preinit.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(ln) })
	if writeErr := os.WriteFile(filepath.Join(root, fakePreInitHelloFile), []byte(ln.Addr().String()), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	startErr := make(chan error, 1)
	supervise.Go(t.Context(), "test.pre-init-start", func(ctx context.Context) error {
		_, callErr := v.vendors.StartSession(ctx, newPreInitSessionReq("preinit-start", wsID))
		startErr <- callErr
		return nil
	})

	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	line, readErr := bufio.NewReader(conn).ReadString('\n')
	closeQuietly(conn)
	if readErr != nil {
		t.Fatal(readErr)
	}
	got := strings.TrimSpace(line)
	if !strings.HasPrefix(got, connect.CodePermissionDenied.String()+":") {
		t.Fatalf("vendor peer hello before handshake = %q, want permission_denied", got)
	}
	if startResErr := <-startErr; startResErr != nil {
		t.Fatalf("StartSession after releasing handshake = %v", startResErr)
	}

	// Failed spawn before handshake must drop the pending owner registration.
	failRoot := t.TempDir()
	failWS := v.workspaceWithRoot(t, failRoot)
	if writeErr := os.WriteFile(filepath.Join(failRoot, fakeFailPreInitFile), []byte("fail"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, failStartErr := v.vendors.StartSession(t.Context(), newPreInitSessionReq("preinit-fail", failWS)); failStartErr == nil {
		t.Fatal("StartSession with pre-init failure succeeded unexpectedly")
	}
	host, _ := newTestClient(t, v.sockPath)
	if _, helloErr := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); helloErr != nil {
		t.Fatalf("local client hello after failed vendor spawn = %v", helloErr)
	}
}

func newDrainConfirmHost(t *testing.T, ran *atomic.Bool) (*vendorHost, string) {
	t.Helper()
	confirmTool := app.NewReadOnlyTool(
		"fugue_test_confirm_drain", "Deploy.",
		json.RawMessage(`{"type":"object","properties":{"env":{"type":"string"}},"additionalProperties":false}`),
		func(context.Context, string, json.RawMessage) (string, error) {
			ran.Store(true)
			return "deployed-drain", nil
		},
	)
	confirmTool.Descriptor.Risk = "destructive"
	confirmTool.Descriptor.Confirm = "always"
	confirmTool.Plan = func(json.RawMessage) (app.FugueToolPlan, error) {
		return app.FugueToolPlan{Summary: "deploy"}, nil
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	v := startVendorHostWith(t, true, func(deps *app.HostDeps, sockPath string) {
		deps.Config.DrainTimeout = 0
		deps.Random = ids.NewDeterministic(sessionTokenSeed)
		deps.Bridge = app.VendorBridge{Binary: exe, Socket: sockPath}
		deps.TestFugueTools = []*app.FugueTool{confirmTool}
	})
	_ = v.readySession(t, "drain-start", "", t.TempDir())
	return v, confirmTool.Name
}

type callOutcome struct {
	res *sessiontoolsv1.CallToolResponse
	err error
}

func startPendingToolCall(t *testing.T, v *vendorHost, toolName string) <-chan callOutcome {
	t.Helper()
	stream, err := v.host.Subscribe(streamCtx(t), connect.NewRequest(&hostv1.SubscribeRequest{Topic: app.TopicInteraction}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(stream)

	client, _, err := attachSession(t, v.sockPath, sessionTokens(t, 1)[0])
	if err != nil {
		t.Fatal(err)
	}

	callCh := make(chan callOutcome, 1)
	supervise.Go(t.Context(), "test.drain-confirm-call", func(ctx context.Context) error {
		resp, callErr := client.CallTool(ctx, connect.NewRequest(&sessiontoolsv1.CallToolRequest{
			Name: toolName, Arguments: []byte(`{"env":"prod"}`),
		}))
		var msg *sessiontoolsv1.CallToolResponse
		if resp != nil {
			msg = resp.Msg
		}
		callCh <- callOutcome{res: msg, err: callErr}
		return nil
	})
	for {
		event := recv(t, stream).GetEvent()
		if event != nil && event.Type == app.EventInteractionRequested {
			return callCh
		}
	}
}

// TestGracefulDrainCancelsPendingToolApproval verifies that a graceful drain
// with DrainTimeout 0 cancels pending session-tool confirmation waits before
// waiting for in-flight RPC handlers, so serve drain cannot hang and the tool
// never runs.
func TestGracefulDrainCancelsPendingToolApproval(t *testing.T) {
	t.Parallel()
	var ran atomic.Bool
	v, toolName := newDrainConfirmHost(t, &ran)
	callCh := startPendingToolCall(t, v, toolName)

	v.hp.Stop("test-graceful-drain")
	select {
	case <-v.hp.DrainDone():
	case <-t.Context().Done():
		t.Fatal("graceful drain with DrainTimeout 0 hung on pending confirmation")
	}

	got := <-callCh
	if got.err != nil {
		t.Fatalf("cancelled confirmation CallTool RPC err = %v, want typed tool denial", got.err)
	}
	if !got.res.IsError {
		t.Fatalf("CallTool during drain = %+v, want IsError=true", got.res)
	}
	if ran.Load() {
		t.Fatal("tool ran despite graceful drain cancelling confirmation")
	}
	pending, _, err := v.hp.Runtime().Interactions().ListPending(t.Context(), string(app.KindApp))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending interactions after drain = %d, want 0", len(pending))
	}
}
