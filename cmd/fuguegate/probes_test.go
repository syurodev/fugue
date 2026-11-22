package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeHeaderDeclaresEveryTarget(t *testing.T) {
	t.Parallel()
	const first = "# probe-test: ./example TestValue\n"
	targets, err := parseProbeTargets(first + "# probe-test: ./other TestOther\ndiff --git")
	if err != nil || len(targets) != 2 || targets[1] != (probeTarget{pkg: "./other", test: "TestOther"}) {
		t.Fatalf("parseProbeTargets() = %v, %v", targets, err)
	}
	for _, header := range []string{"", "# probe-test: ./example", "# probe-test: ./../other TestValue", first + first} {
		if _, err := parseProbeTargets(header); !errors.Is(err, ErrProbes) {
			t.Errorf("parseProbeTargets(%q) error = %v, want ErrProbes", header, err)
		}
	}
}

func TestProbeInventoryReportsMissingFiles(t *testing.T) {
	t.Parallel()
	if _, err := discoverProbes(t.TempDir(), "quality/probes"); !errors.Is(err, ErrProbes) {
		t.Fatalf("discoverProbes() error = %v, want ErrProbes", err)
	}
}

func TestProbeOutcomeRequiresExactCompletedTest(t *testing.T) {
	t.Parallel()
	const named = `{"Action":"pass","Package":"example/p","Test":"TestValue"}` + "\n"
	const complete = `{"Action":"pass","Package":"example/p"}` + "\n"
	if !probeOutcome(named+complete, "TestValue", "pass", nil) {
		t.Fatal("complete named pass was rejected")
	}
	if probeOutcome(named, "TestValue", "pass", nil) ||
		probeOutcome(named+complete, "TestOther", "pass", nil) ||
		probeOutcome(named+complete, "TestValue", "fail", os.ErrNotExist) {
		t.Fatal("incomplete or unrelated test result was accepted")
	}
}

func setupProbeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := capture(t.Context(), dir, "git", "init", "--quiet"); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestProbeRunsAgainstAnIsolatedSnapshot(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"go.mod":    "module example.test/probe\n\ngo 1.25\n",
		"value.txt": "baseline\n",
		"example/value_test.go": `package example

import ("os"; "testing")

func TestValue(t *testing.T) {
	value, err := os.ReadFile("../value.txt")
	if err != nil || string(value) != "baseline\n" {
		t.Fatal("unexpected fixture value")
	}
}
`,
		"probe.patch": "# probe-test: ./example TestValue\n" +
			"diff --git a/value.txt b/value.txt\n--- a/value.txt\n+++ b/value.txt\n@@ -1 +1 @@\n-baseline\n+mutated\n",
	}
	dir := setupProbeRepo(t, files)
	probe, err := readProbe(filepath.Join(dir, "probe.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runProbe(t.Context(), dir, probe, time.Minute, runProbeTest); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(dir, "value.txt"))
	if err != nil || string(value) != files["value.txt"] {
		t.Fatalf("source snapshot changed: %q, %v", value, err)
	}
}

func TestProbeMultipleTargetsRequireAllToFail(t *testing.T) {
	t.Parallel()
	const testSrc = `package example

import ("os"; "testing")

func TestFirst(t *testing.T) {
	if data, err := os.ReadFile("../value.txt"); err != nil || string(data) != "baseline\n" {
		t.Fatal("unexpected value")
	}
}

func TestSecond(t *testing.T) {
	if data, err := os.ReadFile("../value.txt"); err != nil || string(data) != "baseline\n" {
		t.Fatal("unexpected value")
	}
}

func TestUnrelated(t *testing.T) {}
`
	files := map[string]string{
		"go.mod":                "module example.test/probe\n\ngo 1.25\n",
		"value.txt":             "baseline\n",
		"example/value_test.go": testSrc,
		"all_fail.patch": "# probe-test: ./example TestFirst\n# probe-test: ./example TestSecond\n" +
			"diff --git a/value.txt b/value.txt\n--- a/value.txt\n+++ b/value.txt\n@@ -1 +1 @@\n-baseline\n+mutated\n",
		"one_passes.patch": "# probe-test: ./example TestFirst\n# probe-test: ./example TestUnrelated\n" +
			"diff --git a/value.txt b/value.txt\n--- a/value.txt\n+++ b/value.txt\n@@ -1 +1 @@\n-baseline\n+mutated\n",
	}
	dir := setupProbeRepo(t, files)

	good, err := readProbe(filepath.Join(dir, "all_fail.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runProbe(t.Context(), dir, good, time.Minute, runProbeTest); err != nil {
		t.Fatalf("all_fail.patch: %v", err)
	}

	partial, err := readProbe(filepath.Join(dir, "one_passes.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runProbe(t.Context(), dir, partial, time.Minute, runProbeTest); !errors.Is(err, ErrProbes) {
		t.Fatalf("one_passes.patch error = %v, want ErrProbes", err)
	}
}

func TestProbeRejectsBuildErrorsAndSkippedTests(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"go.mod":          "module example.test/probe\n\ngo 1.25\n",
		"example/prod.go": "package example\n\nfunc Value() int { return 1 }\n",
		"example/value_test.go": `package example

import "testing"

func TestValue(t *testing.T) {
	if Value() != 1 {
		t.Fatal("bad value")
	}
}

func TestSkipped(t *testing.T) {
	t.Skip("skipped")
}
`,
		"build_error.patch": "# probe-test: ./example TestValue\n" +
			"diff --git a/example/prod.go b/example/prod.go\n--- a/example/prod.go\n+++ b/example/prod.go\n@@ -3 +3 @@\n-func Value() int { return 1 }\n+func Value() int { return broken }\n",
		"skip_target.patch": "# probe-test: ./example TestSkipped\n" +
			"diff --git a/example/prod.go b/example/prod.go\n--- a/example/prod.go\n+++ b/example/prod.go\n@@ -3 +3 @@\n-func Value() int { return 1 }\n+func Value() int { return 2 }\n",
	}
	dir := setupProbeRepo(t, files)

	for _, name := range []string{"build_error.patch", "skip_target.patch"} {
		probe, err := readProbe(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := runProbe(t.Context(), dir, probe, time.Minute, runProbeTest); !errors.Is(err, ErrProbes) {
			t.Errorf("%s error = %v, want ErrProbes", name, err)
		}
	}
}

func TestProbeRejectsWrongTestFailingAndCleansTempDirs(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)

	files := map[string]string{
		"go.mod":          "module example.test/probe\n\ngo 1.25\n",
		"example/prod.go": "package example\n\nfunc Value() int { return 1 }\n",
		"example/value_test.go": `package example

import "testing"

func TestTarget(t *testing.T) {}

func TestOther(t *testing.T) {
	if Value() != 1 {
		t.Fatal("other failed")
	}
}
`,
		"wrong_test.patch": "# probe-test: ./example TestTarget\n" +
			"diff --git a/example/prod.go b/example/prod.go\n--- a/example/prod.go\n+++ b/example/prod.go\n@@ -3 +3 @@\n-func Value() int { return 1 }\n+func Value() int { return 2 }\n",
	}
	dir := setupProbeRepo(t, files)

	probe, err := readProbe(filepath.Join(dir, "wrong_test.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runProbe(t.Context(), dir, probe, time.Minute, runProbeTest); !errors.Is(err, ErrProbes) {
		t.Fatalf("wrong_test.patch error = %v, want ErrProbes", err)
	}

	const wrongFail = `{"Action":"pass","Package":"example/p","Test":"TestTarget"}` + "\n" +
		`{"Action":"fail","Package":"example/p","Test":"TestOther"}` + "\n" +
		`{"Action":"fail","Package":"example/p"}` + "\n"
	if probeOutcome(wrongFail, "TestTarget", "fail", nil) {
		t.Fatal("wrong failing test was accepted")
	}

	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover temp entries after runProbe: %v", entries)
	}
}
