package quality

import (
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestTaskResourceDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, ci, procs, flags, workers string
		want                            string
	}{
		{"local", "", "", "", "", "2|-p=1|1"},
		{"ci", "true", "", "", "", "||"},
		{"local_override", "", "4", "-p=3 -tags=custom", "2", "4|-p=3 -tags=custom|2"},
		{"ci_override", "true", "4", "-p=3 -tags=custom", "2", "4|-p=3 -tags=custom|2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := taskResourceProbe(t)
			args := []string{"-u", "CI", "-u", "GOMAXPROCS", "-u", "GOFLAGS", "-u", "GREMLINS_UNLEASH_WORKERS"}
			for _, entry := range []string{"CI=" + tc.ci, "GOMAXPROCS=" + tc.procs, "GOFLAGS=" + tc.flags, "GREMLINS_UNLEASH_WORKERS=" + tc.workers} {
				if !strings.HasSuffix(entry, "=") {
					args = append(args, entry)
				}
			}
			args = append(args, toolBinary(t, "task"), "--silent", "resource-probe")
			out, code := execIn(t.Context(), t, dir, nil, "env", args...)
			if code != 0 {
				t.Fatalf("Task resource probe exited %d: %s", code, out)
			}
			if strings.TrimSpace(out) != tc.want {
				t.Fatalf("Task environment = %q, want %q", out, tc.want)
			}
		})
	}
}

func taskResourceProbe(t *testing.T) string {
	t.Helper()
	// Exercise the real Taskfile's exported environment in a child shell. This
	// catches templating, inheritance and CI/default precedence errors together.
	return taskProbe(t, `
  resource-probe:
    cmds:
      - printf '%s|%s|%s' "$GOMAXPROCS" "$GOFLAGS" "$GREMLINS_UNLEASH_WORKERS"
`)
}

// taskProbe copies the real Taskfile with an extra probe task appended and
// returns the directory it lives in.
func taskProbe(t *testing.T, probe string) string {
	t.Helper()
	data, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := root.WriteFile("Taskfile.yml", append(data, probe...), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// heavyTasks lists the Taskfile tasks whose commands must run at low local
// priority through the HEAVY_WRAP prefix; docs/quality-gates.md section 6.
// The mutation tasks are covered by the prefix rule below, so only the base
// name is listed.
func heavyTasks() []string {
	return []string{"test", "test-pkg", "coverage", "fuzz-short", "mutation", "lint", "vet"}
}

// isHeavyTask reports whether a task must carry the priority wrapper.
func isHeavyTask(task string) bool {
	return strings.HasPrefix(task, "mutation") || slices.Contains(heavyTasks(), task)
}

var (
	taskHeader  = regexp.MustCompile(`^  ([a-z][a-z0-9-]*):`)
	commandItem = regexp.MustCompile(`^      - (.+)$`)
)

// heavyTaskViolations returns the heavy tasks that carry a command without
// the priority wrapper. Composition references and the light tasks are not
// commands of the heavy task itself.
func heavyTaskViolations(taskfile string) []string {
	var bad []string
	inTasks := false
	task := ""
	for _, line := range strings.Split(taskfile, "\n") {
		switch {
		case strings.HasPrefix(line, "tasks:"):
			inTasks = true
		case inTasks:
			if m := taskHeader.FindStringSubmatch(line); m != nil {
				task = m[1]
				continue
			}
			if m := commandItem.FindStringSubmatch(line); m != nil && isHeavyTask(task) {
				if !wrappedCommand(m[1]) {
					bad = append(bad, task)
				}
			}
		}
	}
	return bad
}

// wrappedCommand reports whether a command item runs through the priority
// wrapper; quotes and the `task:` composition reference are not commands.
func wrappedCommand(item string) bool {
	cmd := strings.Trim(strings.TrimSpace(item), "'\"")
	if strings.HasPrefix(cmd, "task:") {
		return true
	}
	return strings.HasPrefix(cmd, "{{.HEAVY_WRAP}} ")
}

// The gate only holds while the real Taskfile carries the wrapper on every
// heavy task, and the scanner must still catch a task that drops it.
func TestHeavyTasksRunAtLowPriority(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	if bad := heavyTaskViolations(string(data)); len(bad) != 0 {
		t.Errorf("heavy tasks without the {{.HEAVY_WRAP}} prefix: %v", bad)
	}

	good := "tasks:\n" +
		"  test:\n" +
		"    cmds:\n" +
		"      - '{{.HEAVY_WRAP}} go test -race ./...'\n" +
		"  arch:\n" +
		"    cmds:\n" +
		"      - go test -race ./internal/arch/...\n"
	if bad := heavyTaskViolations(good); len(bad) != 0 {
		t.Errorf("clean taskfile flagged: %v", bad)
	}
	violating := "tasks:\n" +
		"  test-pkg:\n" +
		"    cmds:\n" +
		"      - go test -race ./...\n"
	if got := heavyTaskViolations(violating); !slices.Equal(got, []string{"test-pkg"}) {
		t.Errorf("violations = %v, want [test-pkg]", got)
	}
}

// The wrapper expands per platform locally and disappears under CI, so the
// runner keeps its own settings. The CI matrix covers darwin and linux.
func TestHeavyWrapExpansion(t *testing.T) {
	t.Parallel()
	want := ""
	switch runtime.GOOS {
	case "darwin":
		want = "taskpolicy -c utility"
	case "linux":
		want = "nice -n 10"
	}
	for _, tc := range []struct{ name, ci, want string }{
		{"local", "", want},
		{"ci", "1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := []string{"-u", "CI"}
			if tc.ci != "" {
				args = append(args, "CI="+tc.ci)
			}
			args = append(args, toolBinary(t, "task"), "--silent", "wrap-probe")
			probe := taskProbe(t, "\n  wrap-probe:\n    cmds:\n      - printf '%s' '{{.HEAVY_WRAP}}'\n")
			out, code := execIn(t.Context(), t, probe, nil, "env", args...)
			if code != 0 {
				t.Fatalf("wrap probe exited %d: %s", code, out)
			}
			if strings.TrimSpace(out) != tc.want {
				t.Fatalf("HEAVY_WRAP = %q, want %q", out, tc.want)
			}
		})
	}
}
