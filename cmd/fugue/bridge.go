package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/pkg/client"
)

// The command line the host writes into a vendor session's MCP configuration.
const (
	bridgeCommand    = "mcp-bridge"
	bridgeSocketFlag = "--socket"
)

// mcpProtocolVersion is the one MCP revision the bridge speaks. It answers
// initialize with it whatever the client asked for, as the revision's
// lifecycle rules allow; a client that cannot speak it disconnects.
const mcpProtocolVersion = "2025-06-18"

// mcpMaxLineBytes bounds one JSON-RPC line read from the vendor.
const mcpMaxLineBytes = 4 << 20

// JSON-RPC error codes the bridge returns.
const (
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError"`
}

// bridge is a minimal MCP stdio server — initialize, ping, tools/list and
// tools/call — that proxies the tool methods to the host's session tool
// service. It holds no authority of its own: the host decides everything
// from the session token the bridge attached with. What it writes to the
// vendor is tool data for the vendor's model, never an instruction to Fugue.
type bridge struct {
	session *client.SessionClient
	version string

	writeMu sync.Mutex
	out     io.Writer
}

// errBridgeNoToken is why a bridge refuses to start: it never runs
// unauthenticated, and the vendor's variable expansion leaves a literal
// "${...}" behind when the variable is unset.
var errBridgeNoToken = errcode.New(errcode.PermissionDenied, "the session token is missing")

// sessionToken reads the token from the environment. An empty value and an
// unexpanded "${NAME}" placeholder both count as missing.
func sessionToken(env config.Env) (client.SessionToken, error) {
	value, _ := env.Lookup(app.SessionTokenEnv)
	if value == "" || strings.HasPrefix(value, "${") {
		return client.SessionToken{}, errBridgeNoToken
	}
	return client.NewSessionToken(value), nil
}

// RunBridge serves MCP on in and out until in closes, then cancels the calls
// still running and returns the exit code. Nothing it logs carries the token.
func RunBridge(ctx context.Context, in io.Reader, out io.Writer, env config.Env, socket, version string) int {
	token, err := sessionToken(env)
	if err != nil || socket == "" {
		slog.Error("fugue mcp-bridge needs the session token and the host socket", "code", string(errcode.CodeOf(errBridgeNoToken)))
		return exitFailure
	}
	session, err := client.DialSession(ctx, socket, token)
	if err != nil {
		slog.Error("fugue mcp-bridge could not attach to the host", "code", connect.CodeOf(err).String())
		return exitFailure
	}
	defer session.Close()
	b := &bridge{session: session, version: version, out: out}
	if err := b.serve(ctx, in); err != nil {
		slog.Error("fugue mcp-bridge stopped", "code", string(errcode.CodeOf(err)))
		return exitFailure
	}
	return exitOK
}

// serve answers each request on its own task, so a call that waits for the
// user does not hold up the next one. It returns when the input closes.
func (b *bridge) serve(ctx context.Context, in io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	var calls sync.WaitGroup
	defer calls.Wait()
	defer cancel()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64<<10), mcpMaxLineBytes)
	for scanner.Scan() {
		var req rpcRequest
		// A line that is not a request with an id is a notification or noise;
		// neither gets an answer.
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil || len(req.ID) == 0 || req.Method == "" {
			continue
		}
		calls.Add(1)
		supervise.Go(ctx, "mcp-bridge.request", func(ctx context.Context) error {
			defer calls.Done()
			return b.reply(req.ID, b.handle(ctx, &req))
		})
	}
	if err := scanner.Err(); err != nil {
		return errcode.Wrap(errcode.Internal, "read the mcp input", err)
	}
	return nil
}

func (b *bridge) reply(id json.RawMessage, result any) error {
	msg := rpcReply{JSONRPC: "2.0", ID: id, Result: result}
	if rpcErr, ok := result.(*rpcError); ok {
		msg.Result, msg.Error = nil, rpcErr
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "encode the mcp reply", err)
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if _, err := b.out.Write(append(raw, '\n')); err != nil {
		return errcode.Wrap(errcode.Internal, "write the mcp reply", err)
	}
	return nil
}

func (b *bridge) handle(ctx context.Context, req *rpcRequest) any {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "fugue", "version": b.version},
		}
	case "ping":
		return map[string]any{}
	case "tools/list":
		return b.listTools(ctx)
	case "tools/call":
		return b.callTool(ctx, req.Params)
	default:
		return &rpcError{Code: rpcMethodNotFound, Message: "method not found"}
	}
}

func (b *bridge) listTools(ctx context.Context) any {
	listed, err := b.session.ListTools(ctx)
	if err != nil {
		return &rpcError{Code: rpcInternalError, Message: client.HostMessage(err)}
	}
	tools := make([]mcpTool, 0, len(listed))
	for _, tool := range listed {
		schema := json.RawMessage(tool.InputSchema)
		if !json.Valid(schema) {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		tools = append(tools, mcpTool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
	}
	return map[string]any{"tools": tools}
}

func (b *bridge) callTool(ctx context.Context, params json.RawMessage) any {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil || call.Name == "" {
		return &rpcError{Code: rpcInvalidParams, Message: "tools/call needs a tool name"}
	}
	res, err := b.session.CallTool(ctx, call.Name, call.Arguments)
	if err != nil {
		// A host refusal is a tool result the model can read, as MCP asks of
		// tool execution errors.
		return mcpCallResult{Content: []mcpContent{{Type: "text", Text: client.HostMessage(err)}}, IsError: true}
	}
	return mcpCallResult{Content: []mcpContent{{Type: "text", Text: res.Text}}, IsError: res.IsError}
}
