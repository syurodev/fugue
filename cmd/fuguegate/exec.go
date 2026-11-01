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

// runCommand executes a program in dir and returns stdout and stderr separately.
func runCommand(ctx context.Context, dir, name string, args ...string) (stdout, stderr string, err error) {
	//nolint:gosec // G204: name is a fixed program or a tool path from `go tool -n`; args come from this module's own package listing.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out, errs bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errs
	err = cmd.Run()
	return out.String(), errs.String(), err
}

// capture runs a program in dir and returns its combined output. A failure
// carries the output so the gate explains itself.
func capture(ctx context.Context, dir, name string, args ...string) (string, error) {
	stdout, stderr, err := runCommand(ctx, dir, name, args...)
	combined := stdout + stderr
	if err != nil {
		return combined, fmt.Errorf("%w: %s %s: %w\n%s", ErrCommand, name, strings.Join(args, " "), err, combined)
	}
	return combined, nil
}

// toolPath resolves a `tool` directive of go.mod to its built binary so it can
// run in another module's directory. Only stdout is the path: on a cold module
// cache the go command reports its downloads on stderr.
func toolPath(ctx context.Context, tool string) (string, error) {
	stdout, stderr, err := runCommand(ctx, ".", "go", "tool", "-n", tool)
	if err != nil {
		return "", fmt.Errorf("%w: go tool -n %s: %w\n%s", ErrCommand, tool, err, stderr)
	}
	return strings.TrimSpace(stdout), nil
}
