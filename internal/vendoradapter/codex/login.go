package codex

// The login probe: `codex login status`, the CLI's own status command
// (VR-03). Logged in is a zero exit; logged out is exit 1 with the CLI's
// "Not logged in" line. The host never opens the CLI's credential store, and
// the text the CLI prints (which can name the login method) is not kept.

import (
	"context"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
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
