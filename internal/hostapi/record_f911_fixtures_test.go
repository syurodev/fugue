//go:build fugue_record

package hostapi

// Manual recording of the F9.11 real-CLI fixtures (vendor-runtime §5.2, §5.3):
//
//	go test -tags fugue_record -run TestRecordF911Fixtures ./internal/hostapi/
//
// It starts a real host serving the echo tool over the real socket, then
// drives the real codex and agy CLIs through one turn each that calls the
// fugue echo tool through `fugue mcp-bridge`. The captured vendor stdout is
// redacted and written into the adapters' testdata fixtures; the golden
// transcripts regenerate with `-update` in each adapter package. Requires a
// logged-in codex and agy; CI never runs it (build tag).

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/agy"
	"github.com/syurodev/fugue/internal/vendoradapter/codex"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
)

const recordPrompt = "Call the fugue_test_echo tool exactly twice with the arguments {\"msg\":\"hi\"} (two separate tool calls, waiting for each result). Then reply with only the word DONE."

func TestRecordF911Fixtures(t *testing.T) {
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		t.Skipf("codex not installed: %v", err)
	}
	agyBin, err := exec.LookPath("agy")
	if err != nil {
		t.Skipf("agy not installed: %v", err)
	}
	logs, _ := os.Create("/tmp/record-host.log")
	slog.SetDefault(slog.New(redact.NewJSONHandler(logs, slog.LevelDebug)))
	v := startVendorHostWith(t, true, func(deps *app.HostDeps, sockPath string) {
		deps.Random = ids.NewDeterministic(recordTokenSeed)
		deps.Bridge = app.VendorBridge{Binary: buildFugue(t), Socket: sockPath}
		deps.TestFugueTools = []*app.FugueTool{echoTool()}
		deps.CodexBinary = codexBin
		deps.AgyBinary = agyBin
		deps.VendorHostEnv = os.Environ()
	})

	recordCodex(t, v, codexBin)
	if os.Getenv("FUGUE_RECORD_AGY") == "1" {
		recordAgy(t, v, agyBin)
	}
}

// recordAgyModelEnv names the agy model the recording selects through ACP
// `session/set_config_option` (`configId: "model"`, the same call Fugue's own
// agy adapter makes). The default is a model with quota on the recording
// account; the exhausted "Argon" family is refused so a quota error is never
// recorded as a fixture.
const (
	recordAgyModelEnv     = "FUGUE_RECORD_AGY_MODEL"
	recordAgyDefaultModel = "gemini-4-hydrogen"
)

func recordAgyModel(t *testing.T) string {
	t.Helper()
	model := os.Getenv(recordAgyModelEnv)
	if model == "" {
		model = recordAgyDefaultModel
	}
	if strings.Contains(strings.ToLower(model), "argon") {
		t.Fatalf("%s=%q: the Argon models are out of quota on the recording account", recordAgyModelEnv, model)
	}
	return model
}

// recordTokenSeed derives the tokens of the host: the first spawn's token is
// the first 32 bytes of the stream, the second spawn's the next 32, so the
// recorded CLI's bridge can attach while the recording runs.
const recordTokenSeed = "f9.11-record-seed"

// recordingToken returns the token of the n-th spawn of the host.
func recordingToken(t *testing.T, n int) string {
	t.Helper()
	random := ids.NewDeterministic(recordTokenSeed)
	for range n {
		raw := make([]byte, 32)
		if err := random.Read(raw); err != nil {
			t.Fatal(err)
		}
	}
	raw := make([]byte, 32)
	if err := random.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func recordCodex(t *testing.T, v *vendorHost, codexBin string) {
	t.Helper()
	// A live codex session on the host makes the session row and its token
	// real; the recorded CLI attaches to the same session through the same
	// token.
	stream := subscribeVendorSessions(t, v.host, nil)
	defer closeQuietly(stream)
	root := t.TempDir()
	wsID := v.workspaceWithRoot(t, root)
	started, err := v.vendors.StartSession(t.Context(), connect.NewRequest(&vendorsv1.StartSessionRequest{
		CommandId: "record-codex-session", Vendor: vendorsv1.Vendor_VENDOR_CODEX, WorkspaceId: wsID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	awaitVendorState(t, stream, started.Msg.Session.SessionId, "ready")
	token := recordingToken(t, 0)
	bridge := vendor.Bridge{Binary: buildFugue(t), Socket: v.sockPath}
	spec := &vendor.StartSpec{Cwd: root, HostEnv: os.Environ(), SessionToken: redact.NewSecret(token), Bridge: bridge}
	argv, err := codex.BuildArgv(codexBin, spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = codex.SessionEnv(spec)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The codex app-server speaks only when spoken to: the handshake goes
	// out first, then the driver answers whatever arrives.
	if _, wErr := fmt.Fprint(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"fugue-recording","title":"fugue-recording","version":"0.1.0"}}}`+"\n"); wErr != nil {
		t.Fatal(wErr)
	}
	var lines []string
	turnLine := 0
	turnSent := false
	gateAnswers := 0
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		// Codex keeps its own approval policy (VR-04): the recording grants
		// the calls the prompt asks for. The FIRST tool-call gate is answered
		// with the measured persist-session meta, so a second gate for the
		// same tool must never arrive; any later gate gets a one-time accept.
		if requestID := approvalRequestID(line); requestID != "" {
			var result any = map[string]string{"decision": "accept"}
			if strings.Contains(line, "elicitation") {
				result = map[string]any{"action": "accept"}
				if gateAnswers == 0 {
					gateAnswers++
					result = map[string]any{
						"action": "accept",
						"_meta":  map[string]string{"persist": "session"},
					}
				}
			}
			decision, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": json.RawMessage(requestID),
				"result": result,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, wErr := fmt.Fprint(stdin, string(decision)+"\n"); wErr != nil {
				t.Fatal(wErr)
			}
		}
		if strings.Contains(line, `"id":1`) && strings.Contains(line, `"result"`) {
			if _, wErr := fmt.Fprint(stdin, `{"jsonrpc":"2.0","method":"initialized"}`+"\n"); wErr != nil {
				t.Fatal(wErr)
			}
			if _, wErr := fmt.Fprint(stdin, `{"jsonrpc":"2.0","id":2,"method":"thread/start","params":{"cwd":`+jsonString(root)+`}}`+"\n"); wErr != nil {
				t.Fatal(wErr)
			}
			continue
		}
		if !turnSent && strings.Contains(line, `"thread/started"`) {
			turnLine = len(lines) + 1
			turnSent = true
			threadID := extractJSON(line, "params", "thread", "id")
			turn, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 100, "method": "turn/start",
				"params": map[string]any{
					"threadId": json.RawMessage(threadID),
					"input":    []map[string]string{{"type": "text", "text": recordPrompt}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, wErr := fmt.Fprint(stdin, string(turn)+"\n"); wErr != nil {
				t.Fatal(wErr)
			}
			continue
		}
		if turnSent && strings.Contains(line, `"turn/completed"`) {
			break
		}
	}
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("the codex recording timed out after %d lines: last %s", len(lines), lastLine(lines))
	}
	if len(lines) == 0 {
		t.Fatalf("the codex recording captured nothing (close %v, wait %v)", closeErr, waitErr)
	}
	if !containsFugueToolCall(lines) {
		t.Fatalf("the codex recording has no fugue tool call: %d lines", len(lines))
	}
	writeCodexFixture(t, lines, root, turnLine)
}

// agyResponseID returns the id of a JSON-RPC response line, or "".
func agyResponseID(line string) string {
	var resp struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal([]byte(line), &resp) != nil || resp.Method != "" {
		return ""
	}
	return strings.Trim(string(resp.ID), `"`)
}

// jsonString encodes one string as a JSON string literal.
func jsonString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(raw)
}

func lastLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// approvalRequestID returns the id of a server request the vendor sent, or
// "" when the line is not one.
func approvalRequestID(line string) string {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal([]byte(line), &req) != nil {
		return ""
	}
	if req.Method == "" || len(req.ID) == 0 {
		return ""
	}
	if strings.Contains(req.Method, "requestApproval") ||
		strings.Contains(req.Method, "request_permission") ||
		strings.Contains(req.Method, "elicitation/request") {
		return string(req.ID)
	}
	return ""
}

// extractJSON digs a nested JSON value out of a recorded line as raw JSON.
func extractJSON(line string, keys ...string) string {
	current := json.RawMessage(line)
	for _, key := range keys {
		var obj map[string]json.RawMessage
		if json.Unmarshal(current, &obj) != nil {
			return `""`
		}
		current = obj[key]
	}
	return string(current)
}

func containsFugueToolCall(lines []string) bool {
	for _, line := range lines {
		if strings.Contains(line, "fugue_test_echo") {
			return true
		}
	}
	return false
}

func recordAgy(t *testing.T, v *vendorHost, agyBin string) {
	t.Helper()
	stream := subscribeVendorSessions(t, v.host, nil)
	defer closeQuietly(stream)
	root := t.TempDir()
	wsID := v.workspaceWithRoot(t, root)
	started, err := v.vendors.StartSession(t.Context(), connect.NewRequest(&vendorsv1.StartSessionRequest{
		CommandId: "record-agy-session", Vendor: vendorsv1.Vendor_VENDOR_AGY, WorkspaceId: wsID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	awaitVendorState(t, stream, started.Msg.Session.SessionId, "ready")
	token := recordingToken(t, 1)
	bridge := vendor.Bridge{Binary: buildFugue(t), Socket: v.sockPath}
	server := agy.McpServer{Name: agy.BridgeServerName, Command: bridge.Binary, Args: []string{"mcp-bridge", "--socket", bridge.Socket}}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, agyBin, "acp")
	cmd.Dir = root
	cmd.Env = agy.SessionEnv(&vendor.StartSpec{Cwd: root, HostEnv: os.Environ(), SessionToken: redact.NewSecret(token)})
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	newSession, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/new",
		"params": map[string]any{"cwd": root, "mcpServers": []agy.McpServer{server}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, wErr := fmt.Fprint(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}`+"\n"); wErr != nil {
		t.Fatal(wErr)
	}
	model := recordAgyModel(t)
	var lines []string
	promptSent := false
	promptLine := 0
	handshook := false
	acpSessionID := ""
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		// agy keeps its own permission flow (VR-05): the recording allows
		// the one call the prompt asks for. agy 1.5.10 rejects the flat
		// `{"outcome":"selected",...}` form ("cannot unmarshal string into
		// ... outcome of type AllowedOutcome") and needs the ACP-spec nested
		// `{"outcome":{"outcome":"selected","optionId":...}}`.
		if requestID := approvalRequestID(line); requestID != "" {
			decision, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": json.RawMessage(requestID),
				"result": map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": "allow"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, wErr := fmt.Fprint(stdin, string(decision)+"\n"); wErr != nil {
				t.Fatal(wErr)
			}
		}
		if !handshook && strings.Contains(line, `"protocolVersion":1`) {
			handshook = true
			if _, wErr := fmt.Fprint(stdin, string(newSession)+"\n"); wErr != nil {
				t.Fatal(wErr)
			}
			continue
		}
		if handshook && acpSessionID == "" && agyResponseID(line) == "2" {
			acpSessionID = extractJSON(line, "result", "sessionId")
			setModel, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 4, "method": agy.MethodSessionSetConfigOption,
				"params": map[string]string{"sessionId": strings.Trim(acpSessionID, `"`), "configId": "model", "value": model},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, wErr := fmt.Fprint(stdin, string(setModel)+"\n"); wErr != nil {
				t.Fatal(wErr)
			}
			continue
		}
		if acpSessionID != "" && !promptSent && agyResponseID(line) == "4" {
			if strings.Contains(line, `"error"`) {
				t.Fatalf("agy refused the model %q: %s", model, line)
			}
			prompt, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
				"params": map[string]any{
					"sessionId": json.RawMessage(acpSessionID),
					"prompt":    []map[string]string{{"type": "text", "text": recordPrompt}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, wErr := fmt.Fprint(stdin, string(prompt)+"\n"); wErr != nil {
				t.Fatal(wErr)
			}
			promptSent = true
			promptLine = len(lines) + 1
			continue
		}
		if promptSent && strings.Contains(line, `"stopReason":"end_turn"`) {
			break
		}
	}
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("the agy recording timed out after %d lines: last %s", len(lines), lastLine(lines))
	}
	if len(lines) == 0 {
		t.Fatalf("the agy recording captured nothing (close %v, wait %v)", closeErr, waitErr)
	}
	if !containsFugueToolCall(lines) || strings.Contains(strings.Join(lines, "\n"), "quota") {
		t.Fatalf("the agy recording has no fugue tool call or hit a quota error (model %s): %d lines", model, len(lines))
	}
	writeAgyFixture(t, lines, root, promptLine, agyBin)
}

// writeCodexFixture redacts and writes the recorded codex stream.
func writeCodexFixture(t *testing.T, lines []string, root string, turnLine int) {
	t.Helper()
	red := newRecorder()
	red.rule(root, "/home/REDACTED/codex-fixture")
	red.rule(filepath.Dir(root), "/home/REDACTED")
	home, _ := os.UserHomeDir()
	red.rule(home, "/home/REDACTED")
	red.ruleRE(`\(Mac OS [^)]*\)`, "(Linux; x86_64)")
	red.ruleRE(`[A-Za-z0-9-]+\.local`, "host.example")
	writeFixtureFiles(t, "../../internal/vendoradapter/codex/testdata/fixtures", "codex-0.167.0-session4-fugue-tool", red.applyAll(lines), turnLine)
}

func writeAgyFixture(t *testing.T, lines []string, root string, turnLine int, agyBin string) {
	t.Helper()
	red := newRecorder()
	red.uuidPrefix = "session-uuid-"
	red.callSeen = map[string]string{}
	red.rule(root, "/home/REDACTED/agy-fixture")
	red.rule(filepath.Dir(root), "/home/REDACTED")
	home, _ := os.UserHomeDir()
	red.rule(home, "/home/REDACTED")
	red.ruleRE(`[A-Za-z0-9-]+\.local`, "host.example")
	writeFixtureFiles(t, "../../internal/vendoradapter/agy/testdata/fixtures", "agy-"+agyVersion(t, agyBin)+"-session7-fugue-tool", red.applyAll(lines), turnLine)
}

// agyVersion reads the installed agy's version so the fixture name carries
// the CLI that was recorded.
func agyVersion(t *testing.T, agyBin string) string {
	t.Helper()
	out, err := exec.Command(agyBin, "--version").Output()
	if err != nil {
		t.Fatalf("agy --version: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		t.Fatal("agy --version printed nothing")
	}
	return fields[len(fields)-1]
}

// writeFixtureFiles writes the fixture and its turn sidecar.
func writeFixtureFiles(t *testing.T, dir, base string, lines []string, turnLine int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, base+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecar := fmt.Sprintf("{\n \"sends\": [\n  {\n   \"beforeLine\": %d,\n   \"turn\": \"turn-1\"\n  }\n ]\n}\n", turnLine)
	if err := os.WriteFile(filepath.Join(dir, base+".turns.json"), []byte(sidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d lines, turn at line %d)", base, len(lines), turnLine)
}

// recorder redacts a recorded stream: exact path replacements first, then
// every remaining UUID as uuid-NNNN in order of first appearance.
type recorder struct {
	replacements []struct{ from, to string }
	regexps      []struct{ re, to *regexp.Regexp }
	uuidSeq      int
	seen         map[string]string
	uuidPrefix   string
	callSeen     map[string]string
}

func newRecorder() *recorder {
	return &recorder{seen: map[string]string{}}
}

func (r *recorder) rule(from, to string) {
	r.replacements = append(r.replacements, struct{ from, to string }{from, to})
}

// ruleRE replaces every match of a pattern, for host shapes that vary run to
// run: the OS line of a user agent and the machine's hostname.
func (r *recorder) ruleRE(pattern, to string) {
	r.regexps = append(r.regexps, struct{ re, to *regexp.Regexp }{regexp.MustCompile(pattern), regexp.MustCompile(to)})
}

func (r *recorder) applyAll(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, r.apply(line))
	}
	return out
}

func (r *recorder) apply(line string) string {
	if r.callSeen != nil {
		line = regexp.MustCompile(`call_[0-9]+`).ReplaceAllStringFunc(line, func(m string) string {
			if name, ok := r.callSeen[m]; ok {
				return name
			}
			name := fmt.Sprintf("call-%04d", len(r.callSeen)+1)
			r.callSeen[m] = name
			return name
		})
	}
	for _, rule := range r.replacements {
		line = strings.ReplaceAll(line, rule.from, rule.to)
	}
	for _, rule := range r.regexps {
		line = rule.re.ReplaceAllLiteralString(line, rule.to.String())
	}
	return regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`).ReplaceAllStringFunc(line, func(m string) string {
		if name, ok := r.seen[m]; ok {
			return name
		}
		r.uuidSeq++
		prefix := r.uuidPrefix
		if prefix == "" {
			prefix = "uuid-"
		}
		name := fmt.Sprintf("%s%04d", prefix, r.uuidSeq)
		r.seen[m] = name
		return name
	})
}
