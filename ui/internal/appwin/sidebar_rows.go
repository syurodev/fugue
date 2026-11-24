package appwin

import (
	"fmt"
	"strconv"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
)

// The chat sidebar's utility rows (F10.5b, F10.5c, F10.5d): approvals,
// accounts and settings as regular slots — a row opens its kind's slot or
// closes the one that is open, and the selected state reads the grid.

func (s *screens) layoutInboxRow(env *kit.Env, gtx *layout.Context) layout.Dimensions {
	props := &kit.ListRowProps{
		Icon:     kit.IconInbox,
		Label:    "Approvals",
		Selected: s.grid.mainRef().Kind == KindInbox,
	}
	props.StateLabel, props.StateTone = inboxRowState(s.inbox.PendingCount())
	dims, ev := s.inboxRow.Layout(env, gtx, props)
	if ev.Clicked || ev.Opened {
		s.toggleCompanion(KindInbox)
	}
	return dims
}

func (s *screens) layoutAccountsRow(env *kit.Env, gtx *layout.Context) layout.Dimensions {
	props := &kit.ListRowProps{
		Icon:     kit.IconCircleUser,
		Label:    "Accounts",
		Selected: s.grid.mainRef().Kind == KindAccounts,
	}
	props.StateLabel, props.StateTone = accountsRowState(s.accounts.HasSnapshot(), s.accounts.NeedsAttentionCount())
	dims, ev := s.accountsRow.Layout(env, gtx, props)
	if ev.Clicked || ev.Opened {
		s.toggleCompanion(KindAccounts)
	}
	return dims
}

func (s *screens) layoutSettingsRow(env *kit.Env, gtx *layout.Context) layout.Dimensions {
	props := &kit.ListRowProps{
		Icon:     kit.IconSettings,
		Label:    "Settings",
		Selected: s.grid.mainRef().Kind == KindSettings,
	}
	if dirty := len(s.settings.DirtyFields()); dirty > 0 {
		props.StateLabel = fmt.Sprintf("%d unsaved", dirty)
		props.StateTone = kit.ToneInfo
	}
	dims, ev := s.settingsRow.Layout(env, gtx, props)
	if ev.Clicked || ev.Opened {
		s.openSettings()
	}
	return dims
}

func (s *screens) layoutSecretsRow(env *kit.Env, gtx *layout.Context) layout.Dimensions {
	props := &kit.ListRowProps{
		Icon:     kit.IconKeyRound,
		Label:    "Secrets",
		Selected: s.grid.mainRef().Kind == KindSecrets,
	}
	locked := s.vault.Locked()
	if locked {
		props.Icon = kit.IconLock
	}
	props.StateLabel, props.StateTone, props.Badge = secretsRowState(locked, s.secrets.HasSnapshot(), len(s.secrets.All()))
	dims, ev := s.secretsRow.Layout(env, gtx, props)
	if ev.Clicked || ev.Opened {
		s.toggleCompanion(KindSecrets)
	}
	return dims
}

// The utility rows show a trailing value only when it asks for attention or
// carries a count worth reading: a zero, an "ok" or an unknown is noise, so
// it draws nothing.

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
