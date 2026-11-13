package hostapi

import (
	"bufio"
	"os"
	"strings"
)

// fakeVendorVersion is the tested Claude CLI version the fake reports.
const fakeVendorVersion = "2.1.319"

// fakeHoldTurn makes the fake leave a turn running until the next steer or
// interrupt arrives.
const fakeHoldTurn = "hold-turn"

// playFakeVendor runs the test binary as a minimal Claude CLI speaking
// stream-json, so the vendor RPC tests exercise the real adapter, process
// supervisor and actor without a vendor account.
func playFakeVendor(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "--version":
		fakeOut(fakeVendorVersion + " (Claude Code)\n")
		return true
	case "-p":
		fakeOut(`{"type":"system","subtype":"init","session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301","tools":["Read"],"claude_code_version":"` +
			fakeVendorVersion + `","model":"fake-model","permissionMode":"default","slash_commands":["clear"],"terminal_slash_commands":[]}` + "\n")
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			fakeVendorLine(scanner.Text())
		}
		return true
	default:
		return false
	}
}

func fakeVendorLine(line string) {
	const result = `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}` + "\n"
	switch {
	case strings.Contains(line, `"subtype":"interrupt"`):
		fakeOut(`{"type":"control_response","response":{"subtype":"success","request_id":"req-interrupt"}}` + "\n")
		fakeOut(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"interrupted","session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}` + "\n")
	case strings.Contains(line, `"type":"user"`) && strings.Contains(line, fakeHoldTurn):
		fakeOut(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"}]},"session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}` + "\n")
	case strings.Contains(line, `"type":"user"`):
		fakeOut(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"reply"}]},"session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}` + "\n")
		fakeOut(result)
	}
}

func fakeOut(s string) {
	if _, err := os.Stdout.WriteString(s); err != nil {
		os.Exit(1)
	}
}
