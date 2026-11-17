package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// A test that both passed and failed across the repeated runs is reported; one
// that only ever passed or only ever failed is not a flake.
func TestFlakyReportsTestsThatBothPassedAndFailed(t *testing.T) {
	t.Parallel()
	log := `{"Action":"pass","Package":"p","Test":"TestFlake"}
{"Action":"fail","Package":"p","Test":"TestFlake"}
{"Action":"pass","Package":"p","Test":"TestFlake"}
{"Action":"pass","Package":"p","Test":"TestStable"}
{"Action":"fail","Package":"p","Test":"TestBroken"}
{"Action":"pass","Package":"p"}
build output that is not json
`
	path := filepath.Join(t.TempDir(), "nightly.json")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "flaky", "-in", path)
	if !errors.Is(err, ErrFlaky) {
		t.Fatalf("err = %v, want ErrFlaky", err)
	}
	if diff := cmp.Diff("FLAKY p.TestFlake: passed 2, failed 1\n", out); diff != "" {
		t.Errorf("report (-want +got):\n%s", diff)
	}

	clean := filepath.Join(t.TempDir(), "clean.json")
	if err := os.WriteFile(clean, []byte(`{"Action":"pass","Package":"p","Test":"TestStable"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, "flaky", "-in", clean); err != nil || out != "no test both passed and failed\n" {
		t.Errorf("clean run: out %q, err %v", out, err)
	}
	if _, err := execute(t, "flaky"); !errors.Is(err, ErrUsage) {
		t.Errorf("no input: err = %v, want ErrUsage", err)
	}
	if _, err := execute(t, "flaky", "-in", filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing log must fail")
	}
}
