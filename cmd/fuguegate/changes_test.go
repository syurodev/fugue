package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestChangeScope pins which pull requests skip which test jobs. Every case
// that could hide a regression must land on the side that runs more.
func TestChangeScope(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/kernel/decide.go", "// Package kernel.\npackage kernel\n")
	write("internal/vendor/proc.go", "//go:build unix\n\npackage vendor\n")
	write("internal/vendor/after.go", "package vendor\n\n//go:build unix\n")

	cases := []struct {
		name    string
		changed []string
		want    changeScope
	}{
		{"docs only", []string{"docs/plans/f5.2-event-stream.md", "README.md", "docs/adr/0002-vendor-domain.md", "AGENTS.md"}, changeScope{}},
		{"markdown fixture is code", []string{"internal/arch/testdata/layers-good/docs/adr/0002-vendor-domain.md"}, changeScope{code: true}},
		{"root testdata markdown is code", []string{"testdata/notes.md"}, changeScope{code: true}},
		{"gate document is code", []string{"docs/quality-gates.md"}, changeScope{code: true}},
		{"portable Go", []string{"internal/kernel/decide.go", "docs/ports.md"}, changeScope{code: true}},
		{"non-Go code", []string{"proto/fugue/host/v1/event.proto"}, changeScope{code: true}},
		{"build constraint", []string{"internal/vendor/proc.go"}, everything()},
		{"constraint after the package clause is not one", []string{"internal/vendor/after.go"}, changeScope{code: true}},
		{"deleted Go file", []string{"internal/vendor/gone.go"}, everything()},
		{"darwin suffix", []string{"internal/x/peer_darwin.go"}, everything()},
		{"unix test suffix", []string{"internal/x/lock_unix_test.go"}, everything()},
		{"other suffix", []string{"internal/x/socket_other.go"}, everything()},
		{"os before arch", []string{"internal/x/sys_linux_amd64.go"}, everything()},
		{"process tree", []string{"internal/process/stop.go"}, everything()},
		{"host api", []string{"internal/hostapi/README.md"}, changeScope{}},
		{"host api code", []string{"internal/hostapi/server.go"}, everything()},
		{"entry point", []string{"cmd/fugue/serve.go"}, everything()},
		{"workflow", []string{".github/workflows/pr.yml"}, everything()},
		{"module", []string{"go.sum"}, everything()},
		{"task file", []string{"Taskfile.yml"}, everything()},
		{"empty change set runs everything", nil, everything()},
		{"blank entries only", []string{"", " "}, everything()},
	}
	for _, tc := range cases {
		got := classifyChanges(dir, tc.changed)
		if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(changeScope{})); diff != "" {
			t.Errorf("%s: scope mismatch (-want +got):\n%s", tc.name, diff)
		}
	}
}

func TestChangesCommandPrintsStepOutputs(t *testing.T) {
	t.Parallel()
	out, err := execute(t, "changes", "-files", "docs/error-codes.md")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("code=false\nplatform=false\n", out); diff != "" {
		t.Errorf("output (-want +got):\n%s", diff)
	}
	out, err = execute(t, "changes", "-files", "cmd/fugue/serve.go")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("code=true\nplatform=true\n", out); diff != "" {
		t.Errorf("output (-want +got):\n%s", diff)
	}
}

// A diff that cannot be computed must never skip a job.
func TestChangesFailsSafeOnABrokenDiff(t *testing.T) {
	t.Parallel()
	out, err := execute(t, "changes", "-dir", t.TempDir(), "-base", "no-such-ref", "-head", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "code=true") || !strings.Contains(out, "platform=true") {
		t.Errorf("a broken diff must run everything, got:\n%s", out)
	}
	if _, err := execute(t, "changes"); !errors.Is(err, ErrUsage) {
		t.Errorf("no input: err = %v, want ErrUsage", err)
	}
}
