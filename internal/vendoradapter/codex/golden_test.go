package codex

// VR-E02 and TR-E01 (Codex): fixtures recorded from the real CLI 0.167.0 replay
// through the codec and normalizer to the golden transcript — the same item
// sequence with the same revisions — with no account and no CLI in CI.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/transcript"
)

type replayStep struct {
	Line          int            `json:"line"`
	Op            string         `json:"op"`
	Item          map[string]any `json:"item,omitempty"`
	Before        map[string]any `json:"before,omitempty"`
	After         map[string]any `json:"after,omitempty"`
	Init          *Init          `json:"init,omitempty"`
	TurnEnd       *TurnEnd       `json:"turn_end,omitempty"`
	Quota         *RateLimit     `json:"quota,omitempty"`
	ServerRequest *ServerRequest `json:"server_request,omitempty"`
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

func replay(t *testing.T, fixture string) goldenFile {
	t.Helper()
	lines := readFixture(t, fixture)
	var sends turnSends
	sidecar := strings.TrimSuffix(fixture, ".jsonl") + ".turns.json"
	raw := readFile(t, filepath.Join("testdata", "fixtures", sidecar))
	if err := json.Unmarshal(raw, &sends); err != nil {
		t.Fatalf("decode the turn sidecar: %v", err)
	}

	n := NewNormalizer("session-1")
	out := goldenFile{Session: "session-1", Vendor: "codex"}
	sendAt := 0
	for i, line := range lines {
		for sendAt < len(sends.Sends) && sends.Sends[sendAt].BeforeLine == i+1 {
			n.BeginTurn(sends.Sends[sendAt].Turn)
			sendAt++
		}
		obs, err := n.Normalize(line)
		if err != nil {
			t.Fatalf("fixture line %d did not normalize: %v", i+1, err)
		}
		if uErr := validateObservation(&obs); uErr != nil {
			t.Fatalf("fixture line %d produced an invalid item: %v", i+1, uErr)
		}
		replayObservation(&out, &obs, i+1)
	}
	return out
}

func validateObservation(obs *Observation) error {
	for i := range obs.Records {
		if err := obs.Records[i].Validate(); err != nil {
			return fmt.Errorf("a recorded item is invalid: %w", err)
		}
	}
	for i := range obs.Updates {
		if err := transcript.ValidateUpdate(&obs.Updates[i].Before, &obs.Updates[i].After); err != nil {
			return fmt.Errorf("an update is invalid: %w", err)
		}
	}
	return nil
}

func replayObservation(out *goldenFile, obs *Observation, line int) {
	if obs.Init != nil {
		out.Steps = append(out.Steps, replayStep{Line: line, Op: "init", Init: obs.Init})
	}
	if obs.Quota != nil {
		out.Steps = append(out.Steps, replayStep{Line: line, Op: "quota", Quota: obs.Quota})
	}
	if obs.ServerRequest != nil {
		out.Summary.ServerRequests++
		out.Steps = append(out.Steps, replayStep{Line: line, Op: "server_request", ServerRequest: obs.ServerRequest})
	}
	if obs.TurnEnd != nil {
		out.Steps = append(out.Steps, replayStep{Line: line, Op: "turn_end", TurnEnd: obs.TurnEnd})
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

func TestVRE02CodexFixturesReplayToGoldenTranscript(t *testing.T) {
	t.Parallel()
	fixtures, err := filepath.Glob(filepath.Join("testdata", "fixtures", "codex-*.jsonl"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no codex fixtures found: %v", err)
	}
	for _, fixture := range fixtures {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			t.Parallel()
			got := replay(t, filepath.Base(fixture))
			checkGolden(t, filepath.Base(fixture), &got)
			if got.Summary.Notices == 0 {
				t.Fatal("the replay produced no unsupported-event notices, so real unknown events were dropped (VR-E08)")
			}
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
		t.Fatalf("read the golden file (run with -update to create it): %v", readErr)
	}
	var wantGolden, gotGolden any
	if err := json.Unmarshal(want, &wantGolden); err != nil {
		t.Fatalf("decode the golden file: %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encode the replay: %v", err)
	}
	if err := json.Unmarshal(encoded, &gotGolden); err != nil {
		t.Fatalf("decode the replay: %v", err)
	}
	if diff := cmp.Diff(wantGolden, gotGolden); diff != "" {
		t.Errorf("the replay diverged from the golden transcript (-golden +replay):\n%s", diff)
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
