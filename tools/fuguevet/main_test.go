package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func vet(ctx context.Context, t *testing.T, bin, dir string) (string, bool) {
	t.Helper()
	roadmap, err := filepath.Abs(filepath.Join("testdata", "roadmap.md"))
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G204: the program is `go`; arguments are literals and test-controlled paths.
	cmd := exec.CommandContext(ctx, "go", "vet", "-vettool="+bin, "-todoref.roadmap="+roadmap, "./...")
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	var exit *exec.ExitError
	if err := cmd.Run(); err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return out.String(), cmd.ProcessState.Success()
}

// The analyzer tests prove each rule in isolation; this one proves the binary
// that `task vet` runs registers all five.
func TestVettoolRunsEveryAnalyzer(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "fuguevet")
	//nolint:gosec // G204: the program is `go`; arguments are literals and a temporary path.
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, ok := vet(t.Context(), t, bin, filepath.Join("testdata", "module"))
	if ok {
		t.Fatalf("go vet passed on the violating module:\n%s", out)
	}
	for analyzer, message := range map[string]string{
		"kernelpure":  `kernel import "os" is not allowlisted`,
		"nobarego":    "bare go statement",
		"secretlog":   "token looks like a secret",
		"todoref":     "must read",
		"noprotoleak": "proto-generated and Connect code stays inside internal/hostapi",
	} {
		if !strings.Contains(out, message) {
			t.Errorf("%s did not report %q:\n%s", analyzer, message, out)
		}
	}
	if out, ok := vet(t.Context(), t, bin, filepath.Join("testdata", "clean")); !ok {
		t.Errorf("go vet failed on the clean module:\n%s", out)
	}
}
