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

const (
	workflowsDir = ".github/workflows"
	osUbuntu     = "ubuntu-latest"
	osMacOS      = "macos-latest"
)

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

// prGateTasks lists the tasks quality-gates section 9 requires of every PR;
// they are the jobs of pr.yml, and their test-shaped members are also the
// heavy tasks of section 6.
func prGateTasks() []string {
	return []string{"fmt", "lint", "vet", "arch", "test", "coverage", "deadcode", "mod", "proto", "fuzz-short", "mutation-changed", "gate-guard", "commit-lint"}
}

// quality-gates section 9 lists the jobs a PR must pass; each is a job in pr.yml
// or pr-meta.yml and, once branch protection is on, a required status check.
func TestPRWorkflowRunsEveryRequiredJob(t *testing.T) {
	t.Parallel()
	pr := readWorkflow(t, "pr.yml")
	meta := readWorkflow(t, "pr-meta.yml")
	for _, job := range prGateTasks() {
		if !strings.Contains(pr+meta, "\n  "+job+":\n") {
			t.Errorf("pr.yml and pr-meta.yml have no job %q", job)
		}
	}
	for _, os := range []string{osUbuntu, osMacOS} {
		if !strings.Contains(pr, os) {
			t.Errorf("pr.yml does not run tests on %s", os)
		}
	}
	for _, want := range []string{"fetch-depth: 0", "gateguard", "commitlint", "label-actor", "-approver"} {
		if !strings.Contains(meta, want) {
			t.Errorf("pr-meta.yml lacks %q", want)
		}
	}
}

// Labels and title edits rerun only the meta checks; the code gates run on
// pushes, decide their scope with fuguegate changes, and every merge runs the
// macOS tests that a portable change skipped.
func TestPRWorkflowsSplitByTrigger(t *testing.T) {
	t.Parallel()
	pr := readWorkflow(t, "pr.yml")
	meta := readWorkflow(t, "pr-meta.yml")
	main := readWorkflow(t, "main.yml")
	if !strings.Contains(pr, "types: [opened, synchronize, reopened]\n") {
		t.Error("pr.yml must run only on opened, synchronize and reopened")
	}
	for _, want := range []string{"edited", "labeled", "unlabeled"} {
		if !strings.Contains(meta, want) {
			t.Errorf("pr-meta.yml does not react to %q", want)
		}
	}
	for _, want := range []string{"fuguegate changes", "needs: changes", "needs: [changes, test]"} {
		if !strings.Contains(pr, want) {
			t.Errorf("pr.yml lacks %q", want)
		}
	}
	for _, want := range []string{"branches: [main]", "runs-on: macos-latest", "go tool task test"} {
		if !strings.Contains(main, want) {
			t.Errorf("main.yml lacks %q", want)
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

// A cache written by a pull request is invisible to other pull requests, and a key
// is written once, so only the default branch can hand every PR a warm cache.
func TestCacheWorkflowWarmsTheDefaultBranch(t *testing.T) {
	t.Parallel()
	cache := readWorkflow(t, "cache.yml")
	for _, want := range []string{"push:", "branches: [main]", "schedule:", osUbuntu, osMacOS, "go tool task warm"} {
		if !strings.Contains(cache, want) {
			t.Errorf("cache.yml lacks %q", want)
		}
	}
	task, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  warm:", "-race -shuffle=on -count=1 -covermode=atomic", "golangci-lint"} {
		if !strings.Contains(string(task), want) {
			t.Errorf("Taskfile.yml `warm` lacks %q: it must build what the CI builds", want)
		}
	}
}

// The coverage job reuses the profile the ubuntu test job already produced instead
// of running the whole suite a second time.
func TestCoverageJobReusesTheTestProfile(t *testing.T) {
	t.Parallel()
	pr := readWorkflow(t, "pr.yml")
	for _, want := range []string{"upload-artifact", "download-artifact", "needs: [changes, test]", "name: cover-profile"} {
		if !strings.Contains(pr, want) {
			t.Errorf("pr.yml lacks %q", want)
		}
	}
	_, coverage, _ := strings.Cut(pr, "\n  coverage:\n")
	coverage, _, _ = strings.Cut(coverage, "\n  deadcode:\n")
	if strings.Contains(coverage, "task test") {
		t.Error("the coverage job runs the test suite again")
	}
}

func TestActionsArePinnedToCommits(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"pr.yml", "pr-meta.yml", "main.yml", "nightly.yml", "cache.yml", "ui.yml"} {
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

// The ui/ module is a separate Go module with a cgo build, so its gates run
// in their own workflow on both OSes, triggered by the changes scope for ui/,
// pkg/ and proto/ (ADR 0002, quality-gates §9).
func TestUIWorkflowRunsTheModuleGates(t *testing.T) {
	t.Parallel()
	ui := readWorkflow(t, "ui.yml")
	pr := readWorkflow(t, "pr.yml")
	for _, want := range []string{osUbuntu, osMacOS, "-race -shuffle=on", "go vet ./...", "golangci-lint"} {
		if !strings.Contains(ui, want) {
			t.Errorf("ui.yml lacks %q", want)
		}
	}
	for _, pkg := range []string{"gcc", "pkg-config", "libwayland-dev", "libx11-xcb-dev", "libxkbcommon-x11-dev", "libgles2-mesa-dev"} {
		if !strings.Contains(ui, pkg) {
			t.Errorf("ui.yml does not install the Gio build requirement %q", pkg)
		}
	}
	if !strings.Contains(pr, "ui: ${{ steps.scope.outputs.ui }}") {
		t.Error("pr.yml does not export the ui change scope")
	}
}
