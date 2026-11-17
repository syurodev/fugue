package hostapi

// The fake vendor CLIs also play the client side of the Fugue MCP bridge, the
// way each real CLI builds the client: Claude spawns the server its
// --mcp-config names with the config's expanded env entries (fakeRunBridge),
// Codex spawns the server named by its `-c mcp_servers.fugue.*` overrides
// with the variables its `env_vars` list names, agy spawns the mcpServers
// entry of session/new with its own environment inherited. All write what
// they saw into the working directory, tokens only as digests (F9.11).

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/syurodev/fugue/internal/app"
)

const (
	// fakeVendorReportPrompt asks the fake vendor CLI to report what it was
	// started with.
	fakeVendorReportPrompt = "fake-report"
	fakeVendorReportFile   = ".fake-report.json"
	fakeVendorBridgeFile   = ".fugue-bridge.json"
	// fakeVendorBridgePrompt asks the fake vendor CLI to run the fugue MCP
	// server.
	fakeVendorBridgePrompt  = "fake-bridge"
	fakeVendorBridgeRunFile = ".fugue-bridge-run.json"
)

// fakeVendorStartup is what the fake saw at startup. It never writes a
// token, only its digest.
type fakeVendorStartup struct {
	Args        []string `json:"args"`
	EnvKeys     []string `json:"env_keys"`
	TokenSHA256 string   `json:"token_sha256"`
}

// fakeVendorBridgeSpec is the bridge command a fake vendor CLI recorded.
type fakeVendorBridgeSpec struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// fakeVendorBridgeOutcome is the result of the MCP conversation the fake held
// with the bridge.
type fakeVendorBridgeOutcome struct {
	Replies []json.RawMessage `json:"replies"`
	Exit    int               `json:"exit"`
	Error   string            `json:"error,omitempty"`
}

// fakeWriteVendorStartup reports the fake vendor CLI's argv and environment
// keys. The token is never written, only its digest.
func fakeWriteVendorStartup() {
	report := fakeVendorStartup{Args: os.Args}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		report.EnvKeys = append(report.EnvKeys, key)
		if key == app.SessionTokenEnv {
			sum := sha256.Sum256([]byte(value))
			report.TokenSHA256 = hex.EncodeToString(sum[:])
		}
	}
	sort.Strings(report.EnvKeys)
	raw, err := json.Marshal(report)
	if err != nil {
		os.Exit(3)
	}
	fakeWrite(fakeVendorReportFile, raw)
}

// fakeRecordCodexBridge reads the fugue server config from the
// `-c mcp_servers.fugue.*=value` overrides on the fake codex argv, where -c
// and its value are separate entries. The values are TOML: a quoted string
// for command, an array for args.
func fakeRecordCodexBridge() {
	var command string
	var args []string
	have := false
	for i, arg := range os.Args {
		if arg != "-c" || i+1 >= len(os.Args) {
			continue
		}
		value := os.Args[i+1]
		if v, ok := strings.CutPrefix(value, "mcp_servers.fugue.command="); ok {
			command = strings.Trim(v, `"`)
			have = true
			continue
		}
		if v, ok := strings.CutPrefix(value, "mcp_servers.fugue.args="); ok {
			if json.Unmarshal([]byte(v), &args) != nil {
				os.Exit(3)
			}
			have = true
		}
	}
	if have && command != "" {
		fakeWriteBridgeSpec(fakeVendorBridgeSpec{Command: command, Args: args})
	}
}

// fakeRecordAgyBridge is the mcpServers entry the fake agy session/new saw.
func fakeRecordAgyBridge(params json.RawMessage) {
	var req struct {
		McpServers []struct {
			Name    string   `json:"name"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(params, &req) != nil {
		return
	}
	for _, server := range req.McpServers {
		if server.Name == "fugue" && server.Command != "" {
			fakeWriteBridgeSpec(fakeVendorBridgeSpec{Command: server.Command, Args: server.Args})
		}
	}
}

func fakeWriteBridgeSpec(spec fakeVendorBridgeSpec) {
	raw, err := json.Marshal(spec)
	if err != nil {
		os.Exit(3)
	}
	fakeWrite(fakeVendorBridgeFile, raw)
}

// fakeVendorChildEnv builds the environment of the bridge process the way the
// fake's vendor does: Codex passes a standard set plus the names its
// `env_vars` override lists; agy hands its own environment, token included,
// on to the child. Both carry the token only through FUGUE_SESSION_TOKEN.
func fakeVendorChildEnv() []string {
	own := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		own[key] = value
	}
	var passThrough []string
	for i, arg := range os.Args {
		if arg != "-c" || i+1 >= len(os.Args) {
			continue
		}
		if value, ok := strings.CutPrefix(os.Args[i+1], "mcp_servers.fugue.env_vars="); ok {
			if json.Unmarshal([]byte(value), &passThrough) != nil {
				os.Exit(3)
			}
		}
	}
	if passThrough == nil {
		// agy semantics: the child inherits the vendor's environment.
		return os.Environ()
	}
	env := []string{"PATH=" + own["PATH"], "HOME=" + own["HOME"]}
	for _, name := range passThrough {
		env = append(env, name+"="+own[name])
	}
	return env
}

// runFakeVendorBridge answers fakeVendorBridgePrompt: it spawns the recorded
// bridge with its vendor's child environment and holds one short MCP
// conversation with it.
func runFakeVendorBridge() {
	spawned, outcome := spawnFakeBridge()
	if spawned != nil {
		outcome = new(fakeVendorBridgeOutcome)
		*outcome = holdBridgeConversation(spawned.stdin, spawned.stdout)
		closeErr := spawned.stdin.Close()
		waitErr := spawned.cmd.Wait()
		if outcome.Error == "" && closeErr != nil {
			outcome.Error = closeErr.Error()
		}
		if outcome.Error == "" && waitErr != nil {
			outcome.Error = "bridge exit: " + waitErr.Error()
		}
		outcome.Exit = spawned.cmd.ProcessState.ExitCode()
	}
	fakeWriteBridgeRun(*outcome)
}

// spawnedBridge is the bridge process of the current action.
type spawnedBridge struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// spawnFakeBridge reads the recorded spec and spawns the bridge; a non-nil
// outcome is the error to record instead of a conversation.
func spawnFakeBridge() (*spawnedBridge, *fakeVendorBridgeOutcome) {
	raw, err := os.ReadFile(fakeVendorBridgeFile)
	if err != nil {
		return nil, &fakeVendorBridgeOutcome{Exit: -1, Error: "no bridge recorded"}
	}
	var spec fakeVendorBridgeSpec
	if json.Unmarshal(raw, &spec) != nil || spec.Command == "" {
		return nil, &fakeVendorBridgeOutcome{Exit: -1, Error: "no bridge recorded"}
	}
	cmd := &exec.Cmd{Path: spec.Command, Args: append([]string{spec.Command}, spec.Args...), Env: fakeVendorChildEnv()}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &fakeVendorBridgeOutcome{Exit: -1, Error: bridgePipesError}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &fakeVendorBridgeOutcome{Exit: -1, Error: bridgePipesError}
	}
	if err := cmd.Start(); err != nil {
		return nil, &fakeVendorBridgeOutcome{Exit: -1, Error: "start the bridge: " + err.Error()}
	}
	return &spawnedBridge{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

// bridgePipesError is why a bridge conversation could not start.
const bridgePipesError = "bridge pipes"

// holdBridgeConversation holds the fake's one MCP conversation with the
// spawned bridge: the initialized notification, the request list, one reply
// per request.
func holdBridgeConversation(stdin io.WriteCloser, stdout io.ReadCloser) fakeVendorBridgeOutcome {
	run := fakeVendorBridgeOutcome{}
	replies := bufio.NewReader(stdout)
	if _, err := fmt.Fprint(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		run.Error = err.Error()
		return run
	}
	for _, request := range fakeVendorBridgeRequests() {
		if _, err := fmt.Fprint(stdin, request+"\n"); err != nil {
			run.Error = err.Error()
			return run
		}
		reply, err := replies.ReadBytes('\n')
		if err != nil {
			run.Error = "read the bridge reply: " + err.Error()
			return run
		}
		run.Replies = append(run.Replies, json.RawMessage(strings.TrimRight(string(reply), "\r\n")))
	}
	return run
}

func fakeWriteBridgeRun(run fakeVendorBridgeOutcome) {
	raw, err := json.Marshal(run)
	if err != nil {
		os.Exit(3)
	}
	fakeWrite(fakeVendorBridgeRunFile, raw)
}

// fakeVendorBridgeRequests is the MCP conversation the fake holds with the
// bridge: lifecycle, a tool call of the registered test tool, the approve
// relay, and ping.
func fakeVendorBridgeRequests() []string {
	return []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fake-vendor","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"call-3","method":"tools/call","params":{"name":"fugue_test_echo","arguments":{"msg":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"tu-fake"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`,
	}
}

// fakeVendorPromptAction runs the actions a vendor-protocol prompt asks for
// and reports whether one matched.
func fakeVendorPromptAction(prompt string) bool {
	switch prompt {
	case fakeVendorReportPrompt:
		fakeWriteVendorStartup()
		return true
	case fakeVendorBridgePrompt:
		runFakeVendorBridge()
		return true
	}
	return false
}

// runHostFakeCodexAppServer speaks the JSON-RPC subset the Codex adapter
// needs: handshake, thread start and resume, turns, interrupt.
func runHostFakeCodexAppServer() {
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
	fakeRecordCodexBridge()
	const threadID = "019a8000-0000-7000-8000-000000000001"
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		id := string(msg.ID)
		prompt := ""
		if len(msg.Params.Input) > 0 {
			prompt = msg.Params.Input[0].Text
		}
		switch msg.Method {
		case "initialize":
			fakeOut(fmt.Sprintf(`{"id":%s,"result":{"userAgent":"codex_cli_rs/0.167.0 (Darwin; arm64) terminal","codexHome":"/home/REDACTED/.codex","platformFamily":"unix","platformOs":"macos"}}`+"\n", id))
		case "initialized":
		case "account/rateLimits/read":
			fakeOut(fmt.Sprintf(`{"id":%s,"result":{"rateLimits":{"limitId":"codex","limitName":null,"planType":"pro","primary":{"usedPercent":10,"windowDurationMins":300,"resetsAt":1800000000},"secondary":null,"rateLimitReachedType":null},"rateLimitsByLimitId":{}}}`+"\n", id))
		case "thread/start", "thread/resume":
			fakeOut(fmt.Sprintf(`{"id":%s,"result":{"thread":{"id":%q,"name":null,"cwd":"/home/REDACTED/codex-fixture"},"model":"gpt-5.4","approvalPolicy":"on-request","reasoningEffort":null}}`+"\n", id, threadID))
			fakeOut(fmt.Sprintf(`{"method":"thread/started","params":{"thread":{"id":%q}}}`+"\n", threadID))
		case "turn/start", "turn/steer":
			fakeOut(fmt.Sprintf(`{"id":%s,"result":{"turn":{"id":"t-1","items":[],"status":"inProgress","error":null}}}`+"\n", id))
			fakeOut(fmt.Sprintf(`{"method":"turn/started","params":{"threadId":%q,"turn":{"id":"t-1","items":[],"status":"inProgress","error":null}}}`+"\n", threadID))
			fakeVendorPromptAction(prompt)
			fakeOut(fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":"t-1","items":[],"status":"completed","error":null}}}`+"\n", threadID))
		case "turn/interrupt":
			fakeOut(fmt.Sprintf(`{"id":%s,"result":{}}`+"\n", id))
			fakeOut(fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":"t-1","items":[],"status":"interrupted","error":null}}}`+"\n", threadID))
		}
	}
}
