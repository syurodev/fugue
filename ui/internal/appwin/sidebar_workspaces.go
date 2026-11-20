package appwin

import (
	"image"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/hostconn"
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
// per-workspace detail reads, satisfied by session.Fetcher.
type wsReader interface {
	RequestWorkspaces()
	RequestWorkspace(id string)
}

// applyWorkspaceDelivery routes one workspaces-topic delivery into the
// model: a snapshot replaces the list, an event folds in.
func (s *screens) applyWorkspaceDelivery(u hostconn.Update) {
	switch {
	case u.Snapshot != nil:
		s.fail(s.workspaces.ApplySnapshot(u.Snapshot.State))
		s.pruneWorkspaceHeaders()
		s.reconcileWorkspaceDialogs()
	case u.Event != nil:
		s.applyWorkspaceEvent(u.Event.Type, u.Event.Payload)
	}
}

// applyWorkspaceEvent folds one workspaces-topic event into the model. Any
// event that deletes a workspace or drops its detail closes open dialogs that
// targeted that stale detail, and armWorkspaceReads queues the fresh read.
func (s *screens) applyWorkspaceEvent(eventType string, payload []byte) {
	if err := s.workspaces.ApplyEvent(eventType, payload); err != nil {
		s.fail(err)
		return
	}
	s.pruneWorkspaceHeaders()
	s.reconcileWorkspaceDialogs()
}

// reconcileWorkspaceDialogs closes any open workspace-targeted dialog whose
// workspace or detail was dropped (for example, deleted or invalidated by a
// config/root/policy event), so no dialog stays open on a stale hash. The
// create dialog targets no existing workspace and stays open, and the start
// dialog clears a gone workspace selection.
func (s *screens) reconcileWorkspaceDialogs() {
	hasDetail := func(id string) bool {
		_, ok := s.workspaces.DetailFor(id)
		return ok
	}
	if s.dlg.root.dialog.IsOpen() && !hasDetail(s.dlg.root.wsID) {
		s.dlg.root.dialog.Close()
	}
	if s.dlg.del.dialog.IsOpen() && !hasDetail(s.dlg.del.wsID) {
		s.dlg.del.dialog.Close()
	}
	if s.dlg.rmRoot.dialog.IsOpen() && !hasDetail(s.dlg.rmRoot.wsID) {
		s.dlg.rmRoot.dialog.Close()
	}
	if s.dlg.policy.dialog.IsOpen() && !hasDetail(s.dlg.policy.wsID) {
		s.dlg.policy.dialog.Close()
	}
	if s.composer != nil {
		s.composer.start.reconcile(s)
	}
}

// armWorkspaceReads arms one workspace detail read per workspace the model
// holds no detail for, once per trigger, and drops the guards of the reads
// whose detail has landed.
func (s *screens) armWorkspaceReads() {
	for _, id := range s.workspaces.NeedsDetail() {
		if s.wsDetailReq[id] {
			continue
		}
		s.wsDetailReq[id] = true
		s.wsReads.RequestWorkspace(id)
	}
	for id := range s.wsDetailReq {
		if _, ok := s.workspaces.DetailFor(id); ok {
			delete(s.wsDetailReq, id)
		}
	}
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
	det, hasDetail := s.workspaces.DetailFor(ws.ID)
	ready := hasDetail && det.ConfigHash != ""
	if ws.PendingPolicyConfirmation {
		items = append(items, kit.MenuItem{ID: "review", Label: "Review policy change…", Disabled: !ready})
	}
	items = append(items, kit.MenuItem{ID: "add-root", Label: "Add root…", Disabled: !ready})
	if ready && len(det.Roots) > 0 {
		sub := make([]kit.MenuItem, 0, 2*len(det.Roots))
		for _, r := range det.Roots {
			sub = append(sub, kit.MenuItem{ID: "edit:" + r.Name, Label: "Edit " + r.Name + "…"})
			sub = append(sub, kit.MenuItem{ID: "remove:" + r.Name, Label: "Remove " + r.Name + "…"})
		}
		items = append(items, kit.MenuItem{ID: "roots", Label: "Roots", Sub: sub})
	}
	items = append(items, kit.MenuItem{ID: "delete", Label: "Delete workspace…", Disabled: !ready})
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
	ready := hasDetail && det.ConfigHash != ""
	switch {
	case chosen == "review":
		if !ready {
			s.fail(errDialogNeedsDetail)
			return
		}
		s.dlg.policy.openFor(&det)
	case chosen == "add-root":
		if !ready {
			s.fail(errDialogNeedsDetail)
			return
		}
		s.dlg.root.openAdd(&det)
	case chosen == "delete":
		if !ready {
			s.fail(errDialogNeedsDetail)
			return
		}
		s.dlg.del.openFor(&det)
	case strings.HasPrefix(chosen, "edit:"):
		s.openRootEdit(det, ready, strings.TrimPrefix(chosen, "edit:"))
	case strings.HasPrefix(chosen, "remove:"):
		s.openRootRemove(det, ready, strings.TrimPrefix(chosen, "remove:"))
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
		if det, ok := s.workspaces.DetailFor(pending); ok && det.ConfigHash != "" {
			s.dlg.policy.openFor(&det)
		} else {
			s.fail(errDialogNeedsDetail)
		}
	}
	return dims
}

// layoutSidebar draws the workspace groups and their sessions (F10.14,
// desktop-app §5.3): the top header carries the create-workspace action, a
// warning banner rides above the list while a policy change waits, and a row
// click opens the session. The column holds the SidebarWidth token: kit rows
// fill the width they are given, so the list must not swallow the
// transcript's half of the window.
func (s *screens) layoutSidebar(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(theme.SidebarWidth))
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return s.layoutPolicyBanner(env, &local)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			header, ev := s.header.Layout(env, &local, &kit.SectionHeaderProps{
				Label:  "Workspaces",
				Action: &kit.IconButtonProps{Icon: kit.IconPlus, Label: "New workspace"},
			}, func(gtx *layout.Context) layout.Dimensions {
				return s.layoutSidebarContent(env, gtx)
			})
			if ev.ActionClick {
				s.dlg.create.open()
			}
			if ev.Err != nil {
				s.fail(ev.Err)
			}
			return header
		}),
	)
}

// sessionRows builds one kit row per session of the given list; the row's
// click opens it.
func (s *screens) sessionRows(env *kit.Env, sessions []session.Session) []layout.FlexChild {
	rows := make([]layout.FlexChild, 0, len(sessions))
	for _, sess := range sessions {
		row, ok := s.rows[sess.ID]
		if !ok {
			row = kit.NewListRowState()
			row.Enter()
			s.rows[sess.ID] = row
		}
		selected := s.open == sess.ID
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			dims, ev := row.Layout(env, &local, &kit.ListRowProps{
				Label:     sessionName(sess),
				Secondary: sessionState(sess),
				Selected:  selected,
			})
			if ev.Clicked {
				s.openSession(sess.ID)
			}
			return dims
		}))
	}
	return rows
}

// sessionName names a session row: the vendor, or "session" when the list
// read has not named one yet.
func sessionName(sess session.Session) string {
	if sess.Vendor == "" {
		return "session"
	}
	return sess.Vendor
}

// sessionState is the row's secondary text: the state and the wait reason,
// in FgMuted like every kit row's secondary text.
func sessionState(sess session.Session) string {
	state := sess.State
	if sess.WaitingReason != "" {
		state += " · " + sess.WaitingReason
	}
	return state
}
