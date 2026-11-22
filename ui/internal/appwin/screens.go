package appwin

import (
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
	viewer          *fileViewer
	viewerWorker    *fileViewWorker
	emptyTranscript *kit.EmptyStateState
	// failure is the window's failure UX (F10.5e): the actionable banners and
	// the active-session close confirmation.
	failure *failureUX
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
		viewer:          newFileViewer(),
		emptyTranscript: kit.NewEmptyStateState(),
		failure:         newFailureUX(),
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

// layout draws the VaultGate while locked, or the failure banner strip, the
// sidebar and the transcript pane (F10.5e puts the strip above the panes; a
// Gio Flex Rigid child needs no pre-measure, so the panes shrink to fit).
func (s *screens) layout(gtx *layout.Context, env *kit.Env) {
	if s.vaultGate.active(s.vault) {
		s.vaultGate.layout(gtx, env, s)
		return
	}
	s.toggleInboxShortcut(gtx)
	s.toggleAccountsShortcut(gtx)
	s.toggleSettingsShortcut(gtx)
	layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return s.failure.layout(env, &local, s)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			layout.Flex{Axis: layout.Horizontal}.Layout(local,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Right: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						local := gtx
						return s.layoutSidebar(&local, env)
					})
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					local := gtx
					// TODO(F10.20b): move the inbox, accounts, settings, and file viewer views into slots
					if s.viewer.open {
						return s.viewer.layout(&local, env, s)
					}
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
			return layout.Dimensions{Size: gtx.Constraints.Max}
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
