package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// ErrCommand reports an external command that exited unsuccessfully.
var ErrCommand = errors.New("command failed")

// childStreams carries the stdio a child command gets; the zero value matches
// exec.Cmd's zero stdio, which reads no stdin and collects both outputs.
type childStreams struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// newCommand is the one constructor every child command of fuguegate goes
// through, so the one G204 exception lives here: the name is a fixed program,
// a `go tool -n` path, or exclusive's own command after --, and the arguments
// come from this module's own package listing, the test fixtures, or the
// caller's own command line.
func newCommand(ctx context.Context, dir string, streams childStreams, name string, args ...string) *exec.Cmd {
	//nolint:gosec // G204: see the function comment; the inputs are fuguegate's own.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = streams.stdin, streams.stdout, streams.stderr
	return cmd
}

// runCommand executes a program in dir and returns stdout and stderr separately.
func runCommand(ctx context.Context, dir, name string, args ...string) (stdout, stderr string, err error) {
	var out, errs bytes.Buffer
	err = newCommand(ctx, dir, childStreams{stdout: &out, stderr: &errs}, name, args...).Run()
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
