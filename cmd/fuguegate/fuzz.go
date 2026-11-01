package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

type fuzzTarget struct {
	pkg  string
	name string
}

func runFuzz(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("fuzz")
	dir := fs.String("dir", ".", "module directory")
	each := fs.Duration("time", 20*time.Second, "fuzzing time per target")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	out, err := capture(ctx, *dir, "go", append([]string{"test", "-run", "^$", "-list", "^Fuzz"}, patterns...)...)
	if err != nil {
		return err
	}
	targets := parseFuzzList(out)
	for _, t := range targets {
		if err := say(stdout, "fuzzing %s %s for %s", t.pkg, t.name, *each); err != nil {
			return err
		}
		if _, err := capture(ctx, *dir, "go", "test", "-run", "^$", "-fuzz", "^"+t.name+"$", "-fuzztime", each.String(), t.pkg); err != nil {
			return err
		}
	}
	return say(stdout, "fuzz ok: %d targets", len(targets))
}

// parseFuzzList reads `go test -list` output: test names, then an "ok <pkg>"
// line that closes each package.
func parseFuzzList(out string) []fuzzTarget {
	var targets []fuzzTarget
	var pending []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 1 && strings.HasPrefix(fields[0], "Fuzz"):
			pending = append(pending, fields[0])
		case len(fields) >= 2 && fields[0] == "ok":
			for _, name := range pending {
				targets = append(targets, fuzzTarget{pkg: fields[1], name: name})
			}
			pending = nil
		}
	}
	return targets
}
