package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
)

const registryFixture = `# Verification Registry

## Not in stage A

| ID | Reason |
| --- | --- |
| ` + "`A-E04`" + ` | Gate B |

## Registry

| ID | Source | Gate | Tier | Evidence | Status |
| --- | --- | --- | --- | --- | --- |
| ` + "`VR-E01`" + ` | ` + "`vendor-runtime §15`" + ` | ` + "`release`" + ` | ` + "`pr`" + ` | ` + "`TestVRE01`" + ` | ` + "`Done`" + ` |
| ` + "`VR-E04`" + ` | ` + "`vendor-runtime §15`" + ` | ` + "`release`" + ` | ` + "`live`" + ` | RECORD | ` + "`Done`" + ` |
| ` + "`A-S01`" + ` | ` + "`activation §6`" + ` | ` + "`stable`" + ` | ` + "`live`" + ` | later | ` + "`Not started`" + ` |
`

func TestParseRegistryKeepsOnlyTheRegistryTable(t *testing.T) {
	t.Parallel()
	rows, err := parseRegistry(registryFixture)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID+"/"+r.Gate+"/"+r.Tier+"/"+r.Status)
	}
	want := []string{"VR-E01/release/pr/Done", "VR-E04/release/live/Done", "A-S01/stable/live/Not started"}
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
}

func TestParseRegistryRejectsMissingAndMalformedTables(t *testing.T) {
	t.Parallel()
	for name, md := range map[string]string{
		"no table":  "# nothing\n",
		"short row": registryHeader + "\n| --- |\n| `X-E01` | only |\n",
	} {
		if _, err := parseRegistry(md); !errors.Is(err, ErrRelease) {
			t.Errorf("%s: err = %v, want ErrRelease", name, err)
		}
	}
}

// releaseRepo is a git repository with a covered file and the registry. It
// returns the directory and the commit hash that holds the covered file.
type releaseRepo struct {
	dir   string
	first string
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)
	out, err := capture(t.Context(), dir, "git", full...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newReleaseRepo(t *testing.T) *releaseRepo {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "internal/vendor/a.go", "package vendor\n")
	writeFile(t, dir, "internal/storage/migrations/0001_initial.sql", "select 1;\n")
	writeFile(t, dir, "internal/storage/migrations/0007_later.sql", "select 1;\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "code")
	return &releaseRepo{dir: dir, first: gitIn(t, dir, "rev-parse", "HEAD")}
}

func recordJSON(revision, result string) string {
	return fmt.Sprintf(`{
  "id": "VR-E04", "check": "approval-relay", "revision": %q, "fugue_version": "dev",
  "schema_version": 7, "protocol_version": "v1", "os": "linux", "arch": "amd64",
  "started_at": "2026-11-23T10:00:00Z", "finished_at": "2026-11-23T10:30:00Z",
  "result": %q, "measurements": {}, "covers": ["internal/vendor/**"], "notes": "ok"
}`, revision, result)
}

const (
	flagDir    = "-dir"
	flagID     = "-id"
	flagNotes  = "-notes"
	idVRE04    = "VR-E04"
	recordPath = "quality/records/VR-E04/2026-11-23-linux.json"
)

// commitRegistry commits the registry (naming the record) and the record body.
func (r *releaseRepo) commitRegistry(t *testing.T, record string) string {
	t.Helper()
	writeFile(t, r.dir, registryFile, strings.ReplaceAll(registryFixture, "RECORD", "`"+recordPath+"`"))
	writeFile(t, r.dir, recordPath, record)
	gitIn(t, r.dir, "add", "-A")
	gitIn(t, r.dir, "commit", "-q", "-m", "evidence")
	return gitIn(t, r.dir, "rev-parse", "HEAD")
}

func TestCheckReleaseAcceptsDoneRowsWithAValidRecord(t *testing.T) {
	t.Parallel()
	r := newReleaseRepo(t)
	rc := r.commitRegistry(t, recordJSON(r.first, resultPass))
	report, err := checkRelease(t.Context(), r.dir, rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Problems) != 0 {
		t.Fatalf("problems = %v, want none", report.Problems)
	}
	if len(report.Rows) != 2 || len(report.Live) != 1 {
		t.Errorf("rows = %d, live = %d, want 2 and 1 (the stable row is not a release row)", len(report.Rows), len(report.Live))
	}
}

// blocker is a way for the release gate to be unmet: prepare builds the repo
// state and returns the RC; want is the problem the gate must list.
type blocker struct {
	prepare func(t *testing.T, r *releaseRepo) string
	want    string
}

func blockers() map[string]blocker {
	all := recordBlockers()
	maps.Copy(all, fileBlockers())
	return all
}

// recordBlockers are the cases where a record exists but does not hold.
func recordBlockers() map[string]blocker {
	return map[string]blocker{
		"record failed": {
			prepare: func(t *testing.T, r *releaseRepo) string {
				t.Helper()
				return r.commitRegistry(t, recordJSON(r.first, resultFail))
			},
			want: `VR-E04: record ` + recordPath + ` has result "fail", want "pass"`,
		},
		"covered code changed after the record": {
			prepare: func(t *testing.T, r *releaseRepo) string {
				t.Helper()
				writeFile(t, r.dir, "internal/vendor/a.go", "package vendor\n\nvar changed = 1\n")
				gitIn(t, r.dir, "commit", "-q", "-am", "change covered code")
				return r.commitRegistry(t, recordJSON(r.first, resultPass))
			},
			want: "is stale: 1 covered file(s) changed since",
		},
		"revision is not an ancestor": {
			prepare: func(t *testing.T, r *releaseRepo) string {
				t.Helper()
				gitIn(t, r.dir, "checkout", "-q", "-b", "side")
				writeFile(t, r.dir, "side.txt", "x\n")
				gitIn(t, r.dir, "add", "-A")
				gitIn(t, r.dir, "commit", "-q", "-m", "side")
				side := gitIn(t, r.dir, "rev-parse", "HEAD")
				gitIn(t, r.dir, "checkout", "-q", "main")
				return r.commitRegistry(t, recordJSON(side, resultPass))
			},
			want: "is not an ancestor of the RC",
		},
		"revision is unknown": {
			prepare: func(t *testing.T, r *releaseRepo) string {
				t.Helper()
				return r.commitRegistry(t, recordJSON(strings.Repeat("a", 40), resultPass))
			},
			want: "is not a commit of this repository",
		},
	}
}

// fileBlockers are the cases of a missing record, a bad record or an unmet row.
func fileBlockers() map[string]blocker {
	return map[string]blocker{
		"record has a field missing": {
			prepare: func(t *testing.T, r *releaseRepo) string {
				t.Helper()
				return r.commitRegistry(t, strings.Replace(recordJSON(r.first, resultPass), `"covers": ["internal/vendor/**"],`, "", 1))
			},
			want: "covers is missing or empty",
		},
		"record holds a secret": {
			prepare: func(t *testing.T, r *releaseRepo) string {
				t.Helper()
				return r.commitRegistry(t, strings.Replace(recordJSON(r.first, resultPass), `"notes": "ok"`, `"notes": "login owner@example.com"`, 1))
			},
			want: "contains an email address",
		},
		"record file is missing": {
			prepare: func(t *testing.T, r *releaseRepo) string {
				t.Helper()
				r.commitRegistry(t, recordJSON(r.first, resultPass))
				gitIn(t, r.dir, "rm", "-q", recordPath)
				gitIn(t, r.dir, "commit", "-q", "-m", "drop record")
				return gitIn(t, r.dir, "rev-parse", "HEAD")
			},
			want: "cannot be read",
		},
		"registry row is not done": {
			prepare: func(t *testing.T, r *releaseRepo) string {
				t.Helper()
				r.commitRegistry(t, recordJSON(r.first, resultPass))
				raw, err := readIn(r.dir, registryFile)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, r.dir, registryFile, strings.Replace(string(raw), "`TestVRE01` | `Done`", "`TestVRE01` | `In progress`", 1))
				gitIn(t, r.dir, "commit", "-q", "-am", "regress")
				return gitIn(t, r.dir, "rev-parse", "HEAD")
			},
			want: `VR-E01 (pr): status is "In progress", want "Done"`,
		},
	}
}

func TestCheckReleaseListsEveryBlocker(t *testing.T) {
	t.Parallel()
	for name, tc := range blockers() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newReleaseRepo(t)
			rc := tc.prepare(t, r)
			report, err := checkRelease(t.Context(), r.dir, rc)
			if err != nil {
				t.Fatal(err)
			}
			if !containsAny(report.Problems, tc.want) {
				t.Errorf("problems = %q, want one containing %q", report.Problems, tc.want)
			}
		})
	}
}

func containsAny(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestCheckReleaseRejectsAnRCThatIsNotTheCheckout(t *testing.T) {
	t.Parallel()
	r := newReleaseRepo(t)
	r.commitRegistry(t, recordJSON(r.first, resultPass))
	report, err := checkRelease(t.Context(), r.dir, r.first)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAny(report.Problems, "is not the checked-out commit") {
		t.Errorf("problems = %q", report.Problems)
	}
}

func TestCheckReleaseRejectsADirtyCheckout(t *testing.T) {
	t.Parallel()
	r := newReleaseRepo(t)
	rc := r.commitRegistry(t, recordJSON(r.first, resultPass))
	writeFile(t, r.dir, "internal/vendor/a.go", "package vendor\n\nvar dirty = 1\n")
	report, err := checkRelease(t.Context(), r.dir, rc)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAny(report.Problems, "tracked files differ from HEAD") {
		t.Errorf("problems = %q", report.Problems)
	}
}

func TestCheckReleaseFailsOnAnUnknownRC(t *testing.T) {
	t.Parallel()
	r := newReleaseRepo(t)
	if _, err := checkRelease(t.Context(), r.dir, "no-such-ref"); !errors.Is(err, ErrRelease) {
		t.Errorf("err = %v, want ErrRelease", err)
	}
}

func TestReleaseEvidenceCommandWritesOnlyForAMetGate(t *testing.T) {
	t.Parallel()
	r := newReleaseRepo(t)
	rc := r.commitRegistry(t, recordJSON(r.first, resultFail))
	clk := clock.NewFake(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	var out strings.Builder
	err := runReleaseEvidence(t.Context(), clk, []string{flagDir, r.dir, "-rc", rc}, &out)
	if !errors.Is(err, ErrRelease) {
		t.Fatalf("err = %v, want ErrRelease", err)
	}
	if _, statErr := os.Stat(filepath.Join(r.dir, "release-evidence.md")); statErr == nil {
		t.Error("release-evidence.md was written for an unmet gate")
	}

	good := newReleaseRepo(t)
	rc = good.commitRegistry(t, recordJSON(good.first, resultPass))
	err = runReleaseEvidence(t.Context(), clk, []string{flagDir, good.dir, "-rc", rc, "-ci-run", "https://github.com/o/r/actions/runs/1"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(good.dir, "release-evidence.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"RC commit: `" + rc + "`", "Generated: 2026-11-23T12:00:00Z", "- https://github.com/o/r/actions/runs/1",
		"| `VR-E01` | `vendor-runtime §15` | `pr` | `TestVRE01` | `Done` |",
		"| `VR-E04` | `approval-relay` | `" + recordPath + "` | pass | linux/amd64 | `" + good.first + "` | dev | `internal/vendor/**` |",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("release-evidence.md lacks %q:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), "A-S01") {
		t.Error("a stable-gate row leaked into the release evidence")
	}
}

func TestReleaseEvidenceCommandRejectsABadCIRun(t *testing.T) {
	t.Parallel()
	err := runReleaseEvidence(t.Context(), clock.NewFake(time.Time{}), []string{"-ci-run", "http://insecure"}, &strings.Builder{})
	if !errors.Is(err, ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
}

// answers is what the owner types: y for every step.
func answers(n int, answer string) *strings.Reader {
	return strings.NewReader(strings.Repeat(answer+"\n", n))
}

func TestVerifyLiveWritesARecordThatTheReleaseGateAccepts(t *testing.T) {
	t.Parallel()
	r := newReleaseRepo(t)
	clk := clock.NewFake(time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC))
	var out strings.Builder
	args := []string{flagDir, r.dir, flagID, idVRE04, flagNotes, "three vendors, all relayed"}
	if err := runVerifyLive(t.Context(), clk, args, answers(20, "y"), &out); err != nil {
		t.Fatal(err)
	}
	rel := "quality/records/VR-E04/2026-11-23-" + runtime.GOOS + ".json"
	rec, probs := readRecord(r.dir, rel, idVRE04, runtime.GOOS)
	if len(probs) != 0 {
		t.Fatalf("the written record is invalid: %v", probs)
	}
	if rec.Result != resultPass || rec.Revision != r.first || string(rec.SchemaVersion) != "7" || rec.Check != "approval-relay" {
		t.Errorf("record = %+v", rec)
	}
	if !strings.Contains(out.String(), "wrote "+rel) {
		t.Errorf("output = %q", out.String())
	}
}

func TestVerifyLiveRecordsAFailedStepAndRequiresNotes(t *testing.T) {
	t.Parallel()
	r := newReleaseRepo(t)
	clk := clock.NewFake(time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC))
	err := runVerifyLive(t.Context(), clk, []string{flagDir, r.dir, flagID, idVRE04}, answers(20, "n"), &strings.Builder{})
	if !errors.Is(err, ErrVerifyLive) {
		t.Fatalf("err = %v, want ErrVerifyLive without notes", err)
	}
	args := []string{flagDir, r.dir, flagID, idVRE04, flagNotes, "codex deny did not reach the vendor"}
	if err := runVerifyLive(t.Context(), clk, args, answers(20, "n"), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	rec, probs := readRecord(r.dir, "quality/records/VR-E04/2026-11-23-"+runtime.GOOS+".json", idVRE04, runtime.GOOS)
	if len(probs) != 0 || rec.Result != resultFail {
		t.Errorf("record = %+v, problems = %v", rec, probs)
	}
}

func TestVerifyLiveRefusesBadInput(t *testing.T) {
	t.Parallel()
	r := newReleaseRepo(t)
	clk := clock.NewFake(time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC))
	for name, tc := range map[string]struct {
		args  []string
		stdin string
		want  error
	}{
		"unknown id":          {args: []string{flagID, "ZZ-E01"}, want: ErrUsage},
		"stable id":           {args: []string{flagID, "A-S01"}, want: ErrUsage},
		"missing measurement": {args: []string{flagID, "OR-E01"}, want: ErrUsage},
		"bad measurement":     {args: []string{flagID, "OR-E01", "-measure", "Ram=1"}, want: ErrUsage},
		"secret in notes":     {args: []string{flagID, idVRE04, flagNotes, "token=abcdef123456"}, want: ErrVerifyLive},
		"no answers":          {args: []string{flagID, idVRE04}, stdin: "", want: ErrVerifyLive},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := runVerifyLive(t.Context(), clk, append([]string{flagDir, r.dir}, tc.args...), strings.NewReader(tc.stdin), &strings.Builder{})
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifyLiveRefusesADirtyTreeAndAnExistingRecord(t *testing.T) {
	t.Parallel()
	r := newReleaseRepo(t)
	clk := clock.NewFake(time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC))
	args := []string{flagDir, r.dir, flagID, idVRE04}
	writeFile(t, r.dir, "internal/vendor/a.go", "package vendor\n\nvar dirty = 1\n")
	if err := runVerifyLive(t.Context(), clk, args, answers(20, "y"), &strings.Builder{}); !errors.Is(err, ErrVerifyLive) {
		t.Errorf("dirty tree: err = %v, want ErrVerifyLive", err)
	}
	gitIn(t, r.dir, "checkout", "-q", "--", "internal/vendor/a.go")
	if err := runVerifyLive(t.Context(), clk, args, answers(20, "y"), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if err := runVerifyLive(t.Context(), clk, args, answers(20, "y"), &strings.Builder{}); !errors.Is(err, ErrVerifyLive) {
		t.Errorf("second run the same day: err = %v, want ErrVerifyLive", err)
	}
}

func TestVerifyLiveStepsFilterByOperatingSystem(t *testing.T) {
	t.Parallel()
	check := liveChecks()["DA-E02"]
	var out strings.Builder
	passed, err := askSteps(answers(5, "y"), &out, "DA-E02", check)
	if err != nil || !passed {
		t.Fatalf("passed = %v, err = %v", passed, err)
	}
	wantLines := 1 // the header
	for _, s := range check.steps {
		if len(s.oses) == 0 || s.oses[0] == runtime.GOOS {
			wantLines++
		}
	}
	if got := strings.Count(out.String(), "\n"); got != wantLines {
		t.Errorf("printed %d lines, want %d:\n%s", got, wantLines, out.String())
	}
}

func TestEveryLiveCheckIsReleaseLiveRowShaped(t *testing.T) {
	t.Parallel()
	for id, c := range liveChecks() {
		if c.name == "" || len(c.covers) == 0 || len(c.steps) == 0 {
			t.Errorf("%s: check needs a name, covers and steps", id)
		}
	}
}

func TestSecretFindingsNamesWhatItFound(t *testing.T) {
	t.Parallel()
	got := secretFindings("sk-abcdefghijklmnop1234 and Bearer abcdefghijklmnopqrstu")
	want := []string{"a bearer token", "an API key"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("findings (-want +got):\n%s", diff)
	}
	if got := secretFindings("three vendors relayed; p95 120 ms"); len(got) != 0 {
		t.Errorf("clean text flagged: %v", got)
	}
}
