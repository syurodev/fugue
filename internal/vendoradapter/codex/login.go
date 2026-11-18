package codex

// The login probe: `codex login status`, the CLI's own status command
// (VR-03). Logged in is a zero exit; logged out is exit 1 with the CLI's
// "Not logged in" line. The host never opens the CLI's credential store, and
// the text the CLI prints (which can name the login method) is not kept.

import (
	"context"
	"strings"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

const notLoggedInText = "Not logged in"

func loginStatusArgs() []string { return []string{"login", "status"} }

func (a *Adapter) probeLogin(ctx context.Context, path string, home vendor.AccountHome) (vendor.LoginState, error) {
	login, err := vendor.ProbeLogin(ctx, DefaultBinary, path, loginStatusArgs(), ProbeEnv(a.HostEnv, home), classifyLogin)
	if err != nil {
		return login, errcode.Wrap(errcode.CodeOf(err), "run codex login status command", err)
	}
	return login, nil
}

func classifyLogin(res *vendor.CommandResult) vendor.LoginState {
	switch {
	case res.ExitCode == 0:
		return vendor.LoginActive
	case strings.Contains(res.Stderr, notLoggedInText) || strings.Contains(res.Stdout, notLoggedInText):
		return vendor.LoginMissing
	}
	return vendor.LoginUnknown
}

// The login command (F9.10b, VR-03): `codex login --device-auth`, the CLI's
// own device flow (Q-112). It prints the device URL and one-time code on
// stdout, takes no stdin, needs no terminal and the code expires in fifteen
// minutes. The default `codex login` flow (a browser plus a local callback
// server) is never run by Fugue: it opens the user's browser and, when that
// browser already carries an OpenAI session, completes a login without the
// user's involvement.

// LoginCommand implements vendor.LoginStarter.
func (a *Adapter) LoginCommand() vendor.LoginCommand { return vendor.LoginCommand{} }

// StartLogin implements vendor.LoginStarter.
func (a *Adapter) StartLogin(ctx context.Context, home vendor.AccountHome) (vendor.LoginRun, error) {
	path, err := vendor.ResolveBinary(DefaultBinary, a.Binary)
	if err != nil {
		return nil, errcode.Wrap(errcode.CodeOf(err), "resolve codex binary", err)
	}
	clk := a.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	idGen := a.IDs
	if idGen == nil {
		idGen = ids.NewUUIDv7(clk, ids.CryptoRandom{})
	}
	scope, err := idGen.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "allocate a codex login identity", err)
	}
	probe := a.SystemdProbe
	if probe == nil {
		probe = process.NewSystemdProbe()
	}
	run, err := vendor.StartLoginProcess(ctx, &vendor.LoginOptions{
		Name:          DefaultBinary,
		Path:          path,
		Args:          loginArgs(),
		Env:           ProbeEnv(a.HostEnv, home),
		SessionID:     scope,
		XDGRuntimeDir: a.XDGRuntimeDir,
		Probe:         probe,
		Clock:         clk,
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.CodeOf(err), "run codex login command", err)
	}
	return run, nil
}

// loginArgs is the CLI's own device login command; never the default browser
// flow and never --with-api-key or --with-access-token, which would ask the
// host to carry a credential (VR-03).
func loginArgs() []string { return []string{"login", "--device-auth"} }
