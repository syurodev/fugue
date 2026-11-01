package quality

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const workflowsDir = ".github/workflows"

var (
	usesLine  = regexp.MustCompile(`(?m)^\s*(?:-\s*)?uses:\s*(\S+)`)
	fullSHA   = regexp.MustCompile(`@[0-9a-f]{40}$`)
	localUses = regexp.MustCompile(`^\./`)
)

// unpinnedActions lists `uses:` references that are not pinned to a full commit
// SHA, which a moved tag could otherwise change under the repository.
func unpinnedActions(workflow string) []string {
	var out []string
	for _, m := range usesLine.FindAllStringSubmatch(workflow, -1) {
		if !localUses.MatchString(m[1]) && !fullSHA.MatchString(m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS(".."), path.Join(workflowsDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// quality-gates section 9 lists the jobs a PR must pass; each is a job here and,
// once branch protection is on, a required status check.
func TestPRWorkflowRunsEveryRequiredJob(t *testing.T) {
	t.Parallel()
	pr := readWorkflow(t, "pr.yml")
	for _, job := range []string{"fmt", "lint", "vet", "arch", "test", "coverage", "deadcode", "mod", "proto", "fuzz-short", "mutation-changed", "gate-guard", "commit-lint"} {
		if !strings.Contains(pr, "\n  "+job+":\n") {
			t.Errorf("pr.yml has no job %q", job)
		}
	}
	for _, os := range []string{"ubuntu-latest", "macos-latest"} {
		if !strings.Contains(pr, os) {
			t.Errorf("pr.yml does not run tests on %s", os)
		}
	}
	for _, want := range []string{"fetch-depth: 0", "gateguard", "commitlint", "label-actor", "-approver"} {
		if !strings.Contains(pr, want) {
			t.Errorf("pr.yml lacks %q", want)
		}
	}
}

func TestNightlyWorkflowRunsTheLongJobs(t *testing.T) {
	t.Parallel()
	nightly := readWorkflow(t, "nightly.yml")
	for _, want := range []string{"schedule:", "fuzz-long:", "mutation-full:", "repeat-tests:", "windows-build:", "-time 10m"} {
		if !strings.Contains(nightly, want) {
			t.Errorf("nightly.yml lacks %q", want)
		}
	}
}

func TestActionsArePinnedToCommits(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"pr.yml", "nightly.yml"} {
		if got := unpinnedActions(readWorkflow(t, name)); len(got) > 0 {
			t.Errorf("%s uses actions not pinned to a commit SHA: %v", name, got)
		}
	}
	bad, err := os.ReadFile(filepath.Join("testdata", "actions", "bad", workflowsDir, "broken.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := unpinnedActions(string(bad)); len(got) != 1 || got[0] != "actions/checkout@v4" {
		t.Errorf("the violating fixture must be reported, got %v", got)
	}
}

// actionlint checks the syntax and expressions of a workflow. The files are named
// explicitly because inside a repository it would otherwise lint the repository's
// own workflows.
func TestActionlintGate(t *testing.T) {
	t.Parallel()
	bin := toolBinary(t, "actionlint")
	out, code := execIn(t.Context(), t, ".", nil, bin, filepath.Join("testdata", "actions", "bad", workflowsDir, "broken.yml"))
	if code == 0 {
		t.Errorf("actionlint passed on a broken workflow:\n%s", out)
	}
	for _, want := range []string{"missing", "duplicated"} {
		if !strings.Contains(out, want) {
			t.Errorf("actionlint did not mention %q:\n%s", want, out)
		}
	}
	if out, code := execIn(t.Context(), t, ".", nil, bin, filepath.Join("testdata", "actions", "good", workflowsDir, "ok.yml")); code != 0 {
		t.Errorf("actionlint failed on a clean workflow:\n%s", out)
	}
}

func TestCommitHookIsInstalledByTaskHooks(t *testing.T) {
	t.Parallel()
	info, err := os.Stat("../.githooks/commit-msg")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error(".githooks/commit-msg is not executable")
	}
	hook, err := os.ReadFile("../.githooks/commit-msg")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hook), "fuguegate commitmsg") {
		t.Error("the hook does not call fuguegate commitmsg")
	}
	task, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(task), "git config core.hooksPath .githooks") {
		t.Error("`task hooks` does not set core.hooksPath")
	}
}
