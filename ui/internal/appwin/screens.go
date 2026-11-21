package appwin

import (
	"fmt"
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/transcriptview"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The topic name pieces the screens route on; the constants mirror the
// host's topic owners.
const (
	topicVendorSessions = "vendor-sessions"
	topicTranscript     = "transcript:"
	topicTranscriptDlt  = "transcript-delta:"
	topicWorkspaces     = "workspaces"
	topicVault          = "vault"
	topicInteraction    = "interaction"
	topicQuota          = client.TopicQuota
	topicLogin          = client.TopicLogin
	topicAppState       = client.TopicAppState
)

// pageFetcher is the read side the screens drive: the session.Fetcher, and a
// recording fake in the tests.
type pageFetcher interface {
	RequestSessions()
	RequestPage(session string, before int64)
	RequestItem(session, itemID string)
	RequestCommands(session string)
	RequestWorkspaces()
	RequestWorkspace(workspace string, gen uint64)
	RequestProbe()
	Results() <-chan session.Result
}

// screens is the app's read and write sides (F10.4a, F10.4b): the sidebar's
// session list, the open session's transcript model, each session's
// transcript view state, and the composer. It changes only on the window
// goroutine, which applies every delivered update here.
type screens struct {
	m       *hostconn.Manager
	fetcher pageFetcher
	// poster is the composer's write side; composer holds its view state.
	poster   *session.Poster
	composer *composer

	sidebar     *session.Sidebar
	transcripts map[string]*session.Transcript
	// views holds one scroll state per session, so session A's offset never
	// leaks into session B; viewCache is the window's one shared layout
	// cache every session's list rides (F10.4a, desktop-app §7).
	views     map[string]*transcriptview.State
	viewCache *kit.VirtualHeightCache
	// commands holds one command model per session for the slash menu,
	// refreshed when the session's commands revision advances (§9).
	commands        map[string]*session.Commands
	pendingCommands map[string]bool
	// pendingWorkspaces marks the start flow's workspace list read, and
	// pendingProbe the capability probe a changed vendor list armed.
	pendingWorkspaces bool
	pendingProbe      bool
	// lastStarted is the session the composer last asked to start, opened
	// as soon as the topic announces it.
	lastStarted string
	// open is the open session's id, empty when none is.
	open string

	header *kit.SectionHeaderState
	rows   map[string]*kit.ListRowState
	// pendingPage and pendingOlder guard one read per trigger, and scrolled
	// makes paging older a user-driven act: it arms after the first scroll
	// and resets when the session changes.
	pendingPage  map[string]bool
	pendingOlder map[string]bool
	pendingItem  map[string]bool
	scrolled     bool
	// lastErr is the last read or payload failure the screens could not act
	// on, shown under the transcript.
	lastErr string

	// The workspace side (F10.14): the workspaces the topic reports, the
	// reads that fill their details, the mutations the dialogs post, and
	// the sidebar's own state per workspace.
	workspaces      *workspace.Model
	wsMut           *workspace.Mutator
	wsReads         wsReader
	wsDetailReq     map[string]uint64
	wsHeaders       map[string]*kit.SectionHeaderState
	wsMenu          *kit.MenuState
	wsMenuFor       string
	policyBanner    *kit.BannerState
	otherHeader     *kit.SectionHeaderState
	dlg             *workspaceDialogs
	vault           *vault.Model
	vaultWorker     *vault.Worker
	vaultGate       *vaultGate
	inbox           *inbox.Model
	inboxWorker     *inbox.Worker
	inboxView       *inboxView
	inboxRow        *kit.ListRowState
	inboxOpen       bool
	accounts        *accounts.Model
	accountsWorker  *accounts.Worker
	accountsView    *accountsView
	accountsRow     *kit.ListRowState
	accountsOpen    bool
	settings        *settings.Model
	settingsWorker  *settings.Worker
	settingsView    *settingsView
	settingsRow     *kit.ListRowState
	settingsOpen    bool
	emptyTranscript *kit.EmptyStateState
}

// newScreens builds the screens over the manager, its fetcher, the composer's
// poster and the workspace mutator.
func newScreens(m *hostconn.Manager, fetcher pageFetcher, poster *session.Poster, wsMut *workspace.Mutator) *screens {
	inboxRow := kit.NewListRowState()
	inboxRow.Enter()
	accountsRow := kit.NewListRowState()
	accountsRow.Enter()
	settingsRow := kit.NewListRowState()
	settingsRow.Enter()
	return &screens{
		m:               m,
		fetcher:         fetcher,
		poster:          poster,
		composer:        newComposer(poster),
		sidebar:         session.NewSidebar(),
		transcripts:     map[string]*session.Transcript{},
		views:           map[string]*transcriptview.State{},
		viewCache:       kit.NewVirtualHeightCache(),
		commands:        map[string]*session.Commands{},
		pendingCommands: map[string]bool{},
		header:          kit.NewSectionHeaderState(true),
		rows:            map[string]*kit.ListRowState{},
		pendingPage:     map[string]bool{}, pendingOlder: map[string]bool{}, pendingItem: map[string]bool{},
		workspaces:      workspace.NewModel(),
		wsMut:           wsMut,
		wsReads:         fetcher,
		wsDetailReq:     map[string]uint64{},
		wsHeaders:       map[string]*kit.SectionHeaderState{},
		wsMenu:          kit.NewMenuState(),
		policyBanner:    kit.NewBannerState(),
		otherHeader:     kit.NewSectionHeaderState(false),
		dlg:             newWorkspaceDialogs(),
		vault:           vault.NewModel(),
		vaultGate:       newVaultGate(),
		inbox:           inbox.NewModel(),
		inboxView:       newInboxView(),
		inboxRow:        inboxRow,
		accounts:        accounts.NewModel(),
		accountsView:    newAccountsView(),
		accountsRow:     accountsRow,
		settings:        settings.NewModel(),
		settingsView:    newSettingsView(),
		settingsRow:     settingsRow,
		emptyTranscript: kit.NewEmptyStateState(),
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

// update folds one frame's deliveries into the screens. The loops index
// rather than copy: the delivery and result structs are wide, and the
// window's hot path pays the copy every frame.
func (s *screens) update(updates []hostconn.Update, results []session.Result, posts []session.PostResult, muts []workspace.MutResult) {
	for i := range updates {
		s.applyUpdate(updates[i])
	}
	for i := range results {
		s.applyResult(results[i])
	}
	for i := range posts {
		s.applyPostResult(posts[i])
	}
	for i := range muts {
		s.dlg.apply(&muts[i])
	}
	s.armWorkspaceReads()
}

// applyUpdate routes one topic delivery or connection-state change.
func (s *screens) applyUpdate(u hostconn.Update) {
	if u.State == hostconn.StateConnected {
		s.reconnected()
		return
	}
	switch u.Topic {
	case topicVendorSessions:
		s.applyVendorSessionsDelivery(u)
	case topicWorkspaces:
		s.applyWorkspaceDelivery(u)
	case topicVault:
		s.applyVaultDelivery(u)
	case topicInteraction:
		s.applyInteractionDelivery(u)
	case topicQuota:
		s.applyQuotaDelivery(u)
	case topicLogin:
		s.applyLoginDelivery(u)
	case topicAppState:
		s.applyAppStateDelivery(u)
	case topicTranscriptDlt + s.open:
		if u.Event != nil {
			s.applyDelta(u.Event.Payload)
		}
	case topicTranscript + s.open:
		s.applyTranscriptDelivery(u)
	}
}

func (s *screens) applyVendorSessionsDelivery(u hostconn.Update) {
	if u.Snapshot != nil {
		s.fail(s.sidebar.ApplySnapshot(u.Snapshot.State))
		s.prune()
	} else if u.Event != nil {
		s.applySessionEvent(u.Event.Type, u.Event.Payload)
	}
}

// reconnected arms the reads a fresh connection carries: the session list,
// the workspace list, the vendors' capabilities, the open session's topics,
// its newest page and its commands. A request dropped while the host was
// down re-arms here.
func (s *screens) reconnected() {
	delete(s.pendingPage, s.open)
	delete(s.pendingOlder, s.open)
	s.pendingItem = map[string]bool{}
	s.wsDetailReq = map[string]uint64{}
	s.fetcher.RequestSessions()
	s.wsReads.RequestWorkspaces()
	s.fetcher.RequestProbe()
	if s.vaultWorker != nil {
		req := vault.Request{Kind: vault.OpStatus}
		s.vaultWorker.Do(&req)
	}
	if s.inboxWorker != nil {
		req := inbox.Request{Kind: inbox.OpListPending}
		s.inboxWorker.Do(&req)
	}
	if s.accountsWorker != nil {
		req := accounts.Request{Kind: accounts.OpListAccounts}
		s.accountsWorker.Do(&req)
	}
	if s.settingsWorker != nil {
		req := settings.Request{Kind: settings.OpLoadPrefs}
		s.settingsWorker.Do(&req)
	}
	if s.open != "" {
		s.m.AddTopic(topicTranscript + s.open)
		s.m.AddTopic(topicTranscriptDlt + s.open)
		s.requestNewestPage(s.open)
		s.requestCommands(s.open)
	}
}

// applySessionEvent applies one vendor-sessions event. A created event
// carries no vendor or timestamps; the list read names them. The session the
// composer just started opens as soon as its created event names it.
func (s *screens) applySessionEvent(eventType string, payload []byte) {
	if err := s.sidebar.ApplyEvent(eventType, payload); err != nil {
		s.fail(err)
		return
	}
	switch eventType {
	case session.EventSessionCreated:
		s.fetcher.RequestSessions()
		if s.lastStarted != "" {
			if _, ok := s.sidebar.Lookup(s.lastStarted); ok {
				s.openSession(s.lastStarted)
				s.lastStarted = ""
			}
		}
	case session.EventSessionStateChanged:
		// The state change carries the commands revision; a list older than
		// what the topic announces is re-read for the open session (§9).
		if sess, ok := s.sidebar.Lookup(s.open); ok {
			if cmds := s.commands[s.open]; cmds == nil || sess.CommandsRevision > cmds.Revision {
				s.requestCommands(s.open)
			}
		}
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

// applyResult folds one fetcher read into the screens: the session list and
// transcript pages here, the composer's reads in applyComposerResult.
func (s *screens) applyResult(r session.Result) {
	switch r.Kind {
	case session.ResultSessions:
		if r.Err != nil {
			s.fail(fmt.Errorf("read the session list: %w", r.Err))
			return
		}
		s.sidebar.ApplyList(r.Sessions)
		s.prune()
		s.probeMissingVendors()
	case session.ResultPage:
		s.applyPageResult(&r)
	case session.ResultItem:
		delete(s.pendingItem, r.Session+":"+r.Detail.Item.ItemID)
		if r.Err != nil {
			s.fail(fmt.Errorf("read the transcript item: %w", r.Err))
			return
		}
		if t := s.transcripts[r.Session]; t != nil {
			t.ApplyItemDetail(&r.Detail)
		}
	case session.ResultCommands, session.ResultProbe, session.ResultWorkspaces, session.ResultWorkspace:
		s.applyComposerResult(r)
	}
}

func (s *screens) applyPageResult(r *session.Result) {
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
		return
	}
	t.ApplyNewestPage(r.Items)
	s.pendingPage[r.Session] = false
}

// openSession opens a session's transcript: its topics join the connection,
// and its newest page and commands are read if the model has none.
func (s *screens) openSession(id string) {
	s.showPane(paneTranscript)
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
	s.requestCommands(id)
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

// layout draws the VaultGate while locked, or the sidebar and transcript pane.
func (s *screens) layout(gtx *layout.Context, env *kit.Env) {
	if s.vaultGate.active(s.vault) {
		s.vaultGate.layout(gtx, env, s)
		return
	}
	s.toggleInboxShortcut(gtx)
	s.toggleAccountsShortcut(gtx)
	s.toggleSettingsShortcut(gtx)
	layout.Flex{Axis: layout.Horizontal}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return s.layoutSidebar(&local, env)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			// TODO(F10.20b): move the inbox, accounts, and settings views into slots
			if s.inboxOpen {
				return s.inboxView.layout(&local, env, s)
			}
			if s.accountsOpen {
				return s.accountsView.layout(&local, env, s)
			}
			if s.settingsOpen {
				return s.settingsView.layout(&local, env, s)
			}
			return s.layoutTranscript(&local, env)
		}),
	)
}

// layoutTranscript draws the transcript pane and, under it, the composer
// row. The split is manual — the transcript takes everything above the
// measured row — so the row's origin is known and Env.Offset can record it
// for the slash menu's anchor (UK-07).
func (s *screens) layoutTranscript(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	rowH := s.composer.measure(env, gtx, s)
	pane := image.Pt(w, max(h-rowH, 0))

	var items []*session.Item
	if t := s.transcripts[s.open]; t != nil {
		items = t.Items()
	}
	view := s.viewFor(s.open)
	local := *gtx
	local.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: pane}
	s.layoutTranscriptPane(&local, env, view, items)
	_, connected := s.connected()
	env.Offset(gtx, image.Pt(0, pane.Y), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Min: image.Pt(w, rowH), Max: image.Pt(w, rowH)}
		s.composer.layout(env, &local, s, connected)
	})
	return layout.Dimensions{Size: gtx.Constraints.Max}
}
