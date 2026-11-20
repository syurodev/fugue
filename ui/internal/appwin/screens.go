package appwin

import (
	"fmt"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/transcriptview"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The topic name pieces the screens route on; the constants mirror the
// host's topic owners.
const (
	topicVendorSessions = "vendor-sessions"
	topicTranscript     = "transcript:"
	topicTranscriptDlt  = "transcript-delta:"
	topicWorkspaces     = "workspaces"
)

// pageFetcher is the read side the screens drive: the session.Fetcher, and a
// recording fake in the tests.
type pageFetcher interface {
	RequestSessions()
	RequestPage(session string, before int64)
	Results() <-chan session.Result
}

// screens is the app's read side (F10.4a): the sidebar's session list, the
// open session's transcript model, and each session's transcript view state.
// It changes only on the window goroutine, which applies every delivered
// update here.
type screens struct {
	m       *hostconn.Manager
	fetcher pageFetcher

	sidebar     *session.Sidebar
	transcripts map[string]*session.Transcript
	// views holds one scroll state per session, so session A's offset never
	// leaks into session B; viewCache is the window's one shared layout
	// cache every session's list rides (F10.4a, desktop-app §7).
	views     map[string]*transcriptview.State
	viewCache *kit.VirtualHeightCache
	// open is the open session's id, empty when none is.
	open string

	header *kit.SectionHeaderState
	rows   map[string]*kit.ListRowState
	// pendingPage and pendingOlder guard one read per trigger, and scrolled
	// makes paging older a user-driven act: it arms after the first scroll
	// and resets when the session changes.
	pendingPage  map[string]bool
	pendingOlder map[string]bool
	scrolled     bool
	// lastErr is the last read or payload failure the screens could not act
	// on, shown under the transcript.
	lastErr string

	// The workspace side (F10.14): the workspaces the topic reports, the
	// reads that fill their details, the mutations the dialogs post, and
	// the sidebar's own state per workspace.
	workspaces   *workspace.Model
	wsMut        *workspace.Mutator
	wsReads      wsReader
	wsDetailReq  map[string]bool
	wsHeaders    map[string]*kit.SectionHeaderState
	wsMenu       *kit.MenuState
	wsMenuFor    string
	policyBanner *kit.BannerState
	otherHeader  *kit.SectionHeaderState
	dlg          *workspaceDialogs
}

// newScreens builds the screens over the manager, its fetcher and the
// workspace mutator.
func newScreens(m *hostconn.Manager, fetcher pageFetcher, wsMut *workspace.Mutator) *screens {
	return &screens{
		m:           m,
		fetcher:     fetcher,
		sidebar:     session.NewSidebar(),
		transcripts: map[string]*session.Transcript{},
		views:       map[string]*transcriptview.State{},
		viewCache:   kit.NewVirtualHeightCache(),
		header:      kit.NewSectionHeaderState(true),
		rows:        map[string]*kit.ListRowState{},
		pendingPage: map[string]bool{}, pendingOlder: map[string]bool{},
		workspaces:   workspace.NewModel(),
		wsMut:        wsMut,
		wsDetailReq:  map[string]bool{},
		wsHeaders:    map[string]*kit.SectionHeaderState{},
		wsMenu:       kit.NewMenuState(),
		policyBanner: kit.NewBannerState(),
		otherHeader:  kit.NewSectionHeaderState(false),
		dlg:          newWorkspaceDialogs(),
	}
}

// viewFor returns the session's scroll state, building it on first use. A new
// state follows the newest item, so opening a session lands on it.
func (s *screens) viewFor(id string) *transcriptview.State {
	view, ok := s.views[id]
	if !ok {
		view = transcriptview.NewState(s.viewCache)
		s.views[id] = view
	}
	return view
}

// prune drops the state of sessions that left the sidebar list, so a closed
// session's scroll and expansion state does not outlive it.
func (s *screens) prune() {
	known := map[string]bool{}
	for _, sess := range s.sidebar.List() {
		known[sess.ID] = true
	}
	pruneKnown(s.views, known)
	pruneKnown(s.transcripts, known)
	pruneKnown(s.pendingPage, known)
	pruneKnown(s.pendingOlder, known)
	pruneKnown(s.rows, known)
}

// pruneKnown deletes every key of m the known set does not name.
func pruneKnown[T any](m map[string]T, known map[string]bool) {
	for id := range m {
		if !known[id] {
			delete(m, id)
		}
	}
}

// update folds one frame's deliveries into the screens.
func (s *screens) update(updates []hostconn.Update, results []session.Result, muts []workspace.MutResult) {
	for _, u := range updates {
		s.applyUpdate(u)
	}
	for _, r := range results {
		s.applyResult(r)
	}
	for i := range muts {
		s.dlg.apply(&muts[i])
	}
	s.armWorkspaceReads()
}

// applyUpdate routes one topic delivery or connection-state change.
func (s *screens) applyUpdate(u hostconn.Update) {
	switch {
	case u.State == hostconn.StateConnected:
		s.reconnected()
	case u.Topic == topicVendorSessions && u.Snapshot != nil:
		s.fail(s.sidebar.ApplySnapshot(u.Snapshot.State))
		s.prune()
	case u.Topic == topicVendorSessions && u.Event != nil:
		s.applySessionEvent(u.Event.Type, u.Event.Payload)
	case u.Topic == topicWorkspaces:
		s.applyWorkspaceDelivery(u)
	case u.Topic == topicTranscriptDlt+s.open && u.Event != nil:
		s.applyDelta(u.Event.Payload)
	case u.Topic == topicTranscript+s.open:
		s.applyTranscriptDelivery(u)
	}
}

// applyWorkspaceDelivery routes one workspaces-topic delivery into the
// model: a snapshot replaces the list, an event folds in.
func (s *screens) applyWorkspaceDelivery(u hostconn.Update) {
	switch {
	case u.Snapshot != nil:
		s.fail(s.workspaces.ApplySnapshot(u.Snapshot.State))
		s.pruneWorkspaceHeaders()
		s.closeDialogsOfGoneWorkspaces()
	case u.Event != nil:
		s.applyWorkspaceEvent(u.Event.Type, u.Event.Payload)
	}
}

// applyWorkspaceEvent folds one workspaces-topic event into the model. A
// deleted workspace closes its dialogs and prunes the sidebar's state for it.
func (s *screens) applyWorkspaceEvent(eventType string, payload []byte) {
	if err := s.workspaces.ApplyEvent(eventType, payload); err != nil {
		s.fail(err)
		return
	}
	if eventType == workspace.EventDeleted {
		s.pruneWorkspaceHeaders()
		s.closeDialogsOfGoneWorkspaces()
	}
}

// closeDialogsOfGoneWorkspaces closes a dialog whose workspace left the list,
// so no dialog stays open on a mutation target that no longer exists.
func (s *screens) closeDialogsOfGoneWorkspaces() {
	live := func(id string) bool {
		_, ok := s.workspaces.Lookup(id)
		return ok
	}
	if !live(s.dlg.root.wsID) || !live(s.dlg.del.wsID) || !live(s.dlg.rmRoot.wsID) || !live(s.dlg.policy.wsID) {
		s.dlg.closeWorkspaceDialogs()
	}
}

// armWorkspaceReads arms one workspace detail read per workspace the model
// holds no detail for, once per trigger, and drops the guards of the reads
// whose detail has landed. TODO(F10.14): the nil guard goes when #169's
// fetcher is wired in as wsReads.
func (s *screens) armWorkspaceReads() {
	if s.wsReads == nil {
		return
	}
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

// reconnected arms the reads a fresh connection carries: the session list,
// the open session's topics, and its newest page. A request dropped while the
// host was down re-arms here.
func (s *screens) reconnected() {
	delete(s.pendingPage, s.open)
	delete(s.pendingOlder, s.open)
	s.fetcher.RequestSessions()
	if s.wsReads != nil {
		s.wsReads.RequestWorkspaces()
	}
	if s.open != "" {
		s.m.AddTopic(topicTranscript + s.open)
		s.m.AddTopic(topicTranscriptDlt + s.open)
		s.requestNewestPage(s.open)
	}
}

// applySessionEvent applies one vendor-sessions event. A created event
// carries no vendor or timestamps; the list read names them.
func (s *screens) applySessionEvent(eventType string, payload []byte) {
	if err := s.sidebar.ApplyEvent(eventType, payload); err != nil {
		s.fail(err)
		return
	}
	if eventType == session.EventSessionCreated {
		s.fetcher.RequestSessions()
	}
}

// applyDelta applies one transcript-delta delivery to the open session.
func (s *screens) applyDelta(payload []byte) {
	if t := s.transcripts[s.open]; t != nil {
		s.fail(t.ApplyDelta(payload))
	}
}

// applyTranscriptDelivery applies one durable transcript topic delivery.
func (s *screens) applyTranscriptDelivery(u hostconn.Update) {
	t := s.transcripts[s.open]
	if t == nil {
		return
	}
	if u.Snapshot != nil {
		s.fail(t.ApplySnapshot(u.Snapshot.State))
		s.requestNewestPage(s.open)
		return
	}
	if u.Event != nil {
		s.fail(t.ApplyEvent(u.Event.Type, u.Event.Sequence, u.Event.Payload))
	}
}

// applyResult folds one fetcher read into the screens.
func (s *screens) applyResult(r session.Result) {
	switch r.Kind {
	case session.ResultSessions:
		if r.Err != nil {
			s.fail(fmt.Errorf("read the session list: %w", r.Err))
			return
		}
		s.sidebar.ApplyList(r.Sessions)
		s.prune()
	case session.ResultPage:
		t := s.transcripts[r.Session]
		if t == nil {
			return
		}
		if r.Err != nil {
			s.fail(fmt.Errorf("read the transcript page: %w", r.Err))
			return
		}
		if r.Older {
			// Keep the reader's anchor: the prepended items shift the
			// content under the scroll offset by their height.
			if prepended := t.ApplyOlderPage(r.Items); prepended > 0 {
				s.viewFor(r.Session).AccountForPrepended(prepended)
			}
			s.pendingOlder[r.Session] = false
		} else {
			t.ApplyNewestPage(r.Items)
			s.pendingPage[r.Session] = false
		}
	}
}

// openSession opens a session's transcript: its topics join the connection,
// and its newest page is read if the model has none.
func (s *screens) openSession(id string) {
	if s.open == id {
		return
	}
	if s.open != "" {
		s.m.RemoveTopic(topicTranscript + s.open)
		s.m.RemoveTopic(topicTranscriptDlt + s.open)
	}
	s.open, s.scrolled = id, false
	s.viewFor(id)
	s.m.AddTopic(topicTranscript + id)
	s.m.AddTopic(topicTranscriptDlt + id)
	if _, ok := s.transcripts[id]; !ok {
		s.transcripts[id] = session.NewTranscript(id)
	}
	s.requestNewestPage(id)
}

// requestNewestPage reads the newest page once per trigger.
func (s *screens) requestNewestPage(id string) {
	if s.pendingPage[id] {
		return
	}
	s.pendingPage[id] = true
	s.fetcher.RequestPage(id, 0)
}

// maybePageOlder reads the next older page when the reader has scrolled back
// to the top: paging older is on demand, one page per arrival at the top —
// the prepended history moves the reader off the top again — and never while
// the view follows the newest item.
func (s *screens) maybePageOlder() {
	t := s.transcripts[s.open]
	view := s.viewFor(s.open)
	if t == nil || !s.scrolled || s.pendingOlder[s.open] || view.Following() || !view.AtTop() {
		return
	}
	if before, ok := t.OlderCursor(); ok {
		s.pendingOlder[s.open] = true
		s.fetcher.RequestPage(s.open, before)
	}
}

// fail records a failure the screens could not act on.
func (s *screens) fail(err error) {
	if err != nil {
		s.lastErr = err.Error()
	}
}

// layout draws the sidebar and the transcript pane.
func (s *screens) layout(gtx *layout.Context, env *kit.Env) {
	layout.Flex{Axis: layout.Horizontal}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return s.layoutSidebar(&local, env)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return s.layoutTranscript(&local, env)
		}),
	)
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

// layoutTranscript draws the open session's transcript, read-only.
func (s *screens) layoutTranscript(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	var items []*session.Item
	if t := s.transcripts[s.open]; t != nil {
		items = t.Items()
	}
	view := s.viewFor(s.open)
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			paint.FillShape(local.Ops, env.Th.Chrome().CanvasBg, clip.Rect{Max: local.Constraints.Max}.Op())
			if len(items) == 0 && s.open == "" {
				// TODO(F10.19): Move the empty state onto the kit's EmptyState (§6.25).
				return layout.Center.Layout(local, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return kit.Label(env, &l, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Select a session")
				})
			}
			dims := transcriptview.Layout(env, &local, view, items)
			s.trackScroll(view)
			s.maybePageOlder()
			return dims
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			if s.lastErr == "" {
				return layout.Dimensions{}
			}
			return kit.Label(env, &local, theme.TypeSmall(), env.Th.Status().Danger, s.lastErr)
		}),
	)
}

// trackScroll marks that the reader has scrolled, which arms paging older.
func (s *screens) trackScroll(view *transcriptview.State) {
	if view.Offset() > 0 {
		s.scrolled = true
	}
}
