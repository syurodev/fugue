package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"
)

// slowtestsCommand is the subcommand name.
const slowtestsCommand = "slowtests"

// ErrSlowTests reports tests that ran longer than the per-test budget.
var ErrSlowTests = errors.New("tests above the time budget")

// slowTest is one finished test with its elapsed time.
type slowTest struct {
	name    string
	elapsed time.Duration
}

// runSlowTests reads `go test -json` output from -json and prints every test
// whose elapsed time is above -max, slowest first, so the cost of a suite is
// visible per test instead of only per package (quality-gates §6). Elapsed is
// taken from the run that wrote the log; the budget is therefore a report for
// the PR run under -race, never a speed assertion (those live in
// internal/perf).
func runSlowTests(args []string, stdout io.Writer) error {
	fs := newFlags(slowtestsCommand)
	in := fs.String("json", "", "go test -json output to read")
	budget := fs.Duration("max", 0, "budget: report every test that ran longer than this")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if *in == "" || *budget <= 0 {
		return fmt.Errorf("%w: slowtests needs -json and a positive -max", ErrUsage)
	}
	file, err := os.Open(*in)
	if err != nil {
		return fmt.Errorf("open %s: %w", *in, err)
	}
	slow, readErr := overBudget(file, *budget)
	if closeErr := file.Close(); readErr != nil || closeErr != nil {
		return fmt.Errorf("read %s: %w", *in, errors.Join(readErr, closeErr))
	}
	for _, s := range slow {
		if err := say(stdout, "SLOW %s: %s (budget %s)", s.name, s.elapsed, *budget); err != nil {
			return err
		}
	}
	if len(slow) > 0 {
		return fmt.Errorf("%w: %d", ErrSlowTests, len(slow))
	}
	return say(stdout, "no test ran longer than %s", *budget)
}

// overBudget returns the tests whose pass or fail event reports more than max
// seconds, slowest first. A test repeated by -count appears once per run.
func overBudget(r io.Reader, budget time.Duration) ([]slowTest, error) {
	var slow []slowTest
	err := eachEvent(r, func(ev testEvent) {
		if ev.Test == "" || ev.Elapsed == nil || (ev.Action != "pass" && ev.Action != "fail") {
			return
		}
		if d := time.Duration(*ev.Elapsed * float64(time.Second)); d > budget {
			slow = append(slow, slowTest{name: ev.Package + "." + ev.Test, elapsed: d})
		}
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(slow, func(a, b slowTest) int { return int(b.elapsed - a.elapsed) })
	return slow, nil
}
