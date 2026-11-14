package claude

// The adapter entry point and the probe: run the CLI's own --version once
// under the §8 environment allowlist, classify the version against the tested
// range, and report the capability contract. The probe never reads vendor
// credential files (VR-03) and never guesses a login state (VR-08).

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

const (
	// DefaultBinary is the claude CLI name on PATH.
	DefaultBinary = "claude"

	// DefaultProbeTimeout bounds the version command when the caller's context
	// carries no deadline of its own.
	DefaultProbeTimeout = 10 * time.Second

	// versionOutputLimit caps the version command's stdout: a version line is
	// a few dozen bytes, so anything past the cap is not a version.
	versionOutputLimit = 4 << 10

	// probeWaitDelay is how long Wait lets the output pipe drain after the
	// process was killed, so a grandchild holding the pipe cannot hang it.
	probeWaitDelay = time.Second
)

// Adapter runs the Claude Code CLI (F9.5: probe and normalizer; F9.5b adds
// the process session). Binary defaults to DefaultBinary; HostEnv is the
// caller's environment snapshot, filtered through the §8 allowlist before any
// child process sees it. ProbeTimeout bounds the probe when the caller's
// context has no deadline; zero means DefaultProbeTimeout.
type Adapter struct {
	Binary        string
	HostEnv       []string
	ProbeTimeout  time.Duration
	Clock         clock.Clock
	IDs           ids.Generator
	SystemdProbe  *process.SystemdProbe
	OnEscalation  func(string)
	XDGRuntimeDir string
}

// Option configures an Adapter.
type Option func(*Adapter)

// WithClock injects the clock used by sessions.
func WithClock(clk clock.Clock) Option {
	return func(a *Adapter) { a.Clock = clk }
}

// WithIDs injects the turn ID generator used by sessions.
func WithIDs(idGen ids.Generator) Option {
	return func(a *Adapter) { a.IDs = idGen }
}

// WithSystemdProbe injects the systemd probe for process isolation.
func WithSystemdProbe(probe *process.SystemdProbe) Option {
	return func(a *Adapter) { a.SystemdProbe = probe }
}

// WithOnEscalation registers a callback invoked on process stop order escalation.
func WithOnEscalation(fn func(string)) Option {
	return func(a *Adapter) { a.OnEscalation = fn }
}

// WithXDGRuntimeDir sets the runtime directory for systemd containment.
func WithXDGRuntimeDir(dir string) Option {
	return func(a *Adapter) { a.XDGRuntimeDir = dir }
}

// WithBinary overrides the Claude binary name or path.
func WithBinary(binary string) Option {
	return func(a *Adapter) { a.Binary = binary }
}

// WithProbeTimeout overrides the probe execution timeout.
func WithProbeTimeout(d time.Duration) Option {
	return func(a *Adapter) { a.ProbeTimeout = d }
}

// NewAdapter builds an adapter with the default binary, the host
// environment snapshot supplied by the composition root, and optional configuration.
func NewAdapter(hostEnv []string, opts ...Option) *Adapter {
	a := &Adapter{Binary: DefaultBinary, HostEnv: hostEnv}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Probe runs `claude --version` once and reports the version verdict, the
// login state and the capability. Fugue does not read the CLI's credential
// store; the CLI reports login only during a turn, so the state stays unknown
// here and F9.10 owns account discovery.
//
// The binary is resolved once to an absolute path and that path is what runs
// and what the report carries. A name that resolves into the current
// directory, or any non-absolute result, is refused: a workspace must not be
// able to plant the "claude" that Fugue executes.
func (a *Adapter) Probe(ctx context.Context, home vendor.AccountHome) (vendor.Probe, error) {
	path, err := a.resolveBinary()
	if err != nil {
		return vendor.Probe{}, err
	}
	timeout := a.ProbeTimeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	output, err := a.versionOutput(ctx, path, home)
	if err != nil {
		return vendor.Probe{}, err
	}
	version, tested, warning := versionVerdict(output)
	return vendor.Probe{
		Path:           path,
		Version:        version,
		VersionTested:  tested,
		VersionWarning: warning,
		Login:          vendor.LoginUnknown,
		Capability:     Capability(),
	}, nil
}

// resolveBinary turns Binary into the absolute path the probe may run.
func (a *Adapter) resolveBinary() (string, error) {
	if a.Binary == "" {
		return "", errcode.New(errcode.InvalidArgument, "claude binary is empty")
	}
	if strings.ContainsRune(a.Binary, filepath.Separator) && !filepath.IsAbs(a.Binary) {
		return "", errcode.New(errcode.InvalidArgument, "claude binary must be an absolute path")
	}
	path, err := exec.LookPath(a.Binary)
	switch {
	case errors.Is(err, exec.ErrDot):
		return "", errcode.Wrap(errcode.PermissionDenied, "claude binary resolves inside the current directory", err)
	case err != nil:
		return "", errcode.Wrap(errcode.NotFound, "claude binary not found", err)
	case !filepath.IsAbs(path):
		return "", errcode.New(errcode.InvalidArgument, "claude binary must resolve to an absolute path")
	}
	return path, nil
}

// versionOutput runs the one-shot version command with the filtered
// environment under the caller's context. A context that ends first is a
// typed error: the probe did not complete. Output past the cap, a failed
// start and a non-zero exit leave the version unknown, never a guess.
func (a *Adapter) versionOutput(ctx context.Context, path string, home vendor.AccountHome) (string, error) {
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	out := &cappedBuffer{limit: versionOutputLimit, onOverflow: stop}
	cmd := exec.CommandContext(runCtx, path, "--version")
	cmd.Env = ProbeEnv(a.HostEnv, home)
	cmd.Stdout = out
	cmd.WaitDelay = probeWaitDelay
	runErr := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return "", errcode.Wrap(errcode.DeadlineExceeded, "claude version probe timed out", ctxErr)
		}
		return "", errcode.Wrap(errcode.Canceled, "claude version probe was canceled", ctxErr)
	}
	if runErr != nil || out.overflowed {
		return "", nil
	}
	return out.String(), nil
}

// cappedBuffer keeps at most limit bytes and discards the rest, reporting
// every write as accepted so the child never blocks on a full pipe. The first
// write past the limit marks the buffer overflowed and calls onOverflow, which
// stops the child instead of draining an endless stream.
type cappedBuffer struct {
	buf        bytes.Buffer
	limit      int
	overflowed bool
	onOverflow func()
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()
	if len(p) <= room {
		c.buf.Write(p) // a bytes.Buffer write never fails
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

func (c *cappedBuffer) String() string { return c.buf.String() }

// ProbeEnv filters the host environment through the §8 allowlist for a
// one-shot probe: the standard allowlist, TERM forced to dumb, and the vendor
// home override when the account uses a separate config directory. No Fugue
// secret and no other host variable reaches the child.
func ProbeEnv(hostEnv []string, home vendor.AccountHome) []string {
	return filteredEnv(hostEnv, home, nil)
}

func filteredEnv(hostEnv []string, home vendor.AccountHome, extras []string) []string {
	vendorHome := map[string]string{}
	if home.ConfigDir != "" {
		vendorHome["CLAUDE_CONFIG_DIR"] = home.ConfigDir
	}
	return process.FilterEnv(hostEnv, process.DefaultAllowlist(), vendorHome, extras)
}

// Capability is what the F9.5 adapter declares (vendor-runtime §6). Approval
// relay arrives with the MCP prompt tool in F9.6; blocking every tool call is
// the stage-B strict profile; the CLI offers no model list.
func Capability() vendor.Capability {
	return vendor.Capability{
		Interrupt:           true,
		Steer:               true,
		Resume:              true,
		PartialDeltas:       true,
		VendorSlashCommands: true,
		DirectQuota:         true,
	}
}
