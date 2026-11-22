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
		Label:    "Approvals",
		Selected: s.hasSlotKind(KindInbox),
	}
	if count := s.inbox.PendingCount(); count > 0 {
		props.StateLabel = fmt.Sprintf("%d pending", count)
		props.StateTone = kit.ToneWarning
	} else {
		props.Trailing = "0"
	}
	dims, ev := s.inboxRow.Layout(env, gtx, props)
	if ev.Clicked || ev.Opened {
		s.toggleCompanion(KindInbox)
	}
	return dims
}

func (s *screens) layoutAccountsRow(env *kit.Env, gtx *layout.Context) layout.Dimensions {
	props := &kit.ListRowProps{
		Label:    "Accounts",
		Selected: s.hasSlotKind(KindAccounts),
	}
	switch {
	case !s.accounts.HasSnapshot():
		props.Trailing = "unknown"
	case s.accounts.NeedsAttentionCount() > 0:
		props.StateLabel = formatAlertCount(s.accounts.NeedsAttentionCount())
		props.StateTone = kit.ToneWarning
	default:
		props.Trailing = "ok"
	}
	dims, ev := s.accountsRow.Layout(env, gtx, props)
	if ev.Clicked || ev.Opened {
		s.toggleCompanion(KindAccounts)
	}
	return dims
}

func (s *screens) layoutSettingsRow(env *kit.Env, gtx *layout.Context) layout.Dimensions {
	props := &kit.ListRowProps{
		Label:    "Settings",
		Selected: s.hasSlotKind(KindSettings),
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
		Label:    "Secrets",
		Selected: s.hasSlotKind(KindSecrets),
	}
	switch {
	case s.vault.Locked():
		props.StateLabel = "locked"
		props.StateTone = kit.ToneWarning
	case s.secrets.HasSnapshot():
		props.Trailing = strconv.Itoa(len(s.secrets.All()))
	}
	dims, ev := s.secretsRow.Layout(env, gtx, props)
	if ev.Clicked || ev.Opened {
		s.toggleCompanion(KindSecrets)
	}
	return dims
}
