package appwin

import (
	"fmt"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/transcriptview"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The screens' composer glue (F10.4b): the reads the composer and the start
// flow drive, the finished writes they fold back, and the connection state
// that gates the input. The screens themselves — sidebar, transcript,
// layout — live in screens.go; both files are one screens type.

// applyComposerResult folds one composer read into the screens: the open
// session's slash commands, the vendors' capabilities, and the workspace model
// reads that feed both the sidebar and the start dialog.
func (s *screens) applyComposerResult(r session.Result) {
	switch r.Kind {
	case session.ResultCommands:
		s.pendingCommands[r.Session] = false
		cmds, ok := s.commands[r.Session]
		if !ok {
			cmds = &session.Commands{}
			s.commands[r.Session] = cmds
		}
		cmds.ApplyList(r.Commands)
		if r.Err != nil {
			s.fail(fmt.Errorf("read the commands: %w", r.Err))
		}
	case session.ResultProbe:
		s.pendingProbe = false
		s.composer.applyProbes(r.Probes)
		s.failure.applyProbe(r.Probes, r.Err)
		if r.Err != nil {
			s.fail(fmt.Errorf("probe the vendors: %w", r.Err))
		}
	case session.ResultWorkspaces:
		s.pendingWorkspaces = false
		if r.Err != nil {
			s.fail(fmt.Errorf("list the workspaces: %w", r.Err))
			return
		}
		s.workspaces.ApplyList(workspaceSummariesFromClient(r.Workspaces))
		s.pruneWorkspaceHeaders()
		s.reconcileWorkspaceDialogs()
	case session.ResultWorkspace:
		if r.Err != nil {
			s.fail(fmt.Errorf("read the workspace: %w", r.Err))
			return
		}
		det := workspaceDetailFromClient(&r.Workspace)
		det.Generation = r.Generation
		s.workspaces.ApplyDetail(det)
		if s.workspaces.Generation(det.ID) == r.Generation {
			delete(s.wsDetailReq, det.ID)
		}
		s.composer.start.reconcile(s)
		s.armViewerRead()
	case session.ResultSessions, session.ResultPage, session.ResultItem:
		// The list, page, and item-detail reads fold in applyResult.
	}
}

// workspaceSummariesFromClient maps pkg/client workspace summaries into the
// workspace model's summary slice.
func workspaceSummariesFromClient(list []client.WorkspaceSummary) []workspace.Summary {
	out := make([]workspace.Summary, 0, len(list))
	for _, w := range list {
		out = append(out, workspace.Summary{
			ID:                        w.ID,
			Name:                      w.Name,
			MissingRoots:              w.MissingRoots,
			ConfigRevision:            w.ConfigRevision,
			PendingPolicyConfirmation: w.PendingPolicyConfirmation,
		})
	}
	return out
}

// workspaceDetailFromClient maps pkg/client's Workspace into the workspace
// model's Detail, carrying ConfigHash, ConfigRevision, Roots and PolicyDiff.
func workspaceDetailFromClient(w *client.Workspace) *workspace.Detail {
	det := &workspace.Detail{
		ID:             w.ID,
		Name:           w.Name,
		ConfigHash:     w.ConfigHash,
		ConfigRevision: w.ConfigRevision,
		MissingRoots:   w.MissingRoots,
		Pending:        w.PendingPolicyConfirmation,
		Roots:          make([]workspace.Root, 0, len(w.Roots)),
		Diff:           make([]workspace.DiffEntry, 0, len(w.PolicyDiff)),
	}
	for _, r := range w.Roots {
		det.Roots = append(det.Roots, workspace.Root{Name: r.Name, Path: r.Path, Access: r.Access, Missing: r.Missing})
	}
	for _, d := range w.PolicyDiff {
		det.Diff = append(det.Diff, workspace.DiffEntry{Key: d.Key, OldValue: d.OldValue, NewValue: d.NewValue})
	}
	return det
}

// applyPostResult folds one finished write into the screens. A failed send
// or steer puts the draft back while the user has not typed since; a start
// marks the session to open on its created event.
func (s *screens) applyPostResult(p session.PostResult) {
	switch p.Kind {
	case session.PostStart:
		if p.Err != nil {
			s.composer.start.startPosted(p.Err)
			return
		}
		s.lastStarted = p.Started
		s.composer.start.startPosted(nil)
	case session.PostResume:
		if p.Err != nil {
			s.composer.stateFor(p.Session).lastErr = p.Err.Error()
		}
	case session.PostSend, session.PostSteer:
		if p.Err == nil {
			return
		}
		// The send never landed: no item will arrive, so the armed
		// new-turn anchor stands down (F10.4d).
		if p.Session == s.open {
			s.turnArm = false
		}
		st := s.composer.stateFor(p.Session)
		st.lastErr = p.Err.Error()
		if st.area.Text() == "" && p.Text != "" {
			st.area.SetText(p.Text)
			st.area.MoveCaretToEnd()
		}
	case session.PostInterrupt, session.PostClose:
		if p.Err != nil {
			s.composer.stateFor(p.Session).lastErr = p.Err.Error()
		}
	}
}

// connected reports the manager's connection state and whether the host is
// reachable, which disables the composer while it is not.
func (s *screens) connected() (hostconn.State, bool) {
	state, _ := s.m.State()
	return state, state == hostconn.StateConnected
}

// commandsFor returns the session's command model, empty until its first
// read lands.
func (s *screens) commandsFor(id string) *session.Commands {
	cmds, ok := s.commands[id]
	if !ok {
		cmds = &session.Commands{}
		s.commands[id] = cmds
	}
	return cmds
}

// commandsPending reports a commands read still in flight, which the slash
// menu shows as loading.
func (s *screens) commandsPending(id string) bool { return s.pendingCommands[id] }

// workspacesPending reports the start flow's workspace list read in flight.
func (s *screens) workspacesPending() bool { return s.pendingWorkspaces }

// requestCommands reads a session's commands once per trigger.
func (s *screens) requestCommands(id string) {
	if s.pendingCommands[id] {
		return
	}
	s.pendingCommands[id] = true
	s.fetcher.RequestCommands(id)
}

// requestWorkspaces reads the workspace list once per trigger.
func (s *screens) requestWorkspaces() {
	if s.pendingWorkspaces {
		return
	}
	s.pendingWorkspaces = true
	s.wsReads.RequestWorkspaces()
}

// requestWorkspace reads one workspace's detail if no read at the current
// generation is already in flight.
func (s *screens) requestWorkspace(id string) {
	gen := s.workspaces.Generation(id)
	if armedGen, armed := s.wsDetailReq[id]; armed && armedGen == gen {
		return
	}
	s.wsDetailReq[id] = gen
	s.wsReads.RequestWorkspace(id, gen)
}

// probeMissingVendors re-probes when the session list names a vendor the
// composer has no capability for: a vendor joined the list under a
// connection that already probed, and the composer gates Steer and stop on
// the probed capability. One probe is in flight at a time; a failed probe
// (a vendor CLI the host cannot resolve, say) retries on the next list
// change rather than on every frame.
func (s *screens) probeMissingVendors() {
	if s.pendingProbe {
		return
	}
	for _, sess := range s.sidebar.List() {
		if sess.Vendor != "" && !s.composer.knowsVendor(sess.Vendor) {
			s.pendingProbe = true
			s.fetcher.RequestProbe()
			return
		}
	}
}

// prune drops the state of sessions that left the sidebar list, so a closed
// session's scroll, expansion and composer state does not outlive it. The
// open session leaving the list closes it cleanly: its topics leave the
// connection, the pane falls back to the empty state, and clicking the
// session again reopens it.
func (s *screens) prune() {
	known := map[string]bool{}
	for _, sess := range s.sidebar.List() {
		known[sess.ID] = true
	}
	s.releaseSlotSessions(known)
	if !known[s.open] {
		s.setOpenSession("")
	}
	pruneKnown(s.views, known)
	pruneKnown(s.transcripts, known)
	pruneKnown(s.pendingPage, known)
	pruneKnown(s.pendingOlder, known)
	pruneKnown(s.reopenAnchor, known)
	pruneKnown(s.rows, known)
	pruneKnown(s.commands, known)
	pruneKnown(s.pendingCommands, known)
	s.composer.prune(known)
}

// pruneKnown deletes every key of m the known set does not name.
func pruneKnown[T any](m map[string]T, known map[string]bool) {
	for id := range m {
		if !known[id] {
			delete(m, id)
		}
	}
}

func (s *screens) transcriptCallbacks() transcriptview.Callbacks {
	return transcriptview.Callbacks{
		OnOpenLink: func(u string) {
			s.fail(openHTTPSURL(u))
		},
		OnFullOutput: func(itemID string) {
			if s.open == "" || itemID == "" {
				return
			}
			key := s.open + ":" + itemID
			if s.pendingItem[key] {
				return
			}
			s.pendingItem[key] = true
			s.fetcher.RequestItem(s.open, itemID)
		},
		OnOpenInbox: func() {
			s.grid.openCompanion(SlotRef{Kind: KindInbox})
		},
		OnOpenFile: s.openFileRef,
		OnViewDiff: s.openDiffViewer,
		OnIDChip:   s.jumpToItem,
	}
}

// jumpToItem follows a `#n` chip to its message: the jump anchors the message
// near the top and ends the follow-at-bottom (F10.4d, desktop-app v0.9 §5.4).
func (s *screens) jumpToItem(id string) {
	if s.open == "" || id == "" {
		return
	}
	t := s.transcripts[s.open]
	if t == nil {
		return
	}
	if _, ok := t.ItemByID(id); ok {
		s.viewFor(s.open).AnchorTurn(id)
	}
}

// armTurnAnchor records a composer send (Send or Steer): the sent message's
// user_message item, once it lands newer than the newest item the transcript
// holds, anchors near the top with its context (F10.4d).
func (s *screens) armTurnAnchor(id string) {
	if id != s.open {
		return
	}
	t := s.transcripts[id]
	if t == nil {
		return
	}
	s.turnArm, s.turnSeq = true, t.LastItemSequence()
}

// applyTurnAnchor fires the armed new-turn anchor once the sent message's
// item is in the model. It runs on the transcript pane's frame path and
// returns immediately while no send is pending.
func (s *screens) applyTurnAnchor() {
	if !s.turnArm {
		return
	}
	t := s.transcripts[s.open]
	if t == nil {
		return
	}
	msg, ok := t.LastUserMessage()
	if !ok || msg.Sequence <= s.turnSeq {
		return
	}
	s.turnArm = false
	if msg.ItemID == s.turnAnchored {
		return
	}
	s.turnAnchored = msg.ItemID
	s.viewFor(s.open).AnchorTurn(msg.ItemID)
}

// applyReopenAnchor anchors a session's fresh view at the last turn's user
// message when its newest page lands — before the pane draws the page, so
// the reader never sees the view jump (desktop-app v0.9 §5.4, "Mở lại
// phiên"). A session with no turn yet keeps the fresh view's follow.
func (s *screens) applyReopenAnchor(id string) {
	if !s.reopenAnchor[id] {
		return
	}
	delete(s.reopenAnchor, id)
	t := s.transcripts[id]
	if t == nil {
		return
	}
	if msg, ok := t.LastUserMessage(); ok {
		s.viewFor(id).AnchorTurn(msg.ItemID)
	}
}

// layoutTranscriptPane draws the transcript itself: the canvas, the empty
// state, the transcript list and the read failure line. The pane serves the
// session id names — the main slot's open session, or another session's slot
// (§5.3); the anchors and on-demand paging stay the open session's, whose
// read side they belong to.
func (s *screens) layoutTranscriptPane(gtx *layout.Context, env *kit.Env, id string, view *transcriptview.State, items []*session.Item) {
	paint.FillShape(gtx.Ops, env.Th.Chrome().CanvasBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	if len(items) == 0 && id == "" {
		layout.Center.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			dims, _, err := s.emptyTranscript.Layout(env, &local, &kit.EmptyStateProps{
				Title:       "Select a session",
				Description: "Choose a session from the sidebar or start a new one below.",
			})
			s.fail(err)
			return dims
		})
		return
	}
	if id == s.open {
		s.applyTurnAnchor()
	}
	transcriptview.LayoutWithCallbacks(env, gtx, view, items, s.transcriptCallbacks())
	if id == s.open {
		s.trackScroll(view)
		s.maybePageOlder()
	}
	if s.lastErr == "" {
		return
	}
	local := *gtx
	inset := layout.Inset{Left: theme.SpaceLG, Right: theme.SpaceLG, Top: theme.SpaceXS}
	inset.Layout(local, func(gtx layout.Context) layout.Dimensions {
		l := gtx
		l.Constraints.Min.X = 0
		return kit.Label(env, &l, theme.TypeSmall(), env.Th.Status().Danger, s.lastErr)
	})
}

// trackScroll marks that the reader has scrolled, which arms paging older.
func (s *screens) trackScroll(view *transcriptview.State) {
	if view.Offset() > 0 {
		s.scrolled = true
	}
}
