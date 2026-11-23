package appwin

import (
	"strings"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
)

// hostProblem is the failure UX's account of a connection that is not up
// (desktop-app §6): what happened, why it matters here and the action that
// helps. The vault gate shows it in place of a passphrase form, because the
// window cannot read or change the vault without the host.
type hostProblem struct {
	title  string
	detail string
	// action is the step that fixes it, or says that the app retries itself.
	action string
}

// describeHostProblem maps a not-connected state and the manager's reason to
// the panel text. The reasons are the manager's own error texts, path- and
// credential-free; the checks use the typed errors' messages, not copies.
func describeHostProblem(state hostconn.State, reason string) hostProblem {
	switch {
	case state == hostconn.StateIncompatible:
		return hostProblem{
			title:  "Fugue core and app do not match",
			detail: sentence(reason, "The core and the app run different versions."),
			action: "The vault stays closed until both run the same version.",
		}
	case strings.Contains(reason, hostconn.ErrUnsafeExecutable.Error()):
		return hostProblem{
			title:  "Fugue refused to start its core",
			detail: "The core program, or a folder above it, can be changed by another user or is not owned by you.",
			action: "Install Fugue where only your account can write, then restart the app.",
		}
	case strings.Contains(reason, client.ErrUntrustedHost.Error()):
		return hostProblem{
			title:  "Fugue could not verify its core",
			detail: "The running core's discovery file or process does not belong to your account.",
			action: "Stop the other process or remove the stale discovery file from the data directory, then restart the app.",
		}
	case strings.Contains(reason, hostconn.ErrStartupTimeout.Error()):
		return hostProblem{
			title:  "Fugue core did not start",
			detail: "The core did not answer within ten seconds.",
			action: "Check the data directory, then restart the app.",
		}
	default:
		return hostProblem{
			title:  "Fugue core is not reachable",
			detail: sentence(reason, "The connection to the core is down."),
			action: "Fugue reconnects on its own. The vault opens once the core answers.",
		}
	}
}

// sentence renders a reason as a capitalized sentence, or the fallback when
// the reason is empty.
func sentence(reason, fallback string) string {
	if reason == "" {
		return fallback
	}
	r := []rune(reason)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return strings.TrimRight(string(r), ".") + "."
}
