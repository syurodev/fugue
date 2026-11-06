package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Re-exec entries for the exclusive tests: production dispatches these in
	// main, which a test binary never calls. flockwait keeps the default
	// signal disposition on purpose, so neither entry installs handlers.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case exclusiveCommand:
			ctx, stop := signalContext()
			code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
			stop()
			os.Exit(code)
		case "flockwait":
			err := runFlockWait(os.Args[2:], os.Stdout)
			if err == nil {
				os.Exit(0)
			}
			if _, werr := fmt.Fprintln(os.Stderr, "fuguegate:", err); werr != nil {
				os.Exit(exitOutputError)
			}
			os.Exit(exitFailed)
		}
	}
	goleak.VerifyTestMain(m)
}

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := dispatch(t.Context(), args, &out)
	return out.String(), err
}

// Each gate has a fixture that violates it and a twin that passes: the pair
// shows the gate fires on the violation and not on clean code.
func TestDeadcodeGate(t *testing.T) {
	t.Parallel()
	if _, err := execute(t, "deadcode", "-dir", "testdata/deadcode-bad", "./..."); !errors.Is(err, ErrDeadCode) {
		t.Fatalf("violating fixture: err = %v, want ErrDeadCode", err)
	}
	if _, err := execute(t, "deadcode", "-dir", "testdata/deadcode-good", "./..."); err != nil {
		t.Fatalf("clean fixture: %v", err)
	}
}

func TestFuzzGate(t *testing.T) {
	t.Parallel()
	args := []string{"fuzz", "-time", "1s"}
	if _, err := execute(t, append(args, "-dir", "testdata/fuzz-bad", "./...")...); !errors.Is(err, ErrCommand) {
		t.Fatalf("violating fixture: err = %v, want ErrCommand", err)
	}
	out, err := execute(t, append(args, "-dir", "testdata/fuzz-good", "./...")...)
	if err != nil {
		t.Fatalf("clean fixture: %v", err)
	}
	if !strings.Contains(out, "1 targets") {
		t.Fatalf("clean fixture ran no fuzz target: %q", out)
	}
}

func TestMutationGate(t *testing.T) {
	t.Parallel()
	if _, err := execute(t, "mutation", "-dir", "testdata/mutation-bad", "-packages", "."); !errors.Is(err, ErrMutation) {
		t.Fatalf("surviving mutants: err = %v, want ErrMutation", err)
	}
	if _, err := execute(t, "mutation", "-dir", "testdata/mutation-good", "-packages", "."); err != nil {
		t.Fatalf("killed mutants: %v", err)
	}
}

func TestChangedGuarded(t *testing.T) {
	t.Parallel()
	got := changedGuarded([]string{
		"internal/kernel/state.go", "internal/kernel/state_test.go", "internal/vault/v.go",
		"internal/config/c.go", "docs/roadmap.md", "internal/kernel/README.md", "",
	})
	want := []string{pkgKernel, "internal/vault"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("changedGuarded mismatch (-want +got):\n%s", diff)
	}
}

func TestParseFuzzList(t *testing.T) {
	t.Parallel()
	got := parseFuzzList("FuzzA\nFuzzB\nok  \texample.com/p\t0.01s\nok  \texample.com/q\t0.01s\n")
	want := []fuzzTarget{{pkg: "example.com/p", name: "FuzzA"}, {pkg: "example.com/p", name: "FuzzB"}}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(fuzzTarget{})); diff != "" {
		t.Fatalf("parseFuzzList mismatch (-want +got):\n%s", diff)
	}
}

func TestUsage(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"nope"}, {"coverage", "-nope"}} {
		if _, err := execute(t, args...); !errors.Is(err, ErrUsage) {
			t.Fatalf("args %v: err = %v, want ErrUsage", args, err)
		}
	}
}

func TestMutationScore(t *testing.T) {
	t.Parallel()
	const summary = "Killed: %d, Lived: %d, Not covered: %d\nTimed out: %d, Not viable: 0, Skipped: 0\n"
	cases := []struct {
		killed, lived, uncovered, timedOut int
		want                               float64
	}{
		{8, 2, 0, 0, 80},
		{17, 0, 2, 1, 85},
		{0, 0, 0, 0, 100},
		{0, 2, 0, 0, 0},
		{0, 0, 0, 3, 0},
	}
	for _, c := range cases {
		got, err := mutationScore(fmt.Sprintf(summary, c.killed, c.lived, c.uncovered, c.timedOut))
		if err != nil || got != c.want {
			t.Errorf("%+v: score = %v, %v", c, got, err)
		}
	}
	if _, err := mutationScore("garbage"); !errors.Is(err, ErrMutationOutput) {
		t.Fatalf("err = %v, want ErrMutationOutput", err)
	}
}
