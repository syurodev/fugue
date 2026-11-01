package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrCommand reports an external command that exited unsuccessfully.
var ErrCommand = errors.New("command failed")

// capture runs a program in dir and returns its combined output. A failure
// carries the output so the gate explains itself.
func capture(ctx context.Context, dir, name string, args ...string) (string, error) {
	//nolint:gosec // G204: name is a fixed program or a tool path from `go tool -n`; args come from this module's own package listing.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%w: %s %s: %w\n%s", ErrCommand, name, strings.Join(args, " "), err, out.String())
	}
	return out.String(), nil
}

// toolPath resolves a `tool` directive of go.mod to its built binary so it can
// run in another module's directory.
func toolPath(ctx context.Context, tool string) (string, error) {
	out, err := capture(ctx, ".", "go", "tool", "-n", tool)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
