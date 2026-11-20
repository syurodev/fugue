package appwin

import (
	"image"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The workspace sidebar (F10.14, desktop-app §5.3): the chat sidebar lists
// every workspace at once, the sessions under the workspace each is bound
// to, and a create-workspace affordance at the top. Each workspace's manage
// menu adds, edits and removes its roots, deletes the workspace, and opens
// the Q-64 card while a policy change waits for confirmation. There is no
// common workspace switcher: the user works in several workspaces at once.

// wsReader is the workspace-read side the screens arm: the list and the
// per-workspace detail reads. The session fetcher satisfies it once its
// workspace reads land.
// TODO(F10.14): wire session.Fetcher here and drop the nil guards when
// #169's fetcher carries ResultWorkspaces and ResultWorkspace.
type wsReader interface {
	RequestWorkspaces()
	RequestWorkspace(id string)
}

// wsGroup is one workspace's slice of the sidebar: the workspace and the
// sessions bound to it, in list order.
type wsGroup struct {
	ws workspace.Summary
	// sessions are the workspace's sessions; a session the list read has not
	// bound to a known workspace yet reaches the sidebar under the trailing
	// "Other sessions" header instead.
	sessions []session.Session
}

// wsGroups splits the session list by the workspace each session is bound
// to, in the workspaces' list order, and returns the sessions no known
// workspace claims.
func wsGroups(sessions []session.Session, workspaces []workspace.Summary) ([]wsGroup, []session.Session) {
	byWS := make(map[string]int, len(workspaces))
	groups := make([]wsGroup, 0, len(workspaces))
	for _, ws := range workspaces {
		byWS[ws.ID] = len(groups)
		groups = append(groups, wsGroup{ws: ws})
	}
	var other []session.Session
	for _, sess := range sessions {
		i, ok := byWS[sess.WorkspaceID]
		if !ok {
			other = append(other, sess)
			continue
		}
		groups[i].sessions = append(groups[i].sessions, sess)
	}
	return groups, other
}

// layoutSidebarContent draws the workspace groups under the sidebar's top
// header: one collapsible section per workspace with its sessions, then the
// sessions no known workspace claims.
func (s *screens) layoutSidebarContent(env *kit.Env, gtx *layout.Context) layout.Dimensions {
	groups, other := wsGroups(s.sidebar.List(), s.workspaces.List())
	sections := make([]layout.FlexChild, 0, len(groups)+1)
	for i := range groups {
		group := groups[i]
		sections = append(sections, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return s.workspaceSection(env, &local, group)
		}))
	}
	if len(other) > 0 {
		sections = append(sections, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			header, _ := s.otherHeader.Layout(env, &local,
				&kit.SectionHeaderProps{Label: "Other sessions"},
				func(gtx *layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, s.sessionRows(env, other)...)
				})
			return header
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, sections...)
}

// workspaceSection draws one workspace: its header with the manage action
// and, folded under it, the workspace's sessions. While the manage menu of
// this workspace is open, the menu lays out anchored at the header's action.
func (s *screens) workspaceSection(env *kit.Env, gtx *layout.Context, group wsGroup) layout.Dimensions {
	header := s.wsHeader(group.ws.ID)
	props := &kit.SectionHeaderProps{
		Label:       group.ws.Name,
		Collapsible: true,
		Action:      &kit.IconButtonProps{Icon: kit.IconEllipsis, Label: "Manage workspace"},
	}
	dims, ev := header.Layout(env, gtx, props, func(gtx *layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, s.sessionRows(env, group.sessions)...)
	})
	if ev.ActionClick {
		s.wsMenuFor = group.ws.ID
		s.wsMenu.Open()
	}
	if s.wsMenu.IsOpen() && s.wsMenuFor == group.ws.ID {
		s.wsMenu.SetOpener(header.Tag())
		res, err := s.wsMenu.Layout(env, gtx, kit.MenuProps{Items: s.manageItems(group.ws), Anchor: s.wsMenuAnchor(gtx, dims.Size.Y)})
		if err != nil {
			s.fail(err)
		}
		s.chooseManage(group.ws, res.Chosen)
	}
	return dims
}

// wsMenuAnchor is the manage menu's anchor: the header's action button box,
// in the section's own coordinates.
func (s *screens) wsMenuAnchor(gtx *layout.Context, headerH int) image.Rectangle {
	btn := gtx.Dp(theme.ControlSM)
	x := gtx.Constraints.Max.X - gtx.Dp(theme.SpaceXS) - btn
	y := (headerH - btn) / 2
	return image.Rectangle{Min: image.Pt(x, y), Max: image.Pt(x+btn, y+btn)}
}

// wsHeader returns the workspace's section header state, building it on
// first use, and drops the states of workspaces the list no longer names.
func (s *screens) wsHeader(id string) *kit.SectionHeaderState {
	header, ok := s.wsHeaders[id]
	if !ok {
		header = kit.NewSectionHeaderState(true)
		s.wsHeaders[id] = header
	}
	return header
}

// pruneWorkspaceHeaders drops the header states of workspaces that left the
// list, so a deleted workspace's fold state does not outlive it.
func (s *screens) pruneWorkspaceHeaders() {
	known := map[string]bool{}
	for _, ws := range s.workspaces.List() {
		known[ws.ID] = true
	}
	for id := range s.wsHeaders {
		if !known[id] {
			delete(s.wsHeaders, id)
		}
	}
}

// manageItems builds the workspace's manage menu: the policy card first
// while a change waits, then the root management, then the deletion. The
// root items need the workspace's detail, whose hash every mutation is a
// compare-and-swap against.
func (s *screens) manageItems(ws workspace.Summary) []kit.MenuItem {
	items := make([]kit.MenuItem, 0, 4)
	if ws.PendingPolicyConfirmation {
		items = append(items, kit.MenuItem{ID: "review", Label: "Review policy change…"})
	}
	det, hasDetail := s.workspaces.DetailFor(ws.ID)
	items = append(items, kit.MenuItem{ID: "add-root", Label: "Add root…", Disabled: !hasDetail})
	if hasDetail && len(det.Roots) > 0 {
		sub := make([]kit.MenuItem, 0, 2*len(det.Roots))
		for _, r := range det.Roots {
			sub = append(sub, kit.MenuItem{ID: "edit:" + r.Name, Label: "Edit " + r.Name + "…"})
			sub = append(sub, kit.MenuItem{ID: "remove:" + r.Name, Label: "Remove " + r.Name + "…"})
		}
		items = append(items, kit.MenuItem{ID: "roots", Label: "Roots", Sub: sub})
	}
	items = append(items, kit.MenuItem{ID: "delete", Label: "Delete workspace…"})
	return items
}

// chooseManage acts on the manage menu's choice; an empty choice is a close
// or a stray click, which acts on nothing.
func (s *screens) chooseManage(ws workspace.Summary, chosen string) {
	if chosen == "" {
		return
	}
	s.wsMenuFor = ""
	det, hasDetail := s.workspaces.DetailFor(ws.ID)
	switch {
	case chosen == "review":
		if !hasDetail {
			s.fail(errDialogNeedsDetail)
			return
		}
		s.dlg.policy.openFor(&det)
	case chosen == "add-root":
		if !hasDetail {
			s.fail(errDialogNeedsDetail)
			return
		}
		s.dlg.root.openAdd(&det)
	case chosen == "delete":
		if !hasDetail {
			s.fail(errDialogNeedsDetail)
			return
		}
		s.dlg.del.openFor(&det)
	case strings.HasPrefix(chosen, "edit:"):
		s.openRootEdit(det, hasDetail, strings.TrimPrefix(chosen, "edit:"))
	case strings.HasPrefix(chosen, "remove:"):
		s.openRootRemove(det, hasDetail, strings.TrimPrefix(chosen, "remove:"))
	}
}

// openRootEdit opens the root dialog on the named root of the detail.
func (s *screens) openRootEdit(det workspace.Detail, hasDetail bool, name string) {
	if !hasDetail {
		s.fail(errDialogNeedsDetail)
		return
	}
	for i := range det.Roots {
		if det.Roots[i].Name == name {
			s.dlg.root.openFor(&det, &det.Roots[i])
			return
		}
	}
}

// openRootRemove opens the remove-root confirm on the named root.
func (s *screens) openRootRemove(det workspace.Detail, hasDetail bool, name string) {
	if !hasDetail {
		s.fail(errDialogNeedsDetail)
		return
	}
	for i := range det.Roots {
		if det.Roots[i].Name == name {
			s.dlg.rmRoot.openFor(&det, det.Roots[i])
			return
		}
	}
}

// layoutPolicyBanner draws the sidebar's pending-policy banner, shown while
// any workspace waits for a policy decision; the action opens the first
// pending workspace's card.
func (s *screens) layoutPolicyBanner(env *kit.Env, gtx *layout.Context) layout.Dimensions {
	pending := ""
	for _, ws := range s.workspaces.List() {
		if ws.PendingPolicyConfirmation {
			pending = ws.ID
			break
		}
	}
	props := &kit.BannerProps{
		Kind:        kit.BannerWarning,
		Title:       "Policy change needs confirmation",
		Message:     "A workspace's policy changed outside Fugue; sessions there wait for your decision.",
		ActionLabel: "Review",
		Visible:     pending != "",
	}
	dims, ev, err := s.policyBanner.Layout(env, gtx, *props)
	if err != nil {
		s.fail(err)
	}
	if ev.ActionClicked && pending != "" {
		if det, ok := s.workspaces.DetailFor(pending); ok {
			s.dlg.policy.openFor(&det)
		} else {
			s.fail(errDialogNeedsDetail)
		}
	}
	return dims
}
