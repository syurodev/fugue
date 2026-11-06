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
// priority through HEAVY_WRAP and behind the machine-wide lock through
// HEAVY_LOCK; docs/quality-gates.md section 6. The mutation tasks are covered
// by the prefix rule below, so only the base name is listed.
func heavyTasks() []string {
	return []string{"test", "test-pkg", "coverage", "fuzz-short", "mutation", "lint", "vet"}
}

// isHeavyTask reports whether a task must carry both wrappers.
func isHeavyTask(task string) bool {
	return strings.HasPrefix(task, "mutation") || slices.Contains(heavyTasks(), task)
}

const (
	wrapVar = "{{.HEAVY_WRAP}}"
	lockVar = "{{.HEAVY_LOCK}}"
)

var (
	taskHeader  = regexp.MustCompile(`^  ([a-z][a-z0-9-]*):`)
	commandItem = regexp.MustCompile(`^      - (.+)$`)
)

// heavyTaskViolations returns the tasks whose commands are wrong for their
// kind: a heavy task whose commands miss one of the two wrappers, or a light
// task that carries them. The wrappers sit at exactly one level — on the
// heavy tasks' own commands — because `check` composes them and would nest
// the same flock and deadlock.
func heavyTaskViolations(taskfile string) []string {
	var bad []string
	inTasks := false
	task := ""
	for _, line := range strings.Split(taskfile, "\n") {
		switch {
		case strings.HasPrefix(line, "tasks:"):
			inTasks = true
		case inTasks && taskHeader.MatchString(line):
			task = taskHeader.FindStringSubmatch(line)[1]
		case inTasks:
			if v := commandViolation(task, line); v != "" {
				bad = append(bad, v)
			}
		}
	}
	return bad
}

// commandViolation names the wrapper rule a command item breaks, if any.
func commandViolation(task, line string) string {
	m := commandItem.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	cmd := strings.Trim(strings.TrimSpace(m[1]), "'\"")
	wrapped := strings.Contains(cmd, wrapVar) && strings.Contains(cmd, lockVar)
	wraps := strings.Contains(cmd, wrapVar) || strings.Contains(cmd, lockVar)
	switch {
	case isHeavyTask(task) && !wrapped:
		return task + " misses a wrapper"
	case !isHeavyTask(task) && wraps:
		return task + " must not carry the wrappers"
	default:
		return ""
	}
}

// The gate only holds while the real Taskfile wraps every heavy task with
// both wrappers and nothing else; the scanner must still catch a task that
// drops one or a light task that picks one up.
func TestHeavyTasksRunWrappedAndQueued(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	if bad := heavyTaskViolations(string(data)); len(bad) != 0 {
		t.Errorf("taskfile wrapper violations: %v", bad)
	}

	good := "tasks:\n" +
		"  test:\n" +
		"    cmds:\n" +
		"      - '{{.HEAVY_WRAP}} {{.HEAVY_LOCK}} go test -race ./...'\n" +
		"  arch:\n" +
		"    cmds:\n" +
		"      - go test -race ./internal/arch/...\n"
	if bad := heavyTaskViolations(good); len(bad) != 0 {
		t.Errorf("clean taskfile flagged: %v", bad)
	}
	for _, tc := range []struct{ taskfile, want string }{
		{
			"tasks:\n" +
				"  test-pkg:\n" +
				"    cmds:\n" +
				"      - go test -race ./...\n",
			"test-pkg misses a wrapper",
		},
		{
			"tasks:\n" +
				"  arch:\n" +
				"    cmds:\n" +
				"      - '{{.HEAVY_LOCK}} go test -race ./internal/arch/...'\n",
			"arch must not carry the wrappers",
		},
		{
			"tasks:\n" +
				"  check:\n" +
				"    cmds:\n" +
				"      - '{{.HEAVY_LOCK}} go test -race ./...'\n",
			"check must not carry the wrappers",
		},
	} {
		if got := heavyTaskViolations(tc.taskfile); !slices.Equal(got, []string{tc.want}) {
			t.Errorf("violations = %v, want [%s]", got, tc.want)
		}
	}
}

// The wrappers expand per platform locally and disappear under CI, so the
// runner keeps its own settings. The CI matrix covers darwin and linux.
func TestHeavyWrapExpansion(t *testing.T) {
	t.Parallel()
	const lockPrefix = "go run ./cmd/fuguegate exclusive --"
	want := "|"
	switch runtime.GOOS {
	case "darwin":
		want = "taskpolicy -c utility|" + lockPrefix
	case "linux":
		want = "nice -n 10|" + lockPrefix
	}
	for _, tc := range []struct{ name, ci, want string }{
		{"local", "", want},
		{"ci", "1", "|"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := []string{"-u", "CI"}
			if tc.ci != "" {
				args = append(args, "CI="+tc.ci)
			}
			args = append(args, toolBinary(t, "task"), "--silent", "wrap-probe")
			probe := taskProbe(t, "\n  wrap-probe:\n    cmds:\n      - printf '%s|%s' '{{.HEAVY_WRAP}}' '{{.HEAVY_LOCK}}'\n")
			out, code := execIn(t.Context(), t, probe, nil, "env", args...)
			if code != 0 {
				t.Fatalf("wrap probe exited %d: %s", code, out)
			}
			if strings.TrimSpace(out) != tc.want {
				t.Fatalf("wrappers = %q, want %q", out, tc.want)
			}
		})
	}
}
