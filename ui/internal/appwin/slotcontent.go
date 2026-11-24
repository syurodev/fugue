package appwin

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

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
	if s.missingTarget(ref) {
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
		s.layoutSessionWithComposer(gtx, env, ref.ID, true)
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
		s.layoutTeamBoard(gtx, env, teamBoardKey{team: ref.ID, main: isMainPos(p)})
	case KindAgent:
		s.layoutTeamAgent(gtx, env, ref)
	case KindSecrets:
		s.secretsView.layout(gtx, env, s)
	case KindModule:
		s.layoutModuleSlot(gtx, env, p, ref)
	case SlotKind(""):
		s.layoutSoonSlot(gtx, env, p, "Empty slot", "Nothing is open in this slot.")
	}
}

// missingTarget reports a slot whose object the host no longer
// knows: a session that left the list. The slot shows the EmptyState with
// its close button, never vanishing on its own (DA-E10).
func (s *screens) missingTarget(ref SlotRef) bool {
	if ref.Kind != KindSession || ref.ID == "" {
		return false
	}
	if !s.sessionsLoaded && len(s.sidebar.List()) == 0 {
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
		if isMainPos(p) {
			s.grid.openMain(SlotRef{Kind: KindSession})
		} else {
			s.grid.edited(CloseSlot(s.grid.doc, p))
		}
	}
	_ = dims
}

// slotAction is one pane-level action button hosted in the slot header (§5.3).
type slotAction struct {
	props   kit.IconButtonProps
	onClick func()
}

// slotHeaderActions returns the pane-level IconButton actions for ref, hosted
// in the shared slot header with tooltips rather than duplicated in-pane.
func slotHeaderActions(sc *screens, ref SlotRef) []slotAction {
	switch ref.Kind {
	case KindAccounts:
		return []slotAction{{
			props: kit.IconButtonProps{
				Icon:     kit.IconRestart,
				Label:    "Refresh accounts",
				Variant:  kit.IconButtonGhost,
				Size:     kit.SizeSM,
				Disabled: sc.accountsWorker == nil || sc.accountsView.refreshing,
			},
			onClick: sc.requestRefreshAccounts,
		}}
	case KindSettings:
		return []slotAction{{
			props: kit.IconButtonProps{
				Icon:     kit.IconRestart,
				Label:    "Refresh settings",
				Variant:  kit.IconButtonGhost,
				Size:     kit.SizeSM,
				Disabled: sc.settingsWorker == nil || sc.settings.Loading,
			},
			onClick: sc.requestReloadSettings,
		}}
	case KindSecrets:
		wsID, _ := sc.currentWorkspace()
		return []slotAction{{
			props: kit.IconButtonProps{
				Icon:     kit.IconPlus,
				Label:    labelAddSecret,
				Variant:  kit.IconButtonGhost,
				Size:     kit.SizeSM,
				Disabled: sc.vault.Locked() || sc.secretsWorker == nil,
			},
			onClick: func() { sc.secretsView.addDlg.open(wsID) },
		}}
	case KindSession, KindTeam, KindAgent, KindDiff, KindInbox, KindModule:
		return nil
	default:
		return nil
	}
}

// closeLabel names the slots' close action in the EmptyStates.
const closeLabel = "Close slot"

// layoutMainEmptyState centers the kit EmptyState in the empty main session
// slot with an icon, title, one-line description and primary "New session"
// action (FUG-65).
func (s *screens) layoutMainEmptyState(gtx *layout.Context, env *kit.Env) {
	s.layoutMainEmptyStateDims(gtx, env)
}

func (s *screens) layoutMainEmptyStateDims(gtx *layout.Context, env *kit.Env) image.Rectangle {
	paint.FillShape(gtx.Ops, env.Th.Chrome().CanvasBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	box := *gtx
	box.Constraints.Min = box.Constraints.Max
	var childRect image.Rectangle
	layout.Center.Layout(box, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		dims, ev, err := s.emptyTranscript.Layout(env, &local, &kit.EmptyStateProps{
			Icon:          kit.IconMessageSquare,
			Title:         "Select a session",
			Description:   "Choose a session from the sidebar or start a new one.",
			PrimaryAction: newSessionLabel,
		})
		s.fail(err)
		if ev.PrimaryClicked {
			s.composer.start.open(s)
		}
		off := box.Constraints.Max.Sub(dims.Size).Div(2)
		childRect = image.Rectangle{Min: off, Max: off.Add(dims.Size)}
		return dims
	})
	return childRect
}
