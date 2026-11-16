package claude

// The adapter entry point and the probe: run the CLI's own --version once
// under the §8 environment allowlist, classify the version against the tested
// range, and report the capability contract. The probe never reads vendor
// credential files (VR-03) and never guesses a login state (VR-08).

import (
	"context"
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
	DefaultProbeTimeout = vendor.DefaultProbeTimeout
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

// Probe runs `claude --version` and `claude auth status` once each and reports
// the version verdict, the login state the CLI states and the capability.
// Fugue does not read the CLI's credential store (VR-03).
func (a *Adapter) Probe(ctx context.Context, home vendor.AccountHome) (vendor.Probe, error) {
	path, err := vendor.ResolveBinary(DefaultBinary, a.Binary)
	if err != nil {
		return vendor.Probe{}, errcode.Wrap(errcode.CodeOf(err), "resolve claude binary", err)
	}
	output, err := vendor.RunVersionCommand(ctx, DefaultBinary, path, ProbeEnv(a.HostEnv, home), a.ProbeTimeout)
	if err != nil {
		return vendor.Probe{}, errcode.Wrap(errcode.CodeOf(err), "run claude version command", err)
	}
	version, tested, warning := versionVerdict(output)
	login, err := a.probeLogin(ctx, path, home)
	if err != nil {
		return vendor.Probe{}, err
	}
	return vendor.Probe{
		Path:           path,
		Version:        version,
		VersionTested:  tested,
		VersionWarning: warning,
		Login:          login,
		Capability:     Capability(),
	}, nil
}

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

// Capability is what the Claude Code adapter declares (vendor-runtime §6).
// Blocking every tool call is the stage-B strict profile; the CLI offers no
// model list.
func Capability() vendor.Capability {
	return vendor.Capability{
		PerRequestApproval:  true,
		Interrupt:           true,
		Steer:               true,
		Resume:              true,
		PartialDeltas:       true,
		VendorSlashCommands: true,
		DirectQuota:         true,
	}
}
