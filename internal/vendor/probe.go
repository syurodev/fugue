package vendor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	// DefaultProbeTimeout bounds the version command when the caller's context
	// carries no deadline of its own.
	DefaultProbeTimeout = 10 * time.Second

	// VersionOutputLimit caps the version command's stdout: a version line is
	// a few dozen bytes, so anything past the cap is not a version.
	VersionOutputLimit = 4 << 10

	// ProbeWaitDelay is how long Wait lets the output pipe drain after the
	// process was killed, so a grandchild holding the pipe cannot hang it.
	ProbeWaitDelay = time.Second
)

// ResolveBinary turns a CLI binary name or path into the absolute path the
// probe may run. A name that resolves into the current directory, or any
// non-absolute result, is refused: a workspace must not be able to plant a
// vendor binary that Fugue executes.
func ResolveBinary(vendorName, binary string) (string, error) {
	if binary == "" {
		return "", errcode.New(errcode.InvalidArgument, vendorName+" binary is empty")
	}
	if strings.ContainsRune(binary, filepath.Separator) && !filepath.IsAbs(binary) {
		return "", errcode.New(errcode.InvalidArgument, vendorName+" binary must be an absolute path")
	}
	path, err := exec.LookPath(binary)
	switch {
	case errors.Is(err, exec.ErrDot):
		return "", errcode.Wrap(errcode.PermissionDenied, vendorName+" binary resolves inside the current directory", err)
	case err != nil:
		return "", errcode.Wrap(errcode.NotFound, vendorName+" binary not found", err)
	case !filepath.IsAbs(path):
		return "", errcode.New(errcode.InvalidArgument, vendorName+" binary must resolve to an absolute path")
	}
	return path, nil
}

// CheckAbsoluteBinary verifies that an explicit absolute binary path exists.
func CheckAbsoluteBinary(vendorName, path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errcode.Wrap(errcode.NotFound, vendorName+" binary not found", err)
		}
		if errors.Is(err, os.ErrPermission) {
			return "", errcode.Wrap(errcode.PermissionDenied, vendorName+" binary permission denied", err)
		}
		return "", errcode.Wrap(errcode.Internal, "stat "+vendorName+" binary", err)
	}
	return path, nil
}

// RunVersionCommand runs `<path> --version` with the filtered environment
// under the caller's context (applying timeout when the context has no
// deadline). A context that ends first is a typed error; output past the cap,
// a failed start, or a non-zero exit leave the version unknown ("", nil).
func RunVersionCommand(ctx context.Context, vendorName, path string, env []string, timeout time.Duration) (string, error) {
	res, err := runBounded(ctx, vendorName, path, []string{"--version"}, env, timeout, VersionOutputLimit, "version probe")
	if err != nil {
		return "", err
	}
	if !res.Ran || res.ExitCode != 0 {
		return "", nil
	}
	return res.Stdout, nil
}

// StatusOutputLimit caps each stream of a login status command. The status of
// a CLI is a handful of short fields; anything past the cap is not a status.
const StatusOutputLimit = 16 << 10

// CommandResult is what a bounded one-shot command printed and how it ended.
// Ran is false when the process did not start, was cut off by the output cap,
// or ended by a signal: the exit code is then meaningless.
type CommandResult struct {
	Stdout, Stderr string
	ExitCode       int
	Ran            bool
}

// RunStatusCommand runs `<path> <args...>` once under the caller's context
// with the filtered environment and the same bounds as RunVersionCommand. It
// is for the CLI's own status commands (VR-03): the host reads what the CLI
// prints, never a credential file. Output is untrusted data.
func RunStatusCommand(ctx context.Context, vendorName, path string, args, env []string, timeout time.Duration) (CommandResult, error) {
	return runBounded(ctx, vendorName, path, args, env, timeout, StatusOutputLimit, "status probe")
}

func runBounded(ctx context.Context, vendorName, path string, args, env []string, timeout time.Duration, limit int, what string) (CommandResult, error) {
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	out := NewCappedBuffer(limit, stop)
	errOut := NewCappedBuffer(limit, stop)
	// The path comes from ResolveBinary or an explicit absolute path, and args
	// are literals fixed by the adapter (never client input), so they are
	// appended to the command after it is built.
	cmd := exec.CommandContext(runCtx, path)
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = env
	cmd.Stdout = out
	cmd.Stderr = errOut
	cmd.WaitDelay = ProbeWaitDelay
	runErr := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return CommandResult{}, errcode.Wrap(errcode.DeadlineExceeded, vendorName+" "+what+" timed out", ctxErr)
		}
		return CommandResult{}, errcode.Wrap(errcode.Canceled, vendorName+" "+what+" was canceled", ctxErr)
	}
	if out.Overflowed() || errOut.Overflowed() {
		return CommandResult{}, nil
	}
	res := CommandResult{Stdout: out.String(), Stderr: errOut.String(), Ran: runErr == nil}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() >= 0 {
		res.ExitCode, res.Ran = exitErr.ExitCode(), true
	}
	return res, nil
}

// CappedBuffer keeps at most limit bytes and discards the rest, reporting
// every write as accepted so the child never blocks on a full pipe. The first
// write past the limit marks the buffer overflowed and calls onOverflow, which
// stops the child instead of draining an endless stream.
type CappedBuffer struct {
	buf        bytes.Buffer
	limit      int
	overflowed bool
	onOverflow func()
}

// NewCappedBuffer constructs a CappedBuffer with the given byte limit and
// overflow callback.
func NewCappedBuffer(limit int, onOverflow func()) *CappedBuffer {
	return &CappedBuffer{limit: limit, onOverflow: onOverflow}
}

// Write implements io.Writer.
func (c *CappedBuffer) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()
	if len(p) <= room {
		c.buf.Write(p)
		return len(p), nil
	}
	if room > 0 {
		c.buf.Write(p[:room])
	}
	if !c.overflowed {
		c.overflowed = true
		if c.onOverflow != nil {
			c.onOverflow()
		}
	}
	return len(p), nil
}

// String returns the retained prefix.
func (c *CappedBuffer) String() string { return c.buf.String() }

// Overflowed reports whether a write exceeded the buffer limit.
func (c *CappedBuffer) Overflowed() bool { return c.overflowed }

// LoginStatusTimeout bounds one login status command, so a CLI that stalls on
// the network leaves the login unknown instead of failing the whole probe.
const LoginStatusTimeout = 5 * time.Second

// ProbeLogin runs the CLI's own login status command and lets classify turn
// what it printed into a LoginState (VR-03: the host asks the CLI, it never
// opens a credential file). Anything the CLI does not state clearly is
// LoginUnknown, including a timeout of this command. Only the caller's own
// context ending is an error.
func ProbeLogin(ctx context.Context, vendorName, path string, args, env []string, classify func(*CommandResult) LoginState) (LoginState, error) {
	sub, cancel := context.WithTimeout(ctx, LoginStatusTimeout)
	defer cancel()
	res, err := RunStatusCommand(sub, vendorName, path, args, env, LoginStatusTimeout)
	if err != nil {
		if ctx.Err() != nil {
			return LoginUnknown, err
		}
		return LoginUnknown, nil
	}
	if !res.Ran {
		return LoginUnknown, nil
	}
	return classify(&res), nil
}
