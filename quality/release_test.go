package quality

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const releaseTestFile = "cmd/fuguegate/release_evidence_test.go"

// TestReleaseEvidenceRunsOnlyInRelease guards TestReleaseEvidence
// (cmd/fuguegate). That test fails until every release row is Done and every
// live record is valid, so it must run only in `task release-check` on an RC:
// under the release build tag, never in a PR, main, nightly or ui job, and
// never skipping when it cannot run (verification section 7).
func TestReleaseEvidenceRunsOnlyInRelease(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, releaseTestFile)
	if !strings.HasPrefix(src, "//go:build release\n") {
		t.Errorf("%s must start with //go:build release", releaseTestFile)
	}
	if strings.Contains(src, ".Skip") {
		t.Errorf("%s skips: a missing RC must fail the test", releaseTestFile)
	}
	if !strings.Contains(src, `t.Fatalf("%s is not set`) {
		t.Errorf("%s must fail when the RC commit is not set", releaseTestFile)
	}
	if got := declaringFiles(t, "func TestRelease"+"Evidence("); len(got) != 1 || got[0] != releaseTestFile {
		t.Errorf("TestReleaseEvidence must be declared once, in %s, got %v", releaseTestFile, got)
	}
}

func TestTaskfileRunsTheReleaseGateOutsideCheck(t *testing.T) {
	t.Parallel()
	task := readRepoFile(t, "Taskfile.yml")
	for _, want := range []string{
		"\n  release-check:\n", "- task: check\n", "- task: probes\n", "- task: mutation\n", "- task: drill\n", "- task: release-evidence\n",
		"\n  release-evidence:\n", "go test -tags release -race -shuffle=on -count=1 -run '^TestReleaseEvidence$' ./cmd/fuguegate",
		"FUGUE_RC_COMMIT", "fuguegate release-evidence",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("Taskfile.yml lacks %q", want)
		}
	}
	_, afterCheck, _ := strings.Cut(task, "\n  check:")
	check, _, _ := strings.Cut(afterCheck, "\n  fix:")
	if strings.Contains(check, "release") {
		t.Error("task check runs the release gate")
	}
}

func TestNoOtherWorkflowRunsTheReleaseGate(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir("../" + workflowsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "release.yml" {
			continue
		}
		wf := readWorkflow(t, name)
		for _, banned := range []string{"release-check", "release-evidence", "TestReleaseEvidence", "-tags release"} {
			if strings.Contains(wf, banned) {
				t.Errorf("%s mentions %q: the release gate runs only from release.yml", name, banned)
			}
		}
	}
}

func TestReleaseWorkflowIsManualAndRunsBothOperatingSystems(t *testing.T) {
	t.Parallel()
	wf := readWorkflow(t, "release.yml")
	for _, want := range []string{"workflow_dispatch:", osUbuntu, osMacOS, "go tool task release-check", "CI_RUN=", "release-evidence.md", "fetch-depth: 0"} {
		if !strings.Contains(wf, want) {
			t.Errorf("release.yml lacks %q", want)
		}
	}
	for _, banned := range []string{"pull_request", "push:", "cron:"} {
		if strings.Contains(wf, banned) {
			t.Errorf("release.yml has the trigger %q: it runs only by hand on an RC", banned)
		}
	}
}

// declaringFiles lists the Go test files, relative to the repository root,
// that contain decl.
func declaringFiles(t *testing.T, decl string) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(os.DirFS(".."), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if strings.Contains(readRepoFile(t, p), decl) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
