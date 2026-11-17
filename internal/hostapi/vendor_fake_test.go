package hostapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// fakeVendorVersion is the tested Claude CLI version the fake reports.
const fakeVendorVersion = "2.1.319"

// fakeHoldTurn makes the fake leave a turn running until the next steer or
// interrupt arrives.
const fakeHoldTurn = "hold-turn"

const (
	fakeFailPreInitFile  = "fake-fail-pre-init"
	fakePreInitHelloFile = "fake-pre-init-hello"
)

// playFakeVendor runs the test binary as a minimal Claude CLI speaking
// stream-json, so the vendor RPC tests exercise the real adapter, process
// supervisor and actor without a vendor account.
func playFakeVendor(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "--version":
		if strings.Contains(filepath.Base(args[0]), "agy") {
			fakeOut("1.5.10\n")
			return true
		}
		fakeOut(fakeVendorVersion + " (Claude Code)\n")
		return true
	case "auth":
		// `claude auth status`: the fake account is logged in (F9.10a).
		fakeOut(`{"loggedIn":true}` + "\n")
		return true
	case "login":
		// `codex login status` exits 0 when logged in.
		return true
	case "acp":
		runHostFakeAgyACP()
		return true
	case "app-server":
		runHostFakeCodexAppServer()
		return true
	case "-p":
		fakePreInitHook()
		fakeOut(`{"type":"system","subtype":"init","session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301","tools":["Read"],"claude_code_version":"` +
			fakeVendorVersion + `","model":"fake-model","permissionMode":"default","slash_commands":["clear","terminal","compact"],"terminal_slash_commands":["terminal"]}` + "\n")
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			fakeVendorLine(scanner.Text())
		}
		return true
	default:
		return false
	}
}

func fakePreInitHook() {
	if _, err := os.Stat(fakeFailPreInitFile); err == nil {
		os.Exit(1)
	}
	sockRaw, err := os.ReadFile(fakePreInitHelloFile)
	if err != nil {
		return
	}
	sockPath := strings.TrimSpace(string(sockRaw))
	conn, err := (&net.Dialer{}).Dial("unix", sockPath)
	if err != nil {
		os.Exit(1)
	}
	if _, err := conn.Write([]byte(fakeLocalHello() + "\n")); err != nil {
		closeQuietly(conn)
		os.Exit(1)
	}
	var buf [1]byte
	if _, err := conn.Read(buf[:]); err != nil && !errors.Is(err, io.EOF) {
		closeQuietly(conn)
		os.Exit(1)
	}
	closeQuietly(conn)
}

func fakeVendorLine(line string) {
	const result = `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}` + "\n"
	switch {
	case strings.Contains(line, "change-commands"):
		fakeOut(`{"type":"system","subtype":"init","session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301","tools":["Read"],"claude_code_version":"` + fakeVendorVersion + `","model":"fake-model","permissionMode":"default","slash_commands":["clear","compact","new-command"],"terminal_slash_commands":["compact"]}` + "\n")
	case strings.Contains(line, `"subtype":"interrupt"`):
		fakeOut(`{"type":"control_response","response":{"subtype":"success","request_id":"req-interrupt"}}` + "\n")
		fakeOut(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"interrupted","session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}` + "\n")
	case strings.Contains(line, `"type":"user"`) && strings.Contains(line, fakeHoldTurn):
		fakeOut(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"}]},"session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}` + "\n")
	case strings.Contains(line, `"type":"user"`):
		fakeVendorAction(line)
		fakeOut(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"reply"}]},"session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}` + "\n")
		fakeOut(result)
	}
}

func fakeOut(s string) {
	if _, err := os.Stdout.WriteString(s); err != nil {
		os.Exit(1)
	}
}

type fakeHostACPInbound struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Prompt []struct {
			Text string `json:"text"`
		} `json:"prompt"`
	} `json:"params"`
}

func runHostFakeAgyACP() {
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var heldPromptID string
	for scanner.Scan() {
		var msg fakeHostACPInbound
		if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.Method == "" {
			continue
		}
		var raw struct {
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil {
			continue
		}
		heldPromptID = handleHostFakeACPMessage(&msg, raw.Params, heldPromptID)
	}
}

func handleHostFakeACPMessage(msg *fakeHostACPInbound, rawParams json.RawMessage, heldPromptID string) string {
	const sid = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	id := string(msg.ID)
	switch msg.Method {
	case "initialize":
		fakeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":true,"audio":false,"embeddedContext":true},"sessionCapabilities":{"resume":{},"close":{},"load":{},"list":{},"additionalDirectories":{}}},"agentInfo":{"name":"Antigravity ACP Server","version":"1.5.10"}}}`+"\n", id))
	case "session/new", "session/load":
		fakeRecordAgyBridge(rawParams)
		fakeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"sessionId":%q,"models":{"availableModels":[{"modelId":"gemini-3.8-flash","name":"Gemini 3.8 Flash"}],"currentModelId":"gemini-3.8-flash"},"modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default"}]}}}`+"\n", id, sid))
	case "session/prompt":
		if len(msg.Params.Prompt) > 0 && fakeVendorPromptAction(msg.Params.Prompt[0].Text) {
			fakeOut(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"ACTED"}}}}`+"\n", sid))
			fakeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", id))
			return heldPromptID
		}
		if len(msg.Params.Prompt) > 0 && strings.Contains(msg.Params.Prompt[0].Text, fakeHoldTurn) {
			fakeOut(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"working"}}}}`+"\n", sid))
			return id
		}
		fakeOut(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"PONG"}}}}`+"\n", sid))
		fakeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", id))
	case "session/cancel":
		if heldPromptID != "" {
			fakeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"cancelled"}}`+"\n", heldPromptID))
			return ""
		}
	}
	return heldPromptID
}
