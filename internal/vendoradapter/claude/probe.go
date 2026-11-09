package claude

// The adapter entry point and the probe: run the CLI's own --version once
// under the §8 environment allowlist, classify the version against the tested
// range, and report the capability contract. The probe never reads vendor
// credential files (VR-03) and never guesses a login state (VR-08).

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

// DefaultBinary is the claude CLI name on PATH.
const DefaultBinary = "claude"

// Adapter runs the Claude Code CLI (F9.5: probe and normalizer; F9.5b adds
// the process session). Binary defaults to DefaultBinary; HostEnv is the
// caller's environment snapshot, filtered through the §8 allowlist before any
// child process sees it.
type Adapter struct {
	Binary  string
	HostEnv []string
}

// NewAdapter builds an adapter with the default binary and the host
// environment snapshot supplied by the composition root.
func NewAdapter(hostEnv []string) *Adapter {
	return &Adapter{Binary: DefaultBinary, HostEnv: hostEnv}
}

// Probe runs `claude --version` once and reports the version verdict, the
// login state and the capability. Fugue does not read the CLI's credential
// store; the CLI reports login only during a turn, so the state stays unknown
// here and F9.10 owns account discovery.
func (a *Adapter) Probe(ctx context.Context, home vendor.AccountHome) (vendor.Probe, error) {
	if a.Binary == "" {
		return vendor.Probe{}, errcode.New(errcode.InvalidArgument, "claude binary is empty")
	}
	if _, err := exec.LookPath(a.Binary); err != nil {
		return vendor.Probe{}, errcode.Wrap(errcode.NotFound, "claude binary not found", err)
	}
	version, tested, warning := versionVerdict(a.versionOutput(home))
	return vendor.Probe{
		Version:        version,
		VersionTested:  tested,
		VersionWarning: warning,
		Login:          vendor.LoginUnknown,
		Capability:     Capability(),
	}, nil
}

// versionOutput runs the one-shot version command with the filtered
// environment; the command is built as an exec.Cmd literal, like
// internal/process spawns, so the binary path is an explicit field, never an
// exec.Command argument. The one-shot needs no context watchdog: it produces
// one short line and exits, and Probe's callers bound the whole probe.
func (a *Adapter) versionOutput(home vendor.AccountHome) string {
	var out bytes.Buffer
	cmd := &exec.Cmd{
		Path:   a.Binary,
		Args:   []string{a.Binary, "--version"},
		Env:    ProbeEnv(a.HostEnv, home),
		Stdout: &out,
	}
	if err := cmd.Start(); err != nil {
		// The binary vanished between LookPath and Start; the version stays
		// unknown and the verdict carries the untested warning, never a guess.
		return ""
	}
	if err := cmd.Wait(); err != nil {
		// The probe command failed; same fail-open-on-data, never a guess.
		return ""
	}
	return out.String()
}

// ProbeEnv filters the host environment through the §8 allowlist for a
// one-shot probe: the standard allowlist, TERM forced to dumb, and the vendor
// home override when the account uses a separate config directory. No Fugue
// secret and no other host variable reaches the child.
func ProbeEnv(hostEnv []string, home vendor.AccountHome) []string {
	vendorHome := map[string]string{}
	if home.ConfigDir != "" {
		vendorHome["CLAUDE_CONFIG_DIR"] = home.ConfigDir
	}
	return process.FilterEnv(hostEnv, process.DefaultAllowlist(), vendorHome, nil)
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
