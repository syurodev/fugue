package agy

// VR-E03 and TR-E01 (agy): fixtures recorded from the real CLI 1.5.9 replay
// through the codec and normalizer to the golden transcript — verifying item
// kinds, revisions, and unsupported_event notices with no CLI in CI.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
)

type replayStep struct {
	Line          int                   `json:"line"`
	Op            string                `json:"op"`
	Item          map[string]any        `json:"item,omitempty"`
	Before        map[string]any        `json:"before,omitempty"`
	After         map[string]any        `json:"after,omitempty"`
	Init          *Init                 `json:"init,omitempty"`
	End           *TurnEnd              `json:"turn_end,omitempty"`
	ServerRequest *vendor.ServerRequest `json:"server_request,omitempty"`
}

type replaySummary struct {
	Partials       int `json:"partials"`
	Notices        int `json:"notices"`
	ServerRequests int `json:"server_requests"`
}

type goldenFile struct {
	Session string        `json:"session"`
	Vendor  string        `json:"vendor"`
	Summary replaySummary `json:"summary"`
	Steps   []replayStep  `json:"steps"`
}

type turnSends struct {
	Sends []struct {
		BeforeLine int    `json:"beforeLine"`
		Turn       string `json:"turn"`
	} `json:"sends"`
}

func renderItem(item *transcript.Item) map[string]any {
	return map[string]any{
		"id":       item.ID,
		"turnId":   item.TurnID,
		"kind":     item.Kind,
		"revision": item.Revision,
		"rawRef":   item.RawRef,
		"meta":     item.Metadata,
		"content":  item.Content,
	}
}

func replay(t *testing.T, fixture string) goldenFile {
	t.Helper()
	lines := readFixture(t, fixture)
	var sends turnSends
	sidecar := strings.TrimSuffix(fixture, ".jsonl") + ".turns.json"
	raw := readFile(t, filepath.Join("testdata", "fixtures", sidecar))
	if err := json.Unmarshal(raw, &sends); err != nil {
		t.Fatalf("decode turn sidecar: %v", err)
	}

	n := NewNormalizer("session-1")
	out := goldenFile{Session: "session-1", Vendor: "agy"}
	sendAt := 0
	for i, line := range lines {
		for sendAt < len(sends.Sends) && sends.Sends[sendAt].BeforeLine == i+1 {
			n.BeginTurn(sends.Sends[sendAt].Turn, "")
			sendAt++
		}
		obs, err := n.Normalize(line)
		if err != nil {
			t.Fatalf("fixture line %d did not normalize: %v", i+1, err)
		}
		if uErr := validateObservation(&obs); uErr != nil {
			t.Fatalf("fixture line %d produced invalid item: %v", i+1, uErr)
		}
		replayObservation(&out, &obs, i+1)
	}
	return out
}

func validateObservation(obs *Observation) error {
	for i := range obs.Records {
		if err := obs.Records[i].Validate(); err != nil {
			return fmt.Errorf("invalid recorded item: %w", err)
		}
	}
	for i := range obs.Updates {
		if err := transcript.ValidateUpdate(&obs.Updates[i].Before, &obs.Updates[i].After); err != nil {
			return fmt.Errorf("invalid item update: %w", err)
		}
	}
	return nil
}

func replayObservation(out *goldenFile, obs *Observation, line int) {
	if obs.Init != nil {
		out.Steps = append(out.Steps, replayStep{Line: line, Op: "init", Init: obs.Init})
	}
	if obs.TurnEnd != nil {
		out.Steps = append(out.Steps, replayStep{Line: line, Op: "turn_end", End: obs.TurnEnd})
	}
	for i := range obs.Records {
		item := &obs.Records[i]
		if item.Kind == transcript.Notice {
			out.Summary.Notices++
		}
		out.Steps = append(out.Steps, replayStep{Line: line, Op: "record", Item: renderItem(item)})
	}
	for i := range obs.Updates {
		out.Steps = append(out.Steps, replayStep{
			Line:   line,
			Op:     "update",
			Before: renderItem(&obs.Updates[i].Before),
			After:  renderItem(&obs.Updates[i].After),
		})
	}
	if obs.Partial != nil {
		out.Summary.Partials++
	}
	if obs.ServerRequest != nil {
		out.Summary.ServerRequests++
		out.Steps = append(out.Steps, replayStep{
			Line:          line,
			Op:            "server_request",
			ServerRequest: obs.ServerRequest,
		})
	}
}

func TestVRE03AgyFixturesReplayToGoldenTranscript(t *testing.T) {
	t.Parallel()
	runGoldenFixtureSuite(t)
}

func TestVRE03AgyRelayFixturesReplayToGoldenTranscript(t *testing.T) {
	t.Parallel()
	runGoldenFixtureSuite(t)
}

func runGoldenFixtureSuite(t *testing.T) {
	t.Helper()
	fixtures, err := filepath.Glob(filepath.Join("testdata", "fixtures", "agy-*.jsonl"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no agy fixtures found: %v", err)
	}
	for _, fixture := range fixtures {
		base := filepath.Base(fixture)
		t.Run(base, func(t *testing.T) {
			t.Parallel()
			got := replay(t, base)
			checkGolden(t, base, &got)
		})
	}
}

func checkGolden(t *testing.T, fixture string, got *goldenFile) {
	t.Helper()
	goldenPath := filepath.Join("testdata", "golden", strings.TrimSuffix(fixture, ".jsonl")+".json")
	want, readErr := os.ReadFile(filepath.Clean(goldenPath))
	if goldensRequested() {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o750); err != nil {
			t.Fatal(err)
		}
		encoded, mErr := json.MarshalIndent(got, "", " ")
		if mErr != nil {
			t.Fatal(mErr)
		}
		if wErr := os.WriteFile(goldenPath, append(encoded, '\n'), 0o600); wErr != nil {
			t.Fatal(wErr)
		}
		return
	}
	if readErr != nil {
		t.Fatalf("read golden file (run with -update): %v", readErr)
	}
	var wantGolden, gotGolden any
	if err := json.Unmarshal(want, &wantGolden); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encode replay: %v", err)
	}
	if err := json.Unmarshal(encoded, &gotGolden); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if diff := cmp.Diff(wantGolden, gotGolden); diff != "" {
		t.Errorf("replay diverged from golden (-golden +replay):\n%s", diff)
	}
}

func TestAgyUnknownUpdatesBecomeUnsupportedNoticesWithRawRef(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("session-1")
	n.BeginTurn("turn-1", "")

	lines := []string{
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"brand_new_vendor_event","payload":123}}}`,
		`{"jsonrpc":"2.0","method":"session/custom_notification","params":{"foo":"bar"}}`,
		`not valid json at all`,
	}
	for i, line := range lines {
		obs, err := n.Normalize([]byte(line))
		if err != nil {
			t.Fatalf("line %d failed unexpectedly: %v", i, err)
		}
		if len(obs.Records) != 1 {
			t.Fatalf("line %d records = %d, want 1 notice", i, len(obs.Records))
		}
		item := obs.Records[0]
		if item.Kind != transcript.Notice || item.RawRef == "" {
			t.Fatalf("line %d item = %+v, want Notice with non-empty RawRef", i, item)
		}
		meta, ok := item.Metadata.(transcript.NoticeMetadata)
		if !ok || meta.Code != "unsupported_event" {
			t.Fatalf("line %d meta = %#v, want unsupported_event", i, item.Metadata)
		}
	}
}

func TestAgyMetaIsNeverASafetyCondition(t *testing.T) {
	t.Parallel()
	withMeta := NewNormalizer("session-1")
	withoutMeta := NewNormalizer("session-1")
	withMeta.BeginTurn("turn-1", "")
	withoutMeta.BeginTurn("turn-1", "")

	// Permission request carrying _meta.antigravity claiming auto-approve/safe must still produce identical ServerRequest relay behavior.
	permWithMeta := `{"jsonrpc":"2.0","id":"1","method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"sessionUpdate":"tool_call","toolCallId":"c1","name":"run_command","title":"echo hi"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}],"_meta":{"antigravity":{"autoApprove":true,"trusted":true,"tier":"enterprise"}}}}`
	permNoMeta := `{"jsonrpc":"2.0","id":"1","method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"sessionUpdate":"tool_call","toolCallId":"c1","name":"run_command","title":"echo hi"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}`

	obsWith, err := withMeta.Normalize([]byte(permWithMeta))
	if err != nil {
		t.Fatal(err)
	}
	obsNo, err := withoutMeta.Normalize([]byte(permNoMeta))
	if err != nil {
		t.Fatal(err)
	}
	if obsWith.ServerRequest == nil || obsNo.ServerRequest == nil {
		t.Fatalf("expected ServerRequest on both, got with=%+v without=%+v", obsWith, obsNo)
	}
	if diff := cmp.Diff(obsNo.ServerRequest, obsWith.ServerRequest); diff != "" {
		t.Fatalf("_meta altered safety/routing behavior (-without +with):\n%s", diff)
	}
}

func TestAgyFrameOverTheLimitFailsTyped(t *testing.T) {
	t.Parallel()
	c := NewCodec()
	oversized := make([]byte, process.MaxFrameSize+1)
	for i := range oversized {
		oversized[i] = 'x'
	}
	if _, err := c.DecodeFrame(oversized); errcode.CodeOf(err) != errcode.FrameTooLarge {
		t.Fatalf("DecodeFrame err = %v, want frame_too_large", err)
	}
}

func TestAgyPlanAndDiffNormalization(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("session-1")
	n.BeginTurn("turn-1", "")

	planLine := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[{"content":"Inspect","status":"completed"},{"content":"Edit","status":"in_progress"}]}}}`
	obs, err := n.Normalize([]byte(planLine))
	if err != nil || len(obs.Records) != 1 || obs.Records[0].Kind != transcript.Plan {
		t.Fatalf("plan obs = %+v, err = %v", obs, err)
	}
	diffLine := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"c2","name":"write_file","status":"completed","content":[{"type":"diff","path":"/home/REDACTED/a.txt","oldText":null,"newText":"hello"}]}}}`
	diffObs, err := n.Normalize([]byte(diffLine))
	if err != nil || len(diffObs.Records) != 2 || diffObs.Records[1].Kind != transcript.FileChange {
		t.Fatalf("diff obs = %+v, err = %v", diffObs, err)
	}
}

func TestAgySlashListHidesTerminalCommands(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("session-1")
	fixtureLines := readFixture(t, "agy-1.5.9-session1.jsonl")
	for _, line := range fixtureLines[:6] {
		if _, err := n.Normalize(line); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"goal",
		"schedule",
		"browser",
		"plan",
		"grill-me",
		"teamwork",
		"learn",
		"boost",
		"agy-customizations",
		"antigravity-guide",
		"run-workflow",
		"swarm",
	}
	if diff := cmp.Diff(want, n.SlashCommands()); diff != "" {
		t.Fatalf("SlashCommands() after real fixture available_commands_update (-want +got):\n%s", diff)
	}
	if IsTerminalSlashCommand("goal") || !IsTerminalSlashCommand("compact") || !IsTerminalSlashCommand("/clear") {
		t.Fatal("unexpected terminal slash command classification")
	}
	// Updating available_commands_update replaces the session command list while hiding terminal commands.
	obs, err := n.Normalize([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-uuid-0001","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"plan"},{"name":"compact"},{"name":"clear"},{"name":"custom-skill"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	wantUpdated := []string{"plan", "custom-skill"}
	if obs.Init == nil || !cmp.Equal(obs.Init.SlashCommands, wantUpdated) || !cmp.Equal(n.SlashCommands(), wantUpdated) {
		t.Fatalf("updated SlashCommands = %v (init=%+v), want %v", n.SlashCommands(), obs.Init, wantUpdated)
	}
	assertUntrustedSlashNamesAreDropped(t)
}

// assertUntrustedSlashNamesAreDropped checks the filter on untrusted vendor
// names: fugue namespace, empty, whitespace, control characters, over 64
// bytes, and at most 128 commands.
func assertUntrustedSlashNamesAreDropped(t *testing.T) {
	t.Helper()
	bad := []AvailableCommand{
		{Name: "fugue:status"},
		{Name: "/fugue:help"},
		{Name: "Fugue:status"},
		{Name: "/FUGUE:help"},
		{Name: "FuGuE:mixed"},
		{Name: ""},
		{Name: "  "},
		{Name: "/"},
		{Name: "two words"},
		{Name: "tab\tname"},
		{Name: "nl\nname"},
		{Name: "bell\x07"},
		{Name: "del\x7f"},
		{Name: "rtl\u202ename"},
		{Name: "zw\u200bname"},
		{Name: strings.Repeat("a", 65)},
		{Name: "ok"},
		{Name: strings.Repeat("b", 64)},
	}
	if diff := cmp.Diff([]string{"ok", strings.Repeat("b", 64)}, hideTerminalCommands(bad)); diff != "" {
		t.Fatalf("hideTerminalCommands(untrusted) (-want +got):\n%s", diff)
	}
	many := make([]AvailableCommand, 0, maxSlashCommands+10)
	for i := range maxSlashCommands + 10 {
		many = append(many, AvailableCommand{Name: fmt.Sprintf("cmd-%d", i)})
	}
	if got := hideTerminalCommands(many); len(got) != 128 || got[127] != "cmd-127" {
		t.Fatalf("hideTerminalCommands(%d) kept %d (last %q), want the first 128", len(many), len(got), got[len(got)-1])
	}
}

func TestAgyApprovalIDWindowsStayBounded(t *testing.T) {
	t.Parallel()
	s := &Session{}
	for i := range maxApprovalIDs + 1 {
		id := fmt.Sprintf("req-%d", i)
		if !s.registerServerRequest(&Frame{ID: json.RawMessage(`"` + id + `"`), CanonicalID: id, Method: MethodSessionRequestPermission}) {
			t.Fatalf("register %s = false, want true", id)
		}
		delete(s.pendingApprovals, id)
		s.resolvedApprovals.add(id)
	}
	if len(s.seenApprovals.set) != maxApprovalIDs || len(s.resolvedApprovals.set) != maxApprovalIDs {
		t.Fatalf("seen=%d resolved=%d, want both bounded at %d", len(s.seenApprovals.set), len(s.resolvedApprovals.set), maxApprovalIDs)
	}
	last := fmt.Sprintf("req-%d", maxApprovalIDs)
	if s.seenApprovals.has("req-0") || !s.seenApprovals.has(last) || !s.resolvedApprovals.has(last) {
		t.Fatal("window must evict the oldest ID and keep the newest")
	}
	if s.registerServerRequest(&Frame{ID: json.RawMessage(`"` + last + `"`), CanonicalID: last, Method: MethodSessionRequestPermission}) {
		t.Fatalf("re-register %s inside the window = true, want answered once", last)
	}
}

func TestFugueNamespaceNeverReachesAgy(t *testing.T) {
	t.Parallel()
	rpcLog := filepath.Join(t.TempDir(), "fugue-ns-rpcs.jsonl")
	sess, _ := startFakeSession(t, fakeRecordRPCsConfigDir+":"+rpcLog, "")
	for _, cmd := range []string{"/fugue:status", "  /fugue:help arg", "/fugue:compact"} {
		if _, err := sess.Send(t.Context(), vendor.Input{Text: cmd}); errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Fatalf("Send(%q) err = %v, want invalid_argument", cmd, err)
		}
	}
	if _, err := sess.Close(t.Context(), vendor.CloseHostStop); err != nil {
		t.Fatal(err)
	}
	rawRPCs := string(readFile(t, rpcLog))
	if strings.Contains(rawRPCs, "fugue:") || strings.Contains(rawRPCs, MethodSessionPrompt) {
		t.Fatalf("/fugue: command leaked to agy stdin:\n%s", rawRPCs)
	}
}

func TestAgyCodecAndNormalizerHelpers(t *testing.T) {
	t.Parallel()
	if got := (&RPCError{Code: -1, Message: "boom"}).Error(); !strings.Contains(got, "boom") {
		t.Fatalf("RPCError.Error = %q", got)
	}
	c := NewCodec()
	n := NewNormalizerWithCodec("session-1", c)
	if _, err := c.EncodeRequest(1, MethodInitialize, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Normalize([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true},"agentInfo":{"name":"Antigravity ACP Server","version":"1.5.9"}}}`)); err != nil {
		t.Fatal(err)
	}
	if !n.HandshakeCaps().LoadSession {
		t.Fatal("expected HandshakeCaps().LoadSession true")
	}
	n.BeginTurn("t-roll", "p-roll")
	n.RollbackTurn()
	assertPermissionHelpers(t, n)
}

func assertPermissionHelpers(t *testing.T, n *Normalizer) {
	t.Helper()
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":"1","method":"session/request_permission","params":{}}`,
		`{"jsonrpc":"2.0","id":"1","method":"session/request_permission","params":{"sessionId":"s1","toolCall":{},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}`,
		`{"jsonrpc":"2.0","id":"1","method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"c1"},"options":[{"optionId":"a","name":"A","kind":"bad"}]}}`,
		`{"jsonrpc":"2.0","id":"1","method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"c1","name":"run"},"options":[{"optionId":"a","name":"A","kind":"allow_always"},{"optionId":"d","name":"D","kind":"reject_always"}]}}`,
		`{"jsonrpc":"2.0","id":"1","method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"c1"},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"available_commands_update"}}}`,
	} {
		if _, err := n.Normalize([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	longUTF8 := strings.Repeat("x", maxSummaryBytes-1) + "é" + strings.Repeat("y", 32)
	if got := boundSummary(longUTF8); len(got) > maxSummaryBytes {
		t.Fatalf("boundSummary len = %d, want <= %d", len(got), maxSummaryBytes)
	}
}

func readFixture(t *testing.T, name string) [][]byte {
	t.Helper()
	raw := readFile(t, filepath.Join("testdata", "fixtures", name))
	var lines [][]byte
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, []byte(line))
		}
	}
	return lines
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
