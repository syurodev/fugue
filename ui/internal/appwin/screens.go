package appwin

import (
	"image"
	"maps"
	"runtime"
	"slices"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/secrets"
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
	topicSecrets        = client.TopicSecrets
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
// transcript view state, the composer, and the slot grid of F10.20b that
// arranges them (desktop-app §5.3). It changes only on the window
// goroutine, which applies every delivered update here.
type screens struct {
	teamRuntime
	// titleBar is the app-drawn title bar of platforms that give the window
	// none (FUG-58); nil in tests and wherever the platform draws it.
	titleBar *titleBar
	// deviceMetric is the frame's system metric before env.UIScale is applied.
	deviceMetric unit.Metric
	// pendingMaximize applies app.Maximized.Option() after the window driver is
	// initialized so Gio records the normal window size before maximizing
	// (Wayland's Gio driver panics on unmaximize if Maximized is in initialOpts).
	pendingMaximize           bool
	restoreNormalOnUnmaximize bool
	sawMaximized              bool
	pendingNormalRestore      bool
	skipGeometry              bool
	restoreNormalAttempts     int
	maximizedPxSize           image.Point
	restoreNormalSize         WindowGeometry
	m                         *hostconn.Manager
	fetcher                   pageFetcher
	// poster is the composer's write side; composer holds its view state.
	poster   *session.Poster
	composer *composer

	// grid is the app shell's slot grid: the layout document, the rail, the
	// arrange and drag state, and the persistence marks (F10.20b).
	grid *slotGrid
	// slotEmpty and slotGone are the per-slot placeholder and missing-object
	// empty states keyed by keyOfRef (kind:id) (DA-E10).
	slotEmpty map[string]*kit.EmptyStateState
	slotGone  map[string]*kit.EmptyStateState
	// slotSoonSidebar is the rail sidebars' placeholder before their screens
	// land.
	slotSoonSidebar *kit.EmptyStateState
	// Each window owns its interests even when other windows need the same topics.
	setTopic     func(topic string, active bool)
	slotSessions map[string]bool
	// trackedGen is the grid's docGen at the last trackTopics run.
	trackedGen  uint64
	trackedOnce bool

	sidebar        *session.Sidebar
	sessionsLoaded bool
	transcripts    map[string]*session.Transcript
	// views holds one scroll state per session, so session A's offset never
	// leaks into session B; viewCache is the window's one shared layout
	// cache every session's list rides (F10.4a, desktop-app §7).
	views     map[string]*transcriptview.State
	viewCache *kit.VirtualHeightCache

	connDotTag     struct{}
	connDotHovered bool
	connDotHasPtr  bool
	connDotPtr     image.Point
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
	// open is the open session's id — the main slot's session while the
	// chat rail is selected (§5.3) — empty when none is.
	open string
	// The scroll anchors of F10.4d: reopenAnchor marks sessions whose fresh
	// view still needs the last-turn-user-message anchor once the newest
	// page lands, and the turn fields arm the new-turn anchor a composer
	// send requests — the anchor fires when the sent message's item lands.
	reopenAnchor map[string]bool
	turnArm      bool
	turnSeq      uint64
	turnAnchored string

	wsAddBtn *kit.IconButtonState
	rows     map[string]*kit.ListRowState
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
	workspaces   *workspace.Model
	wsMut        *workspace.Mutator
	wsReads      wsReader
	wsDetailReq  map[string]uint64
	wsHeaders    map[string]*kit.SectionHeaderState
	wsMenu       *kit.MenuState
	wsMenuFor    string
	policyBanner *kit.BannerState
	otherHeader  *kit.SectionHeaderState
	dlg          *workspaceDialogs
	vault        *vault.Model
	vaultWorker  *vault.Worker
	vaultGate    *vaultGate
	// hostState and hostReason are the last connection state the window
	// goroutine folded in; the vault gate reads them (empty until the first
	// update, which the gate treats as still connecting).
	hostState      hostconn.State
	hostReason     string
	inbox          *inbox.Model
	inboxWorker    *inbox.Worker
	inboxView      *inboxView
	accounts       *accounts.Model
	accountsWorker *accounts.Worker
	accountsView   *accountsView
	settings       *settings.Model
	settingsWorker *settings.Worker
	settingsView   *settingsView
	secrets        *secrets.Model
	secretsWorker  *secrets.Worker
	secretsView    *secretsView
	viewer         *fileViewer
	viewerWorker   *fileViewWorker
	// layoutWorker saves the layout document off the window goroutine
	// (slotpersist.go); nil in the headless tests that build no worker.
	layoutWorker    *layoutWorker
	emptyTranscript *kit.EmptyStateState
	// failure is the window's failure UX (F10.5e): the actionable banners and
	// the active-session close confirmation.
	failure       *failureUX
	onThemeToggle func()
}

// newScreens builds the screens over the manager, its fetcher, the composer's
// poster and the workspace mutator.
func newScreens(m *hostconn.Manager, fetcher pageFetcher, poster *session.Poster, wsMut *workspace.Mutator) *screens {
	return &screens{
		titleBar:        newTitleBar(runtime.GOOS),
		teamRuntime:     newTeamRuntime(),
		m:               m,
		fetcher:         fetcher,
		poster:          poster,
		composer:        newComposer(poster),
		grid:            newSlotGrid(),
		slotEmpty:       map[string]*kit.EmptyStateState{},
		slotGone:        map[string]*kit.EmptyStateState{},
		slotSoonSidebar: kit.NewEmptyStateState(),
		slotSessions:    map[string]bool{},
		sidebar:         session.NewSidebar(),
		transcripts:     map[string]*session.Transcript{},
		views:           map[string]*transcriptview.State{},
		reopenAnchor:    map[string]bool{},
		viewCache:       kit.NewVirtualHeightCache(),
		commands:        map[string]*session.Commands{},
		pendingCommands: map[string]bool{},
		wsAddBtn:        kit.NewIconButtonState(),
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
		accounts:        accounts.NewModel(),
		accountsView:    newAccountsView(),
		settings:        settings.NewModel(),
		settingsView:    newSettingsView(),
		secrets:         secrets.NewModel(),
		secretsView:     newSecretsView(),
		viewer:          newFileViewer(),
		emptyTranscript: kit.NewEmptyStateState(),
		failure:         newFailureUX(),
	}
}

// hasSlotKind reports whether a regular slot of the kind is open, the
// Right Stripe and Top Bar icons' active state.
func (s *screens) hasSlotKind(kind SlotKind) bool {
	for _, p := range regularCells(s.grid.doc) {
		if slot, _ := slotAt(s.grid.doc, p); slot.Ref.Kind == kind {
			return true
		}
	}
	return false
}

func (s *screens) toggleThemeFromUI(darkNow bool) {
	if darkNow {
		s.settings.Prefs.Current.ThemeMode = settings.ThemeLight
	} else {
		s.settings.Prefs.Current.ThemeMode = settings.ThemeDark
	}
	if s.onThemeToggle != nil {
		s.onThemeToggle()
	}
}

func (s *screens) activeSessionParts() (wsName, sessName string) {
	main := s.grid.mainRef()
	if main.Kind == KindSession && main.ID != "" {
		if sess, ok := s.sidebar.Lookup(main.ID); ok {
			sessName = sessionName(sess)
			if sess.WorkspaceID != "" {
				if ws, ok := s.workspaces.Lookup(sess.WorkspaceID); ok {
					wsName = ws.Name
				}
			}
			return wsName, sessName
		}
	}
	return "", ""
}

func (s *screens) topBarContext() string {
	wsName, sessName := s.activeSessionParts()
	switch {
	case wsName != "" && sessName != "":
		return wsName + " / " + sessName
	case sessName != "":
		return sessName
	}
	switch s.grid.rail {
	case railTeam:
		return teamLabel
	case railModules:
		return "Modules"
	}
	if s.titleBar != nil && s.titleBar.title != "" {
		return s.titleBar.title
	}
	return "Fugue"
}

func (s *screens) statusBarBreadcrumb() string {
	wsName, sessName := s.activeSessionParts()
	switch {
	case wsName != "" && sessName != "":
		return wsName + " > " + sessName
	case sessName != "":
		return sessName
	}
	switch s.grid.rail {
	case railTeam:
		return teamLabel
	case railModules:
		return "Modules"
	}
	return ""
}

// viewFor returns the session's scroll state, building it on first use. A new
// state follows the newest item until its reopen anchor lands (F10.4d).
func (s *screens) viewFor(id string) *transcriptview.State {
	view, ok := s.views[id]
	if !ok {
		view = transcriptview.NewState(s.viewCache)
		s.views[id] = view
	}
	return view
}

// openSession opens a session in the main slot (§5.3): its topics join the
// connection, its newest page and commands are read if the model has none,
// and the shell swaps the main slot's content in place. A first open arms
// the reopen anchor — the view lands at the last turn's user message when
// the page arrives, not at the bottom (desktop-app v0.9 §5.4). Returning to
// an already-open session keeps the reader's place: their choice wins.
func (s *screens) openSession(id string) {
	s.grid.openMain(SlotRef{Kind: KindSession, ID: id})
	s.setOpenSession(id)
	visible := s.visibleSessions()
	s.releaseSlotSessions(visible)
	s.ensureSessionTopics(id)
}

func (s *screens) setOpenSession(id string) {
	if s.open == id {
		return
	}
	s.open, s.scrolled, s.turnArm = id, false, false
	if id != "" {
		if _, ok := s.views[id]; !ok {
			s.reopenAnchor[id] = true
		}
	}
}

func (s *screens) visibleSessions() map[string]bool {
	visible := make(map[string]bool)
	for c := range s.grid.doc.Columns {
		for _, slot := range s.grid.doc.Columns[c].Slots {
			ref := slot.Ref
			if ref.Kind != KindSession || ref.ID == "" {
				continue
			}
			if !s.missingTarget(ref) {
				visible[ref.ID] = true
			}
		}
	}
	return visible
}

// trackTopics reconciles restored, rearranged, and closed slots before their
// models consume or draw any more transcript updates.
func (s *screens) trackTopics() {
	s.trackedGen, s.trackedOnce = s.grid.docGen, true
	visible := s.visibleSessions()
	main := s.grid.mainRef()
	if main.Kind == KindSession && visible[main.ID] {
		s.setOpenSession(main.ID)
	} else {
		s.setOpenSession("")
	}
	s.releaseSlotSessions(visible)
	for _, id := range slices.Sorted(maps.Keys(visible)) {
		s.ensureSessionTopics(id)
	}
}

func (s *screens) releaseSlotSessions(visible map[string]bool) {
	for id := range s.slotSessions {
		if !visible[id] {
			s.setSessionTopics(id, false)
			delete(s.slotSessions, id)
		}
	}
}

func (s *screens) ensureSessionTopics(id string) {
	if id == "" || s.slotSessions[id] {
		return
	}
	s.slotSessions[id] = true
	if _, ok := s.transcripts[id]; !ok {
		s.transcripts[id] = session.NewTranscript(id)
	}
	s.setSessionTopics(id, true)
	s.requestNewestPage(id)
	s.requestCommands(id)
}

func (s *screens) setSessionTopics(id string, active bool) {
	for _, prefix := range []string{topicTranscript, topicTranscriptDlt} {
		topic := prefix + id
		switch {
		case s.setTopic != nil:
			s.setTopic(topic, active)
		case active:
			s.m.AddTopic(topic)
		default:
			s.m.RemoveTopic(topic)
		}
	}
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

// anyFloatLayerOpen reports whether a dialog or a kit floating layer owns
// the window right now, so the shell's plain Escape does not act beneath
// them (§7: Escape closes the topmost layer first).
func (s *screens) anyFloatLayerOpen() bool {
	return s.dlg.anyOpen() || s.composer.start.dialog.IsOpen() || s.failure.closeDlg.dialog.IsOpen() ||
		s.grid.arrangeMenu.IsOpen() || s.vaultGate.active(s.vault)
}

// layout draws the shell (F10.20b, desktop-app §5.3): the VaultGate while
// locked, then the failure banner strip, the rail, the sidebar and the slot
// grid (a Gio Flex Rigid child needs no pre-measure, so the panes shrink to
// fit).
func (s *screens) layout(gtx *layout.Context, env *kit.Env) {
	s.layoutShell(gtx, env)
	s.releaseTeamTopics(gtx)
	s.pruneSlotEmptyStates()
}

// layoutTranscript draws the transcript pane and, under it, the composer
// row for one session: the main slot's open session, or another session's
// slot (§5.3). The split is manual — the transcript takes everything above
// the measured row — so the row's origin is known and Env.Offset can record
// it for the slash menu's anchor (UK-07).
func (s *screens) layoutTranscriptFor(gtx *layout.Context, env *kit.Env, id string) {
	s.layoutSessionWithComposer(gtx, env, id, false)
}

func (s *screens) layoutSessionWithComposer(gtx *layout.Context, env *kit.Env, id string, main bool) {
	s.layoutSessionPane(gtx, env, id, main, s.connected())
}

func (s *screens) layoutSessionPane(gtx *layout.Context, env *kit.Env, id string, main, connected bool) {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	if main && id == "" {
		s.layoutMainEmptyState(gtx, env)
		return
	}
	measure := *gtx
	_, leads := s.leadTeam(id)
	leadComposer := leads && main
	measureWidth, boxHeight := w, 0
	if leadComposer {
		measureWidth = max(teamChatWidth(gtx)-2*gtx.Dp(theme.SpaceLG), 0)
		boxHeight = gtx.Dp(3*theme.SpaceLG+theme.SpaceXS) + gtx.Sp(theme.TypeCaption().Line)
	}
	measure.Constraints.Min = image.Pt(measureWidth, 0)
	measure.Constraints.Max.X = measureWidth
	rowH := s.composer.measure(env, &measure, s, id) + boxHeight
	pane := image.Pt(w, max(h-rowH, 0))

	local := *gtx
	local.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: pane}
	if main {
		s.layoutTeamSessionContent(&local, env, id)
	} else {
		s.layoutChatContent(&local, env, id)
	}
	env.Offset(gtx, image.Pt(0, pane.Y), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Min: image.Pt(w, rowH), Max: image.Pt(w, rowH)}
		if leadComposer {
			s.layoutTeamLeadComposer(&local, env, id, connected)
		} else {
			s.composer.layout(env, &local, s, id, connected)
		}
	})
}

func (s *screens) layoutChatContent(gtx *layout.Context, env *kit.Env, id string) {
	var items []*session.Item
	if t := s.transcripts[id]; t != nil {
		items = t.Items()
	}
	s.layoutTranscriptPane(gtx, env, id, s.viewFor(id), items)
}
