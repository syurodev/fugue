package quality

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS(".."), name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A PR that changes tests reruns their packages ten times on Linux, and on
// macOS when the change is platform-specific (quality-gates section 6).
func TestPRStressRerunsChangedTestPackages(t *testing.T) {
	t.Parallel()
	pr := readWorkflow(t, "pr.yml")
	task := readRepoFile(t, "Taskfile.yml")
	for _, want := range []string{
		"\n  stress:\n", "needs: changes", "needs.changes.outputs.test_pkgs",
		"needs.changes.outputs.test_run", osUbuntu, osMacOS, "needs.changes.outputs.platform", "task test-pkg PKG=\"$PACKAGES\" RUN=\"$TESTS\" COUNT=1", "seq 1 10",
	} {
		if !strings.Contains(pr, want) {
			t.Errorf("pr.yml lacks %q", want)
		}
	}
	// test-pkg is what the job runs, so it must keep the PR test flags.
	for _, want := range []string{"go test -race -shuffle=on -count={{if .COUNT}}{{.COUNT}}{{else}}1{{end}}"} {
		if !strings.Contains(task, want) {
			t.Errorf("Taskfile.yml test-pkg lacks %q", want)
		}
	}
	// One -count=10 run shares one 10-minute timeout across all counts, so a PR
	// that edits many tests of a heavy package would still time out.
	if strings.Contains(pr, "COUNT=10") {
		t.Error("stress repeats with ten separate -count=1 runs, never one -count=10 run")
	}
	// Ten counts of a whole heavy package (internal/app takes minutes per
	// count) pass the timeout, so the job reruns only the changed tests: the
	// -run pattern is required, and a PR with no changed test function skips.
	if !strings.Contains(pr, `[ -n "$TESTS" ]`) {
		t.Error("stress must skip when no changed test function is known, never rerun whole packages")
	}
	_, afterTestPkg, _ := strings.Cut(task, "\n  test-pkg:\n")
	testPkg, _, _ := strings.Cut(afterTestPkg, "\n  coverage:")
	if strings.Contains(pr, "-timeout") || strings.Contains(testPkg, "-timeout") {
		t.Error("stress keeps the default 10-minute package timeout; never set a longer one")
	}
}

// The nightly job repeats every test twenty times with the race detector and a
// fresh shuffle on both systems and names the tests that both passed and
// failed.
func TestNightlyStressRunsTwentyCountsOnUbuntuAndMacOS(t *testing.T) {
	t.Parallel()
	nightly := readWorkflow(t, "nightly.yml")
	task := readRepoFile(t, "Taskfile.yml")
	for _, want := range []string{
		"repeat-tests (${{ matrix.os }})", osUbuntu, osMacOS, "task nightly-test",
		"fuguegate flaky -in nightly-test.json",
	} {
		if !strings.Contains(nightly, want) {
			t.Errorf("nightly.yml lacks %q", want)
		}
	}
	for _, want := range []string{
		"NIGHTLY_REPEATS: 20", "seq 1 {{.NIGHTLY_REPEATS}}",
		"go test -race -shuffle=on -count=1 -timeout=10m -json ./...",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("Taskfile.yml nightly-test lacks %q", want)
		}
	}
	if strings.Contains(task, "-count=5") {
		t.Error("the nightly run must not fall back to five repetitions")
	}
}

// Speed budgets are tests under the perf build tag: no PR or main job and not
// `task check` runs them; only the nightly job does.
func TestPerfBudgetsRunOnlyInNightly(t *testing.T) {
	t.Parallel()
	task := readRepoFile(t, "Taskfile.yml")
	_, afterPerf, ok := strings.Cut(task, "\n  perf:\n")
	if !ok || !strings.Contains(afterPerf, "-tags perf") {
		t.Fatal("Taskfile.yml has no perf task running the perf-tagged tests")
	}
	for _, name := range []string{"pr.yml", "pr-meta.yml", "main.yml", "ui.yml", "cache.yml"} {
		if strings.Contains(readWorkflow(t, name), "perf") {
			t.Errorf("%s mentions perf: speed budgets run only in nightly.yml", name)
		}
	}
	if !strings.Contains(readWorkflow(t, "nightly.yml"), "task perf") {
		t.Error("nightly.yml does not run task perf")
	}
	_, afterCheck, _ := strings.Cut(task, "\n  check:")
	check, _, _ := strings.Cut(afterCheck, "\n  fix:")
	if strings.Contains(check, "perf") {
		t.Error("task check runs the perf budgets")
	}
	for _, dir := range []string{"internal/perf", "ui/internal/mdview", "ui/internal/transcriptview"} {
		for _, file := range perfFiles(t, dir) {
			if head := readRepoFile(t, dir+"/"+file); !strings.HasPrefix(head, "//go:build perf\n") {
				t.Errorf("%s/%s must start with //go:build perf", dir, file)
			}
		}
	}
}

// perfFiles lists the speed-budget test files of dir: every test file of
// internal/perf and the *_perf_test.go files elsewhere.
func perfFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir("../" + dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, "_test.go") && (dir == "internal/perf" || strings.HasSuffix(name, "_perf_test.go")) {
			out = append(out, name)
		}
	}
	return out
}

// The upgrade and downgrade drills run across two real builds only in the
// nightly job: the cross-build test sits behind the drill build tag, and no PR
// job or `task check` runs it.
func TestDrillRunsOnlyInNightly(t *testing.T) {
	t.Parallel()
	task := readRepoFile(t, "Taskfile.yml")
	_, afterDrill, ok := strings.Cut(task, "\n  drill:\n")
	drill, _, _ := strings.Cut(afterDrill, "\n  windows-build:")
	if !ok || !strings.Contains(drill, "-tags drill") || !strings.Contains(drill, "TestDrillAcrossBuilds") {
		t.Fatal("Taskfile.yml has no drill task running the drill-tagged cross-build test")
	}
	workflows, err := os.ReadDir("../.github/workflows")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range workflows {
		if name := entry.Name(); name != "nightly.yml" && strings.Contains(readWorkflow(t, name), "task drill") {
			t.Errorf("%s runs task drill: the cross-build drill runs only in nightly.yml", name)
		}
	}
	if !strings.Contains(readWorkflow(t, "nightly.yml"), "task drill") {
		t.Error("nightly.yml does not run task drill")
	}
	_, afterCheck, _ := strings.Cut(task, "\n  check:")
	check, _, _ := strings.Cut(afterCheck, "\n  fix:")
	if strings.Contains(check, "drill") {
		t.Error("task check runs the drill")
	}
	if head := readRepoFile(t, "cmd/fugue/drill_builds_test.go"); !strings.HasPrefix(head, "//go:build drill\n") {
		t.Error("cmd/fugue/drill_builds_test.go must start with //go:build drill")
	}
}
