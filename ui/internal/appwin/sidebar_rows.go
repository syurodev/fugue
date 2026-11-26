package appwin

import (
	"fmt"
	"strconv"

	"github.com/syurodev/fugue/ui/internal/kit"
)

// The utility slot state helpers (F10.5b, F10.5c, F10.5d): approvals, accounts
// and secrets badges/status labels, consumed by the Right Stripe (FUG-94) and
// unit tests after removing the utility rows from the Sidebar Island (FUG-93).
//
// The helpers show a trailing value only when it asks for attention or carries
// a count worth reading: a zero, an "ok" or an unknown is noise, so it draws
// nothing.

// inboxRowState is the Approvals row's state: a warning label while
// approvals wait, nothing otherwise.
func inboxRowState(pending int) (string, kit.ListRowTone) {
	if pending > 0 {
		return fmt.Sprintf("%d pending", pending), kit.ToneWarning
	}
	return "", kit.ToneNeutral
}

// accountsRowState is the Accounts row's state: a warning alert count when
// accounts need attention, nothing before the first snapshot or when all is
// well.
func accountsRowState(hasSnapshot bool, attention int) (string, kit.ListRowTone) {
	if hasSnapshot && attention > 0 {
		return formatAlertCount(attention), kit.ToneWarning
	}
	return "", kit.ToneNeutral
}

// vaultLockedLabel is the Secrets row's warning label while the vault is locked.
const vaultLockedLabel = "locked"

// secretsRowState is the Secrets row's state: the warning "locked" label
// while the vault is locked, else a neutral count badge once a snapshot
// exists and holds at least one secret.
func secretsRowState(locked, hasSnapshot bool, count int) (label string, tone kit.ListRowTone, badge string) {
	switch {
	case locked:
		return vaultLockedLabel, kit.ToneWarning, ""
	case hasSnapshot && count > 0:
		return "", kit.ToneNeutral, strconv.Itoa(count)
	}
	return "", kit.ToneNeutral, ""
}

func formatAlertCount(n int) string {
	if n == 1 {
		return "1 alert"
	}
	return fmt.Sprintf("%d alerts", n)
}
