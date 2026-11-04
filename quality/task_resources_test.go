package quality

import (
	"os"
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
	data, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the real Taskfile's exported environment in a child shell. This
	// catches templating, inheritance and CI/default precedence errors together.
	probe := `
  resource-probe:
    cmds:
      - printf '%s|%s|%s' "$GOMAXPROCS" "$GOFLAGS" "$GREMLINS_UNLEASH_WORKERS"
`
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
