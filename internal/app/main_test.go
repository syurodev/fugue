package app

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// The vendor service tests run this test binary as the Claude CLI.
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		writeFakeCLI(fakeCLIVersion + " (Claude Code)\n")
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "-p" {
		runFakeCLISession()
		os.Exit(0)
	}
	goleak.VerifyTestMain(m)
}

const (
	fakeCLIVersion = "2.1.319"
	fakeCLIHold    = "hold-turn"
	fakeCLISession = `"session_id":"9b2e4f10-5c1d-4e8a-9f3b-2d7c6a1e0b55"`
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

func writeFakeCLI(s string) {
	if _, err := os.Stdout.WriteString(s); err != nil {
		os.Exit(1)
	}
}
