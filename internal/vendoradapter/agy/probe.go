package agy

// The adapter entry point and the probe: run `agy --version` once under the
// §8 environment allowlist, resolve from PATH only (never inside cwd), classify
// the version against pinned 1.5.10, and declare the F9.8a capability contract.
// Quota stays "unknown" (OR-09) and credentials are never read (VR-E09).

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
	// DefaultBinary is the agy CLI name on PATH.
	DefaultBinary = "agy"

	// DefaultProbeTimeout bounds the version command when the caller's context
	// carries no deadline of its own.
	DefaultProbeTimeout = vendor.DefaultProbeTimeout

	// DefaultClientVersion is reported in ACP initialize clientInfo.version when
	// the caller does not inject the binary build version.
	DefaultClientVersion = "dev"
)

// Adapter runs the Antigravity CLI (`agy acp`). Binary defaults to DefaultBinary;
// HostEnv is the caller's environment snapshot, filtered through the §8 allowlist
// before any child process sees it.
type Adapter struct {
	Binary        string
	HostEnv       []string
	ProbeTimeout  time.Duration
	Clock         clock.Clock
	IDs           ids.Generator
	SystemdProbe  *process.SystemdProbe
	OnEscalation  func(string)
	XDGRuntimeDir string
	ClientVersion string
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

// WithBinary overrides the agy binary name or path.
func WithBinary(binary string) Option {
	return func(a *Adapter) { a.Binary = binary }
}

// WithProbeTimeout overrides the probe execution timeout.
func WithProbeTimeout(d time.Duration) Option {
	return func(a *Adapter) { a.ProbeTimeout = d }
}

// WithClientVersion injects the binary build version reported in ACP initialize.
func WithClientVersion(version string) Option {
	return func(a *Adapter) { a.ClientVersion = version }
}

// NewAdapter builds an adapter with the default binary, the host environment
// snapshot supplied by the composition root, and optional configuration.
func NewAdapter(hostEnv []string, opts ...Option) *Adapter {
	a := &Adapter{Binary: DefaultBinary, HostEnv: hostEnv}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Probe runs `agy --version` once and reports the version verdict, login state
// ("unknown"), and declared capability. Credentials are never read (VR-E09).
func (a *Adapter) Probe(ctx context.Context, home vendor.AccountHome) (vendor.Probe, error) {
	path, err := vendor.ResolveBinary(DefaultBinary, a.Binary)
	if err != nil {
		return vendor.Probe{}, errcode.Wrap(errcode.CodeOf(err), "resolve agy binary", err)
	}
	output, err := vendor.RunVersionCommand(ctx, DefaultBinary, path, ProbeEnv(a.HostEnv, home), a.ProbeTimeout)
	if err != nil {
		return vendor.Probe{}, errcode.Wrap(errcode.CodeOf(err), "run agy version command", err)
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

// ProbeEnv filters the host environment through the §8 allowlist for a
// one-shot probe: the standard allowlist, TERM forced to dumb, and HOME/config
// override when the account uses a separate config directory.
func ProbeEnv(hostEnv []string, home vendor.AccountHome) []string {
	return filteredEnv(hostEnv, home, nil)
}

func filteredEnv(hostEnv []string, home vendor.AccountHome, extras []string) []string {
	vendorHome := map[string]string{}
	if home.ConfigDir != "" {
		vendorHome["HOME"] = home.ConfigDir
	}
	return process.FilterEnv(hostEnv, process.DefaultAllowlist(), vendorHome, extras)
}

// Capability is what the Antigravity adapter declares (vendor-runtime §6).
func Capability() vendor.Capability {
	return vendor.Capability{
		PerRequestApproval:  true,
		BlockAllToolCalls:   false,
		Interrupt:           true,
		Steer:               false,
		Resume:              true,
		PartialDeltas:       true,
		VendorSlashCommands: true,
		DirectQuota:         false,
		ModelList:           true,
	}
}
