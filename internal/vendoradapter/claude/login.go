package claude

// The login probe: `claude auth status --json`, the CLI's own status command
// (VR-03). The host reads one boolean from what the CLI prints and drops the
// rest of the document (account email, organization, paths): none of it is
// stored, logged or sent to a client.

import (
	"context"
	"encoding/json"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vendor"
)

// loginStatusArgs is the status command; --json is also the CLI's default.
func loginStatusArgs() []string { return []string{"auth", "status", "--json"} }

func (a *Adapter) probeLogin(ctx context.Context, path string, home vendor.AccountHome) (vendor.LoginState, error) {
	login, err := vendor.ProbeLogin(ctx, DefaultBinary, path, loginStatusArgs(), ProbeEnv(a.HostEnv, home), classifyLogin)
	if err != nil {
		return login, errcode.Wrap(errcode.CodeOf(err), "run claude login status command", err)
	}
	return login, nil
}

// classifyLogin trusts only a decodable document that carries loggedIn, and
// only a zero exit for logged-in: anything else is unknown.
func classifyLogin(res *vendor.CommandResult) vendor.LoginState {
	var doc struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if json.Unmarshal([]byte(res.Stdout), &doc) != nil || doc.LoggedIn == nil {
		return vendor.LoginUnknown
	}
	switch {
	case !*doc.LoggedIn:
		return vendor.LoginMissing
	case res.ExitCode == 0:
		return vendor.LoginActive
	}
	return vendor.LoginUnknown
}

// The login command (F9.10b, VR-03): `claude auth login`, the CLI's own
// login, default claude.ai flow (Q-112). The CLI prints its OAuth URL and
// opens a browser; the host relays the URL verbatim, opens nothing and
// relies on the browser's localhost callback. The CLI can also finish by
// reading a code pasted into its own stdin — a channel Fugue does not relay
// — so a login still running at the timeout ends needs_terminal: the user
// finishes it in their own terminal.

// LoginCommand implements vendor.LoginStarter.
func (a *Adapter) LoginCommand() vendor.LoginCommand {
	return vendor.LoginCommand{NeedsTerminal: true}
}

// StartLogin implements vendor.LoginStarter.
func (a *Adapter) StartLogin(ctx context.Context, home vendor.AccountHome) (vendor.LoginRun, error) {
	path, err := a.resolveStartBinary(ctx, home)
	if err != nil {
		return nil, err
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
		return nil, errcode.Wrap(errcode.Internal, "allocate a claude login identity", err)
	}
	run, err := vendor.StartLoginProcess(ctx, &vendor.LoginOptions{
		Name:          DefaultBinary,
		Path:          path,
		Args:          loginArgs(),
		Env:           ProbeEnv(a.HostEnv, home),
		SessionID:     scope,
		XDGRuntimeDir: a.XDGRuntimeDir,
		Probe:         a.SystemdProbe,
		Clock:         clk,
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.CodeOf(err), "run claude login command", err)
	}
	return run, nil
}

// loginArgs is the CLI's own login command; never the flags that widen
// permissions (VR-04), which IsDenylistedFlag forbids for profiles anyway.
func loginArgs() []string { return []string{"auth", "login"} }
