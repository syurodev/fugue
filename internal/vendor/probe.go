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
	out := NewCappedBuffer(VersionOutputLimit, stop)
	cmd := exec.CommandContext(runCtx, path, "--version")
	cmd.Env = env
	cmd.Stdout = out
	cmd.WaitDelay = ProbeWaitDelay
	runErr := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return "", errcode.Wrap(errcode.DeadlineExceeded, vendorName+" version probe timed out", ctxErr)
		}
		return "", errcode.Wrap(errcode.Canceled, vendorName+" version probe was canceled", ctxErr)
	}
	if runErr != nil || out.Overflowed() {
		return "", nil
	}
	return out.String(), nil
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
