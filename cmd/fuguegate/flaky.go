package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
)

// ErrFlaky reports tests that passed in one run and failed in another.
var ErrFlaky = errors.New("flaky tests found")

// testEvent is the part of a `go test -json` event the report reads.
type testEvent struct {
	Action  string
	Package string
	Test    string
	Elapsed *float64
}

type outcomes struct{ passed, failed int }

// runFlaky reads `go test -json` output from -in (the nightly repeated run)
// and prints every test that both passed and failed, so a flake is named
// instead of buried in twenty runs of logs (quality-gates §6).
func runFlaky(_ context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("flaky")
	in := fs.String("in", "", "go test -json output to read")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if *in == "" {
		return fmt.Errorf("%w: flaky needs -in", ErrUsage)
	}
	file, err := os.Open(*in)
	if err != nil {
		return fmt.Errorf("open %s: %w", *in, err)
	}
	tally, readErr := tallyOutcomes(file)
	if closeErr := file.Close(); readErr != nil || closeErr != nil {
		return fmt.Errorf("read %s: %w", *in, errors.Join(readErr, closeErr))
	}
	flaky := 0
	for _, name := range slices.Sorted(maps.Keys(tally)) {
		if o := tally[name]; o.passed > 0 && o.failed > 0 {
			flaky++
			if err := say(stdout, "FLAKY %s: passed %d, failed %d", name, o.passed, o.failed); err != nil {
				return err
			}
		}
	}
	if flaky > 0 {
		return fmt.Errorf("%w: %d", ErrFlaky, flaky)
	}
	return say(stdout, "no test both passed and failed")
}

// tallyOutcomes counts the final pass and fail of every test.
func tallyOutcomes(r io.Reader) (map[string]outcomes, error) {
	tally := map[string]outcomes{}
	err := eachEvent(r, func(ev testEvent) {
		if ev.Test == "" {
			return
		}
		name := ev.Package + "." + ev.Test
		o := tally[name]
		switch ev.Action {
		case "pass":
			o.passed++
		case "fail":
			o.failed++
		}
		tally[name] = o
	})
	return tally, err
}

// eachEvent calls fn for every `go test -json` event in r. Lines that are not
// JSON events (build output) are ignored.
func eachEvent(r io.Reader, fn func(testEvent)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var ev testEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			fn(ev)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan test log: %w", err)
	}
	return nil
}
