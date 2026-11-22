package appwin

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/secrets"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/settings"
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
	m       *hostconn.Manager
	fetcher pageFetcher
	// poster is the composer's write side; composer holds its view state.
	poster   *session.Poster
	composer *composer

	// grid is the app shell's slot grid: the layout document, the rail, the
	// arrange and drag state, and the persistence marks (F10.20b).
	grid *slotGrid
	// slotEmpty and slotGone are the placeholder and missing-object empty
	// states the regular slots render (DA-E10).
	slotEmpty *kit.EmptyStateState
	slotGone  *kit.EmptyStateState
	// slotSoonSidebar is the rail sidebars' placeholder before their screens
	// land.
	slotSoonSidebar *kit.EmptyStateState
	// slotSessions are the sessions a regular slot already joined topics
	// for, so the layout arms each one's reads once.
	slotSessions map[string]bool

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
	workspaces     *workspace.Model
	wsMut          *workspace.Mutator
	wsReads        wsReader
	wsDetailReq    map[string]uint64
	wsHeaders      map[string]*kit.SectionHeaderState
	wsMenu         *kit.MenuState
	wsMenuFor      string
	policyBanner   *kit.BannerState
	otherHeader    *kit.SectionHeaderState
	dlg            *workspaceDialogs
	vault          *vault.Model
	vaultWorker    *vault.Worker
	vaultGate      *vaultGate
	inbox          *inbox.Model
	inboxWorker    *inbox.Worker
	inboxView      *inboxView
	inboxRow       *kit.ListRowState
	accounts       *accounts.Model
	accountsWorker *accounts.Worker
	accountsView   *accountsView
	accountsRow    *kit.ListRowState
	settings       *settings.Model
	settingsWorker *settings.Worker
	settingsView   *settingsView
	settingsRow    *kit.ListRowState
	secrets        *secrets.Model
	secretsWorker  *secrets.Worker
	secretsView    *secretsView
	secretsRow     *kit.ListRowState
	viewer         *fileViewer
	viewerWorker   *fileViewWorker
	// layoutWorker saves the layout document off the window goroutine
	// (slotpersist.go); nil in the headless tests that build no worker.
	layoutWorker    *layoutWorker
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
	secretsRow := kit.NewListRowState()
	secretsRow.Enter()
	return &screens{
		m:               m,
		fetcher:         fetcher,
		poster:          poster,
		composer:        newComposer(poster),
		grid:            newSlotGrid(),
		slotEmpty:       kit.NewEmptyStateState(),
		slotGone:        kit.NewEmptyStateState(),
		slotSoonSidebar: kit.NewEmptyStateState(),
		slotSessions:    map[string]bool{},
		sidebar:         session.NewSidebar(),
		transcripts:     map[string]*session.Transcript{},
		views:           map[string]*transcriptview.State{},
		reopenAnchor:    map[string]bool{},
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
		secrets:         secrets.NewModel(),
		secretsView:     newSecretsView(),
		secretsRow:      secretsRow,
		viewer:          newFileViewer(),
		emptyTranscript: kit.NewEmptyStateState(),
		failure:         newFailureUX(),
	}
}

// hasSlotKind reports whether a regular slot of the kind is open, the
// sidebar rows' selected state.
func (s *screens) hasSlotKind(kind SlotKind) bool {
	for _, p := range regularCells(s.grid.doc) {
		if slot, _ := slotAt(s.grid.doc, p); slot.Ref.Kind == kind {
			return true
		}
	}
	return false
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
	if s.open != id {
		if s.open != "" {
			s.m.RemoveTopic(topicTranscript + s.open)
			s.m.RemoveTopic(topicTranscriptDlt + s.open)
		}
		s.open, s.scrolled = id, false
		if _, ok := s.views[id]; !ok {
			s.reopenAnchor[id] = true
		}
		if _, ok := s.transcripts[id]; !ok {
			s.transcripts[id] = session.NewTranscript(id)
		}
		s.requestNewestPage(id)
		s.requestCommands(id)
	}
	s.ensureSessionTopics(id)
	s.grid.openMain(SlotRef{Kind: KindSession, ID: id})
}

// ensureSessionTopics joins a session's topics and arms its reads once, for
// the sessions a regular slot shows (§5.3: another session's transcript in
// its own slot).
func (s *screens) ensureSessionTopics(id string) {
	if id == "" {
		return
	}
	s.m.AddTopic(topicTranscript + id)
	s.m.AddTopic(topicTranscriptDlt + id)
	if _, ok := s.transcripts[id]; !ok {
		s.transcripts[id] = session.NewTranscript(id)
	}
	if !s.slotSessions[id] {
		s.slotSessions[id] = true
		s.requestNewestPage(id)
		s.requestCommands(id)
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
		s.grid.popover.IsOpen() || s.vaultGate.active(s.vault)
}

// layout draws the shell (F10.20b, desktop-app §5.3): the VaultGate while
// locked, then the failure banner strip, the rail, the sidebar and the slot
// grid (a Gio Flex Rigid child needs no pre-measure, so the panes shrink to
// fit).
func (s *screens) layout(gtx *layout.Context, env *kit.Env) {
	s.layoutShell(gtx, env)
}

// layoutTranscript draws the transcript pane and, under it, the composer
// row for one session: the main slot's open session, or another session's
// slot (§5.3). The split is manual — the transcript takes everything above
// the measured row — so the row's origin is known and Env.Offset can record
// it for the slash menu's anchor (UK-07).
func (s *screens) layoutTranscriptFor(gtx *layout.Context, env *kit.Env, id string) {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	rowH := s.composer.measure(env, gtx, s, id)
	pane := image.Pt(w, max(h-rowH, 0))

	var items []*session.Item
	if t := s.transcripts[id]; t != nil {
		items = t.Items()
	}
	view := s.viewFor(id)
	local := *gtx
	local.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: pane}
	s.layoutTranscriptPane(&local, env, id, view, items)
	_, connected := s.connected()
	env.Offset(gtx, image.Pt(0, pane.Y), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Min: image.Pt(w, rowH), Max: image.Pt(w, rowH)}
		s.composer.layout(env, &local, s, id, connected)
	})
}
