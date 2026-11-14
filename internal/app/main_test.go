package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/interaction"
)

func TestMain(m *testing.M) {
	// The vendor service tests run this test binary as the Claude or Codex CLI.
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		if strings.Contains(filepath.Base(os.Args[0]), "codex") {
			writeFakeCLI(fakeCodexVersion + "\n")
			os.Exit(0)
		}
		writeFakeCLI(fakeCLIVersion + " (Claude Code)\n")
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "-p" {
		runFakeCLISession()
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "app-server" {
		runFakeCodexAppServer()
		os.Exit(0)
	}
	goleak.VerifyTestMain(m)
}

const (
	fakeCLIVersion     = "2.1.319"
	fakeCodexVersion   = "codex-cli 0.167.0"
	fakeCLIHold        = "hold-turn"
	fakeCLISession     = `"session_id":"9b2e4f10-5c1d-4e8a-9f3b-2d7c6a1e0b55"`
	fakeCodexThreadID  = "9b2e4f10-5c1d-4e8a-9f3b-2d7c6a1e0b55"
	fakeCodexStdinFile = ".codex-stdin.jsonl"
)

// runFakeCLISession speaks the stream-json subset the adapter needs: init,
// a completed turn per message, a held turn for fakeCLIHold, and interrupt.
func runFakeCLISession() {
	writeFakeCLI(`{"type":"system","subtype":"init",` + fakeCLISession + `,"tools":[],"claude_code_version":"` + fakeCLIVersion +
		`","model":"m","permissionMode":"default","slash_commands":[],"terminal_slash_commands":[]}` + "\n")
	lines := bufio.NewScanner(os.Stdin)
	for lines.Scan() {
		line := lines.Text()
		interrupt := strings.Contains(line, `"subtype":"interrupt"`)
		user := strings.Contains(line, `"type":"user"`)
		if interrupt {
			writeFakeCLI(`{"type":"control_response","response":{"subtype":"success","request_id":"r"}}` + "\n")
			writeFakeCLI(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"stop",` + fakeCLISession + "}\n")
		}
		if user && !strings.Contains(line, fakeCLIHold) {
			writeFakeCLI(`{"type":"result","subtype":"success","is_error":false,"result":"ok",` + fakeCLISession + "}\n")
		}
	}
}

type fakeCodexInbound struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Input []struct {
			Text string `json:"text"`
		} `json:"input"`
	} `json:"params"`
}

func runFakeCodexAppServer() {
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
	lines := bufio.NewScanner(os.Stdin)
	lines.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for lines.Scan() {
		line := lines.Text()
		appendFakeCodexStdin(line)
		var msg fakeCodexInbound
		if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.Method == "" {
			continue
		}
		handleFakeCodexMethod(string(msg.ID), msg.Method, extractFakeCodexPrompt(&msg))
	}
}

func extractFakeCodexPrompt(msg *fakeCodexInbound) string {
	if len(msg.Params.Input) > 0 {
		return msg.Params.Input[0].Text
	}
	return ""
}

func handleFakeCodexMethod(id, method, prompt string) {
	switch method {
	case "initialize":
		writeFakeCLI(fmt.Sprintf(`{"id":%s,"result":{"userAgent":"codex_cli_rs/0.167.0 (Darwin)","codexHome":"/tmp","platformFamily":"unix","platformOs":"macos"}}`+"\n", id))
	case "account/rateLimits/read":
		writeFakeCLI(fmt.Sprintf(`{"id":%s,"result":{"rateLimits":{"limitId":"codex","limitName":null,"planType":"pro","primary":{"usedPercent":10,"windowDurationMins":300,"resetsAt":1800000000},"secondary":null,"rateLimitReachedType":null},"rateLimitsByLimitId":{}}}`+"\n", id))
	case "thread/start", "thread/resume":
		writeFakeCLI(fmt.Sprintf(`{"id":%s,"result":{"thread":{"id":%q},"model":"gpt-5.4","approvalPolicy":"on-request"}}`+"\n", id, fakeCodexThreadID))
		writeFakeCLI(fmt.Sprintf(`{"method":"thread/started","params":{"thread":{"id":%q}}}`+"\n", fakeCodexThreadID))
	case "turn/start", "review/start", "thread/compact/start":
		handleFakeCodexTurnStart(id, prompt)
	case "turn/steer":
		handleFakeCodexTurnSteer(id, prompt)
	case "turn/interrupt":
		writeFakeCLI(fmt.Sprintf(`{"id":%s,"result":{}}`+"\n", id))
		writeFakeCLI(fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":"t-1","items":[],"status":"interrupted","error":null}}}`+"\n", fakeCodexThreadID))
	}
}

func handleFakeCodexTurnStart(id, prompt string) {
	writeFakeCLI(fmt.Sprintf(`{"id":%s,"result":{"turn":{"id":"t-1","items":[],"status":"inProgress","error":null}}}`+"\n", id))
	writeFakeCLI(fmt.Sprintf(`{"method":"turn/started","params":{"threadId":%q,"turn":{"id":"t-1","items":[],"status":"inProgress","error":null}}}`+"\n", fakeCodexThreadID))
	if emitFakeCodexServerRequests(prompt) {
		return
	}
	if !strings.Contains(prompt, fakeCLIHold) {
		writeFakeCLI(fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":"t-1","items":[],"status":"completed","error":null}}}`+"\n", fakeCodexThreadID))
	}
}

func handleFakeCodexTurnSteer(id, prompt string) {
	writeFakeCLI(fmt.Sprintf(`{"id":%s,"result":{"turnId":"t-1"}}`+"\n", id))
	if emitFakeCodexServerRequests(prompt) {
		return
	}
	writeFakeCLI(fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":"t-1","items":[],"status":"completed","error":null}}}`+"\n", fakeCodexThreadID))
}

func emitFakeCodexServerRequests(prompt string) bool {
	rawReq, ok := strings.CutPrefix(prompt, "server-request:")
	if !ok {
		return false
	}
	if strings.TrimSpace(rawReq) == "oversized" {
		prefix := fmt.Sprintf(`{"threadId":%q,"turnId":"t-1","itemId":"call-big","command":"`, fakeCodexThreadID)
		suffix := `"}`
		fill := interaction.MaxRelayedDetailBytes + 1 - len(prefix) - len(suffix)
		oversizedParams := prefix + strings.Repeat("x", fill) + suffix
		writeFakeCLI(fmt.Sprintf(`{"id":201,"method":"item/commandExecution/requestApproval","params":%s}`+"\n", oversizedParams))
		return true
	}
	for _, reqLine := range strings.Split(strings.TrimSpace(rawReq), "\n") {
		if strings.TrimSpace(reqLine) != "" {
			writeFakeCLI(reqLine + "\n")
		}
	}
	return true
}

func appendFakeCodexStdin(line string) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Clean(filepath.Join(cwd, fakeCodexStdinFile)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if _, wErr := f.WriteString(line + "\n"); wErr != nil {
		os.Exit(1)
	}
	if cErr := f.Close(); cErr != nil {
		os.Exit(1)
	}
}

func writeFakeCLI(s string) {
	if _, err := os.Stdout.WriteString(s); err != nil {
		os.Exit(1)
	}
}
