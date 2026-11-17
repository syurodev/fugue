package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Every test above the budget is reported slowest first, with subtests and
// repeated runs named; a package result and a fast test are not.
func TestSlowtestsReportsEveryTestAboveTheBudget(t *testing.T) {
	t.Parallel()
	log := `{"Action":"pass","Package":"p","Test":"TestFast","Elapsed":0.2}
{"Action":"pass","Package":"p","Test":"TestSlow","Elapsed":12.5}
{"Action":"fail","Package":"q","Test":"TestSlowest/sub","Elapsed":90}
{"Action":"run","Package":"p","Test":"TestSlow"}
{"Action":"pass","Package":"p","Elapsed":400}
not json
`
	path := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, slowtestsCommand, "-json", path, "-max", "10s")
	if !errors.Is(err, ErrSlowTests) {
		t.Fatalf("err = %v, want ErrSlowTests", err)
	}
	want := "SLOW q.TestSlowest/sub: 1m30s (budget 10s)\nSLOW p.TestSlow: 12.5s (budget 10s)\n"
	if diff := cmp.Diff(want, out); diff != "" {
		t.Errorf("report (-want +got):\n%s", diff)
	}
	if out, err := execute(t, slowtestsCommand, "-json", path, "-max", "5m"); err != nil || out != "no test ran longer than 5m0s\n" {
		t.Errorf("within budget: out %q, err %v", out, err)
	}
	for _, args := range [][]string{{slowtestsCommand}, {slowtestsCommand, "-json", path}, {slowtestsCommand, "-json", path, "-max", "0s"}} {
		if _, err := execute(t, args...); !errors.Is(err, ErrUsage) {
			t.Errorf("%v: err = %v, want ErrUsage", args, err)
		}
	}
	if _, err := execute(t, slowtestsCommand, "-json", filepath.Join(t.TempDir(), "missing.json"), "-max", "1s"); err == nil {
		t.Error("a missing log must fail")
	}
}
