package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	pkgKernel = "internal/kernel"
	pkgA      = "internal/a"
	pkgB      = "internal/b"
)

const sampleProfile = `mode: atomic
github.com/syurodev/fugue/internal/kernel/a.go:1.1,2.2 10 1
github.com/syurodev/fugue/internal/kernel/a.go:3.1,4.2 1 0
github.com/syurodev/fugue/internal/config/c.go:1.1,2.2 4 1
github.com/syurodev/fugue/pkg/pb/x.go:1.1,2.2 9 0
github.com/syurodev/fugue/cmd/fugue/main.go:1.1,2.2 9 0
`

func TestParseProfileSkipsExcluded(t *testing.T) {
	t.Parallel()
	got, err := parseProfile(strings.NewReader(sampleProfile))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]packageStats{
		pkgKernel:         {statements: 11, covered: 10},
		"internal/config": {statements: 4, covered: 4},
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(packageStats{})); diff != "" {
		t.Fatalf("stats mismatch (-want +got):\n%s", diff)
	}
}

func TestParseProfileRejectsGarbage(t *testing.T) {
	t.Parallel()
	for _, line := range []string{"no colon here", "f.go:1.1,2.2 x 1", "f.go:1.1,2.2 1 y", "f.go:1.1,2.2 1"} {
		if _, err := parseProfile(strings.NewReader(line)); !errors.Is(err, ErrProfileSyntax) {
			t.Errorf("%q: err = %v, want ErrProfileSyntax", line, err)
		}
	}
}

// The kernel sample is 90.9% covered: enough for the 90% floor, but a baseline
// of 95 makes it a regression, and a 80% package with 75% coverage fails.
func TestEvaluate(t *testing.T) {
	t.Parallel()
	stats := map[string]packageStats{
		pkgKernel:         {statements: 11, covered: 10},
		"internal/config": {statements: 4, covered: 3},
		"internal/empty":  {},
	}
	got := evaluate(stats, map[string]float64{pkgKernel: 95})
	want := []string{
		"internal/config: 75.0% is below the 80% floor",
		pkgKernel + ": 90.9% dropped more than 0.5 points below the 95.0% baseline",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("problems mismatch (-want +got):\n%s", diff)
	}
	if got := evaluate(stats, map[string]float64{pkgKernel: 91}); len(got) != 1 {
		t.Fatalf("within slack should pass the baseline check, got %v", got)
	}
}

func TestStrictFloorForGuardedPackages(t *testing.T) {
	t.Parallel()
	stats := map[string]packageStats{"internal/crypto": {statements: 100, covered: 89}}
	if got := evaluate(stats, nil); len(got) != 1 || !strings.Contains(got[0], "90% floor") {
		t.Fatalf("crypto at 89%% must fail the 90%% floor, got %v", got)
	}
}

func TestRaisedNeverLowers(t *testing.T) {
	t.Parallel()
	stats := map[string]packageStats{
		pkgA: {statements: 100, covered: 97},
		pkgB: {statements: 100, covered: 85},
	}
	got := raised(stats, map[string]float64{pkgA: 90, pkgB: 99})
	want := map[string]float64{pkgA: 97, pkgB: 99}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("baseline mismatch (-want +got):\n%s", diff)
	}
}

func TestRunCoverageEndToEnd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	profile := filepath.Join(dir, "cover.out")
	baseline := filepath.Join(dir, "coverage.json")
	if err := os.WriteFile(baseline, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	failing := "mode: atomic\ngithub.com/syurodev/fugue/internal/config/c.go:1.1,2.2 4 0\n"
	if err := os.WriteFile(profile, []byte(failing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "coverage", "-profile", profile, "-baseline", baseline); !errors.Is(err, ErrCoverage) {
		t.Fatalf("uncovered package: err = %v, want ErrCoverage", err)
	}
	covered := "mode: atomic\ngithub.com/syurodev/fugue/internal/config/c.go:1.1,2.2 4 1\n"
	if err := os.WriteFile(profile, []byte(covered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "coverage", "-profile", profile, "-baseline", baseline, "-update"); err != nil {
		t.Fatalf("covered package: %v", err)
	}
	if got, err := readBaseline(baseline); err != nil || got["internal/config"] != 100 {
		t.Fatalf("baseline after -update = %v, %v", got, err)
	}
}

func TestReadBaselineRejectsInvalid(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	if err := os.WriteFile(path, []byte("[1]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBaseline(path); !errors.Is(err, ErrBaselineInvalid) {
		t.Fatalf("err = %v, want ErrBaselineInvalid", err)
	}
	if _, err := readBaseline(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing baseline must be an error")
	}
}
