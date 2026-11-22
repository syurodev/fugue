package appwin

import (
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
)

// The slots' content (desktop-app §5.3, §5.5–5.6): the main slot holds the
// rail item's work view — a session's chat, the team board, a module
// workbench — and every other kind is a regular slot. A slot whose object
// is gone shows the kit EmptyState with its close button instead of
// disappearing (DA-E10), and the screens whose full UI lands in a later
// item render a clean placeholder for now.

// slotIcon is the header icon of one slot kind.
func slotIcon(kind SlotKind) kit.IconName {
	switch kind {
	case KindSession:
		return kit.IconMessageSquare
	case KindTeam:
		return kit.IconUsers
	case KindAgent:
		return kit.IconTerminal
	case KindDiff:
		return kit.IconFileText
	case KindInbox:
		return kit.IconCheck
	case KindAccounts:
		return kit.IconGlobe
	case KindSettings:
		return kit.IconWrench
	case KindSecrets:
		return kit.IconEye
	case KindModule:
		return kit.IconFolder
	default:
		return kit.IconFileText
	}
}

// slotTitleFallback is the header title of a slot the shell cannot name.
const slotTitleFallback = "Slot"

// slotTitle is the header title of one slot: the kind's name, or the
// object's where the screens know one.
func slotTitle(sc *screens, ref SlotRef) string {
	if ref.Kind == KindSession {
		if sess, ok := sc.sidebar.Lookup(ref.ID); ok {
			return sessionName(sess)
		}
		return "Session"
	}
	if ref.Kind == KindDiff && ref.ID != "" {
		return ref.ID
	}
	if ref.Kind == KindModule && ref.ID != "" {
		return "Module: " + ref.ID
	}
	if ref.Kind == SlotKind("") {
		return slotTitleFallback
	}
	return fixedSlotTitle(ref.Kind)
}

// fixedSlotTitle is the fixed header title of the kinds that carry no
// object name.
func fixedSlotTitle(kind SlotKind) string {
	switch kind {
	case KindTeam:
		return "Team board"
	case KindAgent:
		return "Agent detail"
	case KindInbox:
		return "Approvals inbox"
	case KindAccounts:
		return "Accounts & quota"
	case KindSettings:
		return "Settings"
	case KindSecrets:
		return "Secrets"
	case KindSession, KindDiff, KindModule, SlotKind(""):
		return slotTitleFallback
	}
	return slotTitleFallback
}

// layoutSlotBody draws one slot's body: the main slot's work view for the
// rail, the views' own layouts for the regular kinds, and the EmptyStates
// for missing objects and screens still to land.
func (s *screens) layoutSlotBody(gtx *layout.Context, env *kit.Env, p Pos, ref SlotRef) {
	if !isMainPos(p) && s.missingTarget(ref) {
		s.layoutSlotGone(gtx, env, p)
		return
	}
	if isMainPos(p) {
		s.layoutMainSlot(gtx, env, ref)
		return
	}
	s.layoutRegularSlot(gtx, env, p, ref)
}

// layoutMainSlot draws the rail item's work view: the session chat, the
// team board or a module workbench (§5.3). The team board's Chat/Team
// switch lands with the team screens; until then the main slot's
// non-session kinds render the regular slots' views.
func (s *screens) layoutMainSlot(gtx *layout.Context, env *kit.Env, ref SlotRef) {
	switch ref.Kind {
	case KindSession:
		s.layoutTranscriptFor(gtx, env, ref.ID)
	case KindModule:
		s.layoutSoonSlot(gtx, env, Pos{}, "No modules", "Registered modules open their workbench here, in install order.")
	case KindTeam:
		s.layoutRegularSlot(gtx, env, Pos{}, ref)
	case KindAgent:
		s.layoutRegularSlot(gtx, env, Pos{}, ref)
	case KindDiff:
		s.layoutRegularSlot(gtx, env, Pos{}, ref)
	case KindInbox:
		s.layoutRegularSlot(gtx, env, Pos{}, ref)
	case KindAccounts:
		s.layoutRegularSlot(gtx, env, Pos{}, ref)
	case KindSettings:
		s.layoutRegularSlot(gtx, env, Pos{}, ref)
	case KindSecrets:
		s.layoutRegularSlot(gtx, env, Pos{}, ref)
	case SlotKind(""):
		s.layoutRegularSlot(gtx, env, Pos{}, ref)
	}
}

// layoutRegularSlot draws one regular slot's view by kind, with the
// EmptyStates for objects missing and screens still to land (DA-E10).
func (s *screens) layoutRegularSlot(gtx *layout.Context, env *kit.Env, p Pos, ref SlotRef) {
	switch ref.Kind {
	case KindSession:
		if ref.ID == "" {
			s.layoutSoonSlot(gtx, env, p, "No session", "Pick a session in the sidebar to open it here.")
			return
		}
		s.layoutTranscriptFor(gtx, env, ref.ID)
	case KindDiff:
		s.layoutDiffSlot(gtx, env, p)
	case KindInbox:
		s.inboxView.layout(gtx, env, s)
	case KindAccounts:
		s.accountsView.layout(gtx, env, s)
	case KindSettings:
		s.settingsView.layout(gtx, env, s)
	case KindTeam:
		s.layoutSoonSlot(gtx, env, p, "Team board", "The team board opens here with the team screens.")
	case KindAgent:
		s.layoutSoonSlot(gtx, env, p, "Agent detail", "An agent's transcript, diff and checks open here with the team screens.")
	case KindSecrets:
		s.layoutSoonSlot(gtx, env, p, "Secrets", "The secret vault screen opens here with the secrets item.")
	case KindModule:
		s.layoutModuleSlot(gtx, env, p, ref)
	case SlotKind(""):
		s.layoutSoonSlot(gtx, env, p, "Empty slot", "Nothing is open in this slot.")
	}
}

// missingTarget reports a regular slot whose object the host no longer
// knows: a session that left the list. The slot shows the EmptyState with
// its close button, never vanishing on its own (DA-E10).
func (s *screens) missingTarget(ref SlotRef) bool {
	if ref.Kind != KindSession || ref.ID == "" {
		return false
	}
	if len(s.sidebar.List()) == 0 {
		// The list has not arrived yet; nothing is known to be missing.
		return false
	}
	_, ok := s.sidebar.Lookup(ref.ID)
	return !ok
}

// layoutSessionSlot draws a session slot: the main slot carries the open
// session's full transcript and composer, a regular slot the transcript and
// a composer bound to its own session.
// layoutDiffSlot draws the file and diff viewer's slot; before anything is
// open it shows the invitation (§5.4).
func (s *screens) layoutDiffSlot(gtx *layout.Context, env *kit.Env, p Pos) {
	if !s.viewer.open {
		s.layoutSoonSlot(gtx, env, p, "File viewer", "A file badge or an Edited row opens its view here.")
		return
	}
	s.viewer.layout(gtx, env, s)
}

// layoutModuleSlot draws the modules' slot; stage A registers no modules,
// so the main slot shows the invitation and a regular slot its own empty
// state with a close button (§5.3).
func (s *screens) layoutModuleSlot(gtx *layout.Context, env *kit.Env, p Pos, ref SlotRef) {
	if isMainPos(p) {
		s.layoutSoonSlot(gtx, env, p, "No modules", "Registered modules open their workbench here, in install order.")
		return
	}
	if ref.ID == "" {
		s.layoutSoonSlot(gtx, env, p, "Module", "A module panel opens here when an agent uses the module.")
		return
	}
	s.layoutSoonSlot(gtx, env, p, "Module: "+ref.ID, "The module's panel opens here with its module item.")
}

// layoutSoonSlot is the clean placeholder for a screen that lands in a
// later item, with the slot's close button (§5.3).
func (s *screens) layoutSoonSlot(gtx *layout.Context, env *kit.Env, p Pos, title, desc string) {
	dims, ev, err := s.slotEmpty.Layout(env, gtx, &kit.EmptyStateProps{
		Icon:          slotIcon(refAt(s.grid.doc, p).Kind),
		Title:         title,
		Description:   desc,
		PrimaryAction: closeLabel,
	})
	s.fail(err)
	if ev.PrimaryClicked {
		s.grid.edited(CloseSlot(s.grid.doc, p))
	}
	_ = dims
}

// layoutSlotGone is the missing-object EmptyState: the slot says so and
// offers its close button (DA-E10).
func (s *screens) layoutSlotGone(gtx *layout.Context, env *kit.Env, p Pos) {
	dims, ev, err := s.slotGone.Layout(env, gtx, &kit.EmptyStateProps{
		Icon:          kit.IconHelp,
		Title:         "This object is gone",
		Description:   "The session this slot pointed to no longer exists.",
		PrimaryAction: closeLabel,
	})
	s.fail(err)
	if ev.PrimaryClicked {
		s.grid.edited(CloseSlot(s.grid.doc, p))
	}
	_ = dims
}

// closeLabel names the slots' close action in the EmptyStates.
const closeLabel = "Close slot"
