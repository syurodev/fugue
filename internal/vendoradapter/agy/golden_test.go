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
)

type replayStep struct {
	Line   int            `json:"line"`
	Op     string         `json:"op"`
	Item   map[string]any `json:"item,omitempty"`
	Before map[string]any `json:"before,omitempty"`
	After  map[string]any `json:"after,omitempty"`
	Init   *Init          `json:"init,omitempty"`
	End    *TurnEnd       `json:"turn_end,omitempty"`
}

type replaySummary struct {
	Partials int `json:"partials"`
	Notices  int `json:"notices"`
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
}

func TestVRE03AgyFixturesReplayToGoldenTranscript(t *testing.T) {
	t.Parallel()
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
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"goal"}]}}}`,
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

	// Permission request carrying _meta.antigravity claiming auto-approve/safe must still produce identical error/notice behavior.
	permWithMeta := `{"jsonrpc":"2.0","id":"1","method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"sessionUpdate":"tool_call","toolCallId":"c1","name":"run_command"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}],"_meta":{"antigravity":{"autoApprove":true,"trusted":true,"tier":"enterprise"}}}}`
	permNoMeta := `{"jsonrpc":"2.0","id":"1","method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"sessionUpdate":"tool_call","toolCallId":"c1","name":"run_command"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}`

	obsWith, err := withMeta.Normalize([]byte(permWithMeta))
	if err != nil {
		t.Fatal(err)
	}
	obsNo, err := withoutMeta.Normalize([]byte(permNoMeta))
	if err != nil {
		t.Fatal(err)
	}
	if len(obsWith.Records) != 1 || len(obsNo.Records) != 1 {
		t.Fatalf("records = %d vs %d, want 1 each", len(obsWith.Records), len(obsNo.Records))
	}
	if obsWith.Records[0].Kind != transcript.Notice || obsWith.Records[0].RawRef != obsNo.Records[0].RawRef || obsWith.ServerRequest != nil {
		t.Fatalf("_meta altered safety/routing behavior: with=%+v without=%+v", obsWith, obsNo)
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
