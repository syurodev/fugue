package app

// VR-E14 / git-guard GG-E05: the end-of-turn scan of the lines a turn added.
// The planted values below match detector rules but are obviously fake. No
// assertion message prints a value: a failure must never leak one.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/snapshot"
	"github.com/syurodev/fugue/internal/transcript"
)

const (
	// fakeGitHubToken matches the github-token rule: ghp_ plus 36 characters.
	fakeGitHubToken = "ghp_" + "FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"
	// fakeGitHubServerToken is a second, different value of the same rule.
	fakeGitHubServerToken = "ghs_" + "FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"
	// fakeMiddle is a fragment from the middle of the values, which the masked
	// form (four leading and two trailing characters) must never contain.
	fakeMiddle = "FAKEFAKEFAKEFAKE"
)

func (h *turnDiffHarness) notices() []TranscriptEntry {
	h.t.Helper()
	entries, err := h.f.service.TranscriptPage(h.t.Context(), TranscriptPageParams{Session: testActorSessionID, Limit: 500})
	if err != nil {
		h.t.Fatal(err)
	}
	var out []TranscriptEntry
	for i := range entries {
		if entries[i].Kind == transcript.Notice && entries[i].Metadata.Code == transcript.NoticePossibleSecret {
			out = append(out, entries[i])
		}
	}
	return out
}

func (h *turnDiffHarness) noticeText(entry *TranscriptEntry) string {
	h.t.Helper()
	detail, err := h.f.service.TranscriptItem(h.t.Context(), testActorSessionID, entry.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	var content transcript.NoticeContent
	if err := json.Unmarshal(detail.Content, &content); err != nil {
		h.t.Fatal(err)
	}
	return content.Text
}

// requireAbsentEverywhere checks the places a value could leak: every byte of
// the sqlite database (main file, journal and shared memory), the captured
// logs, and the items as a client receives them.
func (h *turnDiffHarness) requireAbsentEverywhere(logs *vaultLogBuffer, values ...string) {
	h.t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if containsAny(readTestFileOrEmpty(h.t, h.f.dbPath+suffix), values) {
			h.t.Fatalf("a planted value is present in the database file%s", suffix)
		}
	}
	if containsAny([]byte(logs.String()), values) {
		h.t.Fatal("a planted value is present in the captured logs")
	}
	entries, err := h.f.service.TranscriptPage(h.t.Context(), TranscriptPageParams{Session: testActorSessionID, Limit: 500})
	if err != nil {
		h.t.Fatal(err)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		h.t.Fatal(err)
	}
	if containsAny(encoded, values) {
		h.t.Fatal("a planted value is present in an item as a client receives it")
	}
}

// containsAny reports whether raw holds a value or the fragment from the middle
// of the planted values, which even the masked form must not contain.
func containsAny(raw []byte, values []string) bool {
	if bytes.Contains(raw, []byte(fakeMiddle)) {
		return true
	}
	for _, value := range values {
		if bytes.Contains(raw, []byte(value)) {
			return true
		}
	}
	return false
}

// readTestFileOrEmpty reads a file through a root, or returns nothing when it
// does not exist (a sqlite sidecar that was never created).
func readTestFileOrEmpty(t *testing.T, path string) []byte {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	raw, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		return nil
	}
	return raw
}

func scanLogger() (*slog.Logger, *vaultLogBuffer) {
	logs := &vaultLogBuffer{}
	return slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), logs
}

// TestVRE14GGE05PlantedSecretInAddedLinesRaisesMaskedNotice: a secret in a line
// the turn added yields one notice(possible_secret). The metadata holds counts
// only; the sealed text holds the rule, path, line and the masked form; and the
// value is nowhere in the database, the logs or the metadata.
func TestVRE14GGE05PlantedSecretInAddedLinesRaisesMaskedNotice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n")
	logger, logs := scanLogger()
	h := newTurnDiffHarnessWith(t, dir, realTurnSnapshots(t), &VendorSessionDeps{Logger: logger})
	h.runTurn(func() {
		writeTestFile(t, filepath.Join(dir, "config.go"), "package main\n\nvar token = \""+fakeGitHubToken+"\"\n")
	})

	notices := h.notices()
	if len(notices) != 1 {
		t.Fatalf("possible_secret notices = %d, want 1", len(notices))
	}
	meta := notices[0].Metadata
	if meta.Level != "warning" || meta.FindingCount != 1 || meta.FindingFiles != 1 || meta.ScanIncomplete {
		t.Fatalf("notice metadata = %+v, want warning, 1 finding in 1 file, complete", meta)
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "config.go") {
		t.Fatal("a path reached the notice metadata")
	}
	text := h.noticeText(&notices[0])
	for _, want := range []string{"github-token", "config.go:3", "ghp_…KE"} {
		if !strings.Contains(text, want) {
			t.Fatalf("sealed notice text lacks %q", want)
		}
	}
	h.requireAbsentEverywhere(logs, fakeGitHubToken)
}

// TestVRE14GGE05RemovedAndUnchangedSecretsDoNotTrigger: only added lines count.
// A secret the turn deletes, or one that stays in a file the turn edits
// elsewhere, raises nothing.
func TestVRE14GGE05RemovedAndUnchangedSecretsDoNotTrigger(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "old.env"), "TOKEN="+fakeGitHubToken+"\n")
	writeTestFile(t, filepath.Join(dir, "stays.go"), "var token = \""+fakeGitHubServerToken+"\"\n")
	logger, logs := scanLogger()
	h := newTurnDiffHarnessWith(t, dir, realTurnSnapshots(t), &VendorSessionDeps{Logger: logger})
	h.runTurn(func() {
		if err := os.Remove(filepath.Join(dir, "old.env")); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(dir, "stays.go"), "var token = \""+fakeGitHubServerToken+"\"\nvar n = 1\n")
	})
	if got := h.notices(); len(got) != 0 {
		t.Fatalf("possible_secret notices = %d, want 0", len(got))
	}
	h.requireAbsentEverywhere(logs, fakeGitHubToken, fakeGitHubServerToken)
}

// TestVRE14GGE05NoHitRaisesNoNotice: ordinary added lines raise no notice of any
// kind, and the turn diff is unaffected.
func TestVRE14GGE05NoHitRaisesNoNotice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTurnDiffHarness(t, dir, realTurnSnapshots(t))
	h.runTurn(func() {
		writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	})
	entries, err := h.f.service.TranscriptPage(t.Context(), TranscriptPageParams{Session: testActorSessionID, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for i := range entries {
		if entries[i].Kind == transcript.Notice {
			t.Fatal("a notice was raised for a turn without a hit")
		}
	}
	if len(h.turnDiffs()) != 1 {
		t.Fatal("the turn_diff item is missing")
	}
}

// TestVRE14GGE05ShortValueShowsRuleOnly: a value under twelve characters shows
// no fragment at all (content-safety §3.8).
func TestVRE14GGE05ShortValueShowsRuleOnly(t *testing.T) {
	t.Parallel()
	got := maskValue("assigned", "hunter2hunt")
	if got != "" {
		t.Fatal("a value under twelve characters must show no fragment")
	}
	if got := maskValue("private-key", strings.Repeat("k", 40)); got != "" {
		t.Fatal("a private key must show no fragment")
	}
	if got := maskValue("github-token", fakeGitHubToken); got != "ghp_…KE" {
		t.Fatal("a long value must show four leading and two trailing characters")
	}
	if got := maskValue("assigned", "ключ-секрет-значение"); got != "ключ…ие" {
		t.Fatal("masking must cut on characters, not bytes")
	}
}

// TestVRE14GGE05AboveTheScanCapScansTheHeadAndSaysIncomplete: past the cap the
// head is scanned, a secret beyond it is not, and the notice records
// scan_incomplete.
func TestVRE14GGE05AboveTheScanCapScansTheHeadAndSaysIncomplete(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, logs := scanLogger()
	const capBytes = 2048
	h := newTurnDiffHarnessWith(t, dir, realTurnSnapshots(t), &VendorSessionDeps{Logger: logger, TurnScanBytes: capBytes})
	var b strings.Builder
	b.WriteString("var head = \"" + fakeGitHubToken + "\"\n")
	b.WriteString(strings.Repeat("filler line\n", 300))
	b.WriteString("var tail = \"" + fakeGitHubServerToken + "\"\n")
	h.runTurn(func() { writeTestFile(t, filepath.Join(dir, "big.go"), b.String()) })

	notices := h.notices()
	if len(notices) != 1 {
		t.Fatalf("possible_secret notices = %d, want 1", len(notices))
	}
	meta := notices[0].Metadata
	if meta.FindingCount != 1 || !meta.ScanIncomplete {
		t.Fatalf("notice metadata = %+v, want 1 finding and scan_incomplete", meta)
	}
	text := h.noticeText(&notices[0])
	if !strings.Contains(text, "scan_incomplete") || !strings.Contains(text, "ghp_") || strings.Contains(text, "ghs_") {
		t.Fatal("sealed text must list the head hit only and say scan_incomplete")
	}
	h.requireAbsentEverywhere(logs, fakeGitHubToken, fakeGitHubServerToken)
}

// scanBreaksAfterHits runs the real scan, then fails: the hits found before the
// failure must still be reported, marked incomplete.
type scanBreaksAfterHits struct{ *engineTurnSnapshots }

var errScanBroke = errcode.New(errcode.Internal, "the scan broke after reading")

func (s scanBreaksAfterHits) AddedLines(ctx context.Context, snap TurnSnapshot, changes *TurnChanges, maxBytes int64, visit func(*snapshot.AddedLine)) (snapshot.AddedScan, error) {
	scan, err := s.engineTurnSnapshots.AddedLines(ctx, snap, changes, maxBytes, visit)
	if err != nil {
		return scan, err
	}
	return scan, errScanBroke
}

// TestVRE14GGE05ScanFailureKeepsHitsAndTheTurnCompletes: an error in the scan
// never fails the turn; what was found is reported as incomplete, and the error
// text carries no value.
func TestVRE14GGE05ScanFailureKeepsHitsAndTheTurnCompletes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, logs := scanLogger()
	h := newTurnDiffHarnessWith(t, dir, scanBreaksAfterHits{realTurnSnapshots(t)}, &VendorSessionDeps{Logger: logger})
	h.runTurn(func() {
		writeTestFile(t, filepath.Join(dir, "a.go"), "var t = \""+fakeGitHubToken+"\"\n")
	})
	notices := h.notices()
	if len(notices) != 1 || !notices[0].Metadata.ScanIncomplete || notices[0].Metadata.FindingCount != 1 {
		t.Fatal("a failed scan must still report its hit, marked incomplete")
	}
	if !strings.Contains(logs.String(), "the turn secret scan did not finish") {
		t.Fatal("the scan failure was not logged")
	}
	h.requireAbsentEverywhere(logs, fakeGitHubToken)
}
