package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/syurodev/fugue/internal/platform/supervise"
)

// The failure-UX scenarios (A-E05) drive the fake vendor CLI through marker
// text in the prompt, so one fake serves every row of activation §3 without
// a fixture per row.
const (
	// failureUXMarkerPrefix starts every failure-UX marker; the plain member
	// goal carries it too, so its process never runs the crash scenario.
	failureUXMarkerPrefix = "FAILUX_"
	// failureUXProposeMarker makes the lead session propose a team. Its
	// parameters follow as ;key=value pairs: members, max_agents,
	// max_duration, quota (a five_hour utilization reported before the
	// proposal) and scope (members write outside their write scope).
	failureUXProposeMarker = "FAILUX_PROPOSE"
	// failureUXScopeMarker is in the goal of a member that writes outside its
	// write scope and reports done.
	failureUXScopeMarker = "FAILUX_OUT_OF_SCOPE"
	// failureUXBounceText is the start of the mailbox text a scope bounce
	// delivers to the member.
	failureUXBounceText    = "out-of-scope files"
	failureUXOutside       = "outside.txt"
	failureUXRoleAssistant = "assistant"
	// failureUXSessionLine is the session id the fake CLI reports.
	failureUXSessionLine = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
)

type failureUXPlan struct {
	members, maxAgents, maxDuration int
	quota                           string
	outOfScope                      bool
}

// parseFailureUXPlan reads the ;key=value parameters that follow the propose
// marker in one stream-json prompt line.
func parseFailureUXPlan(line string) failureUXPlan {
	_, rest, _ := strings.Cut(line, failureUXProposeMarker)
	if end := strings.IndexFunc(rest, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(";=._-", r)
	}); end >= 0 {
		rest = rest[:end]
	}
	var plan failureUXPlan
	for _, pair := range strings.Split(rest, ";") {
		key, value, _ := strings.Cut(pair, "=")
		n := atoiOrZero(value)
		switch key {
		case "members":
			plan.members = n
		case "max_agents":
			plan.maxAgents = n
		case "max_duration":
			plan.maxDuration = n
		case "quota":
			plan.quota = value
		case "scope":
			plan.outOfScope = true
		}
	}
	return plan
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

type failureUXTask struct {
	ID           string   `json:"id"`
	ParentTaskID string   `json:"parent_task_id,omitempty"`
	TeamRole     string   `json:"team_role"`
	Role         string   `json:"role"`
	Goal         string   `json:"goal"`
	Deliverable  string   `json:"deliverable"`
	WriteScope   []string `json:"write_scope"`
}

type failureUXBudget struct {
	MaxAgents          int `json:"max_agents,omitempty"`
	MaxDurationSeconds int `json:"max_duration_seconds,omitempty"`
}

type failureUXProposal struct {
	Title  string           `json:"title"`
	Brief  string           `json:"brief"`
	Tasks  []failureUXTask  `json:"tasks"`
	Budget *failureUXBudget `json:"budget,omitempty"`
}

func (p failureUXPlan) proposalArgs() ([]byte, error) {
	goal := "Implement " + failureUXMarkerPrefix + "MEMBER"
	if p.outOfScope {
		goal = failureUXScopeMarker
	}
	tasks := make([]failureUXTask, 0, 1+p.members)
	tasks = append(tasks, failureUXTask{
		ID: "t1", TeamRole: "lead", Role: crashLeadRole, Goal: "Coordinate", Deliverable: "report", WriteScope: []string{},
	})
	for i := range p.members {
		tasks = append(tasks, failureUXTask{
			ID: "m" + strconv.Itoa(i+1), ParentTaskID: "t1", TeamRole: "member", Role: crashWorkerRole,
			Goal: goal, Deliverable: "diff", WriteScope: []string{"README.md"},
		})
	}
	proposal := failureUXProposal{Title: "Failure UX team", Brief: "Failure UX scenario", Tasks: tasks}
	if p.maxAgents > 0 || p.maxDuration > 0 {
		proposal.Budget = &failureUXBudget{MaxAgents: p.maxAgents, MaxDurationSeconds: p.maxDuration}
	}
	raw, err := json.Marshal(proposal)
	if err != nil {
		return nil, fmt.Errorf("encode the failure-UX proposal: %w", err)
	}
	return raw, nil
}

// failureUXFake is the scripted behavior of one fake vendor process. It
// stays out of the way until a prompt carries a failure-UX marker, so the
// crash-point scenarios keep their own scripted turns.
type failureUXFake struct {
	args []string
	// active is set by the first failure-UX marker.
	active bool
	// member is set once the prompt names the out-of-scope goal; from then on
	// every prompt (the intro, a bounce, a nudge) gets another done report.
	member bool
}

// playLine reacts to one prompt line and reports whether it handled it.
func (f *failureUXFake) playLine(line string) bool {
	if strings.Contains(line, failureUXMarkerPrefix) {
		f.active = true
	}
	if !f.active {
		return false
	}
	if strings.Contains(line, failureUXScopeMarker) {
		f.member = true
	}
	switch {
	case strings.Contains(line, failureUXProposeMarker):
		playFailureUXPropose(f.args, parseFailureUXPlan(line))
	case f.member:
		playFailureUXMemberDone(f.args)
	}
	return true
}

func playFailureUXPropose(args []string, plan failureUXPlan) {
	if plan.quota != "" {
		emitFailureUXLine(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour",` +
			`"unifiedWindows":{"five_hour":{"utilization":` + plan.quota + `,"status":"allowed"}}}}`)
	}
	raw, err := plan.proposalArgs()
	if err != nil {
		os.Exit(1)
	}
	emitFailureUXText("fugue_team_propose: " + callBridgeTool(args, "fugue_team_propose", string(raw)))
}

// playFailureUXMemberDone writes a file outside the member's write scope into
// its worktree (the process working directory) and reports the task done.
func playFailureUXMemberDone(args []string) {
	cwd, err := os.Getwd()
	if err != nil {
		os.Exit(1)
	}
	if wErr := os.WriteFile(filepath.Join(cwd, failureUXOutside), []byte("outside\n"), 0o600); wErr != nil {
		os.Exit(1)
	}
	emitFailureUXText("fugue_task_done: " + callBridgeTool(args, "fugue_task_done", `{"summary":"finished","status":"succeeded"}`))
	// Ending the turn lets the host deliver the mailbox, which carries the
	// scope bounce.
	emitFailureUXLine(`{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","num_turns":1,` +
		`"session_id":"` + failureUXSessionLine + `","result":"done"}`)
}

func emitFailureUXLine(line string) {
	if _, err := os.Stdout.WriteString(line + "\n"); err != nil {
		os.Exit(1)
	}
}

// emitFailureUXText prints one assistant text message, which the host stores
// as a transcript item the test reads back through the Host API.
func emitFailureUXText(text string) {
	type block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		ID      string  `json:"id"`
		Type    string  `json:"type"`
		Role    string  `json:"role"`
		Model   string  `json:"model"`
		Content []block `json:"content"`
	}
	raw, err := json.Marshal(struct {
		Type      string  `json:"type"`
		Message   message `json:"message"`
		SessionID string  `json:"session_id"`
	}{
		Type:      failureUXRoleAssistant,
		Message:   message{ID: "msg-failux-" + strconv.Itoa(len(text)), Type: "message", Role: failureUXRoleAssistant, Model: "fake-model", Content: []block{{Type: "text", Text: text}}},
		SessionID: failureUXSessionLine,
	})
	if err != nil {
		os.Exit(1)
	}
	emitFailureUXLine(string(raw))
}

type bridgeToolResponse struct {
	Result *struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// callBridgeTool calls one Fugue MCP tool through the `fugue mcp-bridge` the
// vendor CLI was configured with and returns the tool's answer text, prefixed
// with "error: " for a refused call.
func callBridgeTool(args []string, tool, arguments string) string {
	bridgeArgs, ok := parseMCPBridgeArgs(args)
	exe, err := os.Executable()
	if !ok || err != nil {
		return "error: no bridge"
	}
	reqs := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"fake-claude","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":` + strconv.Quote(tool) + `,"arguments":` + arguments + `}}`,
	}, "\n") + "\n"
	pr, pw := io.Pipe()
	cmd := exec.CommandContext(context.Background(), "sh")
	cmd.Path = exe
	cmd.Args = append([]string{exe}, bridgeArgs...)
	cmd.Stdin = pr
	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return "error: bridge pipe: " + pipeErr.Error()
	}
	if startErr := cmd.Start(); startErr != nil {
		return "error: bridge did not start: " + startErr.Error()
	}
	supervise.Go(context.Background(), "test.mcp-bridge-write", func(context.Context) error {
		if _, wErr := io.WriteString(pw, reqs); wErr != nil {
			return fmt.Errorf("write mcp-bridge requests: %w", wErr)
		}
		return nil
	})
	text := readBridgeToolResponse(stdout)
	closeErr := pw.Close()
	if waitErr := cmd.Wait(); closeErr != nil || waitErr != nil {
		return "error: the bridge did not exit cleanly: " + text
	}
	return text
}

func readBridgeToolResponse(r io.Reader) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var resp struct {
			ID int `json:"id"`
			bridgeToolResponse
		}
		if json.Unmarshal(sc.Bytes(), &resp) != nil || resp.ID != 2 {
			continue
		}
		return bridgeToolText(&resp.bridgeToolResponse)
	}
	return "error: no response"
}

func bridgeToolText(resp *bridgeToolResponse) string {
	if resp.Error != nil {
		return "error: " + resp.Error.Message
	}
	if resp.Result == nil || len(resp.Result.Content) == 0 {
		return "error: empty response"
	}
	text := resp.Result.Content[0].Text
	if resp.Result.IsError {
		return "error: " + text
	}
	return text
}
