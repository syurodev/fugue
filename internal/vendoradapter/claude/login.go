package claude

// The login probe: `claude auth status --json`, the CLI's own status command
// (VR-03). The host reads one boolean from what the CLI prints and drops the
// rest of the document (account email, organization, paths): none of it is
// stored, logged or sent to a client.

import (
	"context"
	"encoding/json"

	"github.com/syurodev/fugue/internal/platform/errcode"
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
