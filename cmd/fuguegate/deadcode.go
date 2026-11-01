package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrDeadCode reports functions that are unreachable from cmd/* and tests.
var ErrDeadCode = errors.New("unreachable functions found")

func runDeadcode(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("deadcode")
	dir := fs.String("dir", ".", "module directory to analyze")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	bin, err := toolPath(ctx, "deadcode")
	if err != nil {
		return err
	}
	out, err := capture(ctx, *dir, bin, append([]string{"-test"}, patterns...)...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("%w:\n%s", ErrDeadCode, out)
	}
	return say(stdout, "deadcode ok")
}
