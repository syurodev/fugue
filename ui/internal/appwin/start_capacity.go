package appwin

import (
	"strconv"

	"github.com/syurodev/fugue/pkg/client"
)

// The start refusal over the session cap (FUG-55, owner decision 2026-11-23):
// a session the user starts past the cap is refused, never queued — only team
// tasks queue. The start dialog stays open with every field intact and says
// what is full and what helps, in its own error line (desktop-app §6: a form
// failure shows in the form).

// capacityMessage words a vendor.capacity refusal from the host's typed
// counts. The app Settings has no control for the cap, so the message names
// the host config key instead of offering an action that cannot help; a host
// that sent no counts gets the same advice without numbers.
func capacityMessage(c client.Capacity) string {
	if c.Limit <= 0 {
		return "The host is at its vendor session limit. Close a session, or raise max_sessions in the host config."
	}
	counts := strconv.Itoa(c.Running) + " of " + strconv.Itoa(c.Limit)
	if c.Scope == client.CapacityScopeAccount {
		return counts + " sessions are running on this vendor account. Close a session, or raise per_account_sessions in the host config."
	}
	return counts + " vendor sessions are running. Close a session, or raise max_sessions in the host config."
}

// startPosted folds a start post's result in: the busy mark clears, a
// failure shows.
func (f *startFlow) startPosted(err error) {
	f.busy = false
	if err == nil {
		return
	}
	if capacity, ok := client.VendorCapacityOf(err); ok {
		f.lastErr, f.refused = capacityMessage(capacity), true
		return
	}
	f.lastErr, f.refused = err.Error(), false
}
