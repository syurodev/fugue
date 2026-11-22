package appwin

import (
	"fmt"
	"time"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The failure UX (F10.5e, desktop-app §6, activation §3): every failure the
// read model reports shows as an actionable banner or dialog that names what
// happened and the action that helps, never a generic error. The window-level
// banners cover the vendor CLI problems the probe reports (missing or
// unsupported CLI, signed-out CLI) and the lost host connection with its data
// directory; the detached session's resume and close actions live with the
// composer, a paused account shows in the start dialog before a doomed
// submit, and the vault gate names the sessions waiting for it. The `task
// unknown` recovery UI awaits teams.v1 (F11.7).

// bannerSpec is one failure banner's computed view: the state renders it, the
// tests assert on it.
type bannerSpec struct {
	st          *kit.BannerState
	kind        kit.BannerKind
	title       string
	message     string
	actionLabel string
	dismissible bool
	visible     bool
}

// failureUX holds the window-level failure banners: the kit states, the probe
// answers the specs derive from, and the last-window close confirmation. It
// changes only on the window goroutine.
type failureUX struct {
	conn      *kit.BannerState
	probeFail *kit.BannerState
	version   *kit.BannerState
	login     *kit.BannerState
	detached  *kit.BannerState
	// carry is what the last probe read reported: the failure it ended with,
	// or the answers worth a banner (an untested version, a signed-out CLI).
	// A read replaces the carry wholesale — the probe is the authority on the
	// CLIs.
	carry probeCarry
	// dismissals pin a dismissed warning banner to the answer that showed it:
	// the banner returns when a later read reports a different vendor or
	// version, and never ghosts back mid-answer.
	versionDismiss bool
	versionShown   string
	loginDismiss   bool
	loginShown     string
	closeDlg       closeConfirmDialog
}

// probeCarry is the last probe read's banner-worthy result.
type probeCarry struct {
	// failedErr is the probe read's failure, empty on success: the vendor CLI
	// problem the host answered with, path- and credential-free by contract.
	failedErr string
	// versionVendor, version and versionWarning name a probe whose version
	// sits outside the tested range, the warning verbatim.
	versionVendor, version, versionWarning string
	// loggedOutVendor names a probe whose CLI reports it is signed out.
	loggedOutVendor string
}

// versionKey renders the answer a version dismissal is pinned to.
func (c probeCarry) versionKey() string { return c.versionVendor + ":" + c.version }

// loginKey renders the answer a login dismissal is pinned to.
func (c probeCarry) loginKey() string { return c.loggedOutVendor }

func newFailureUX() *failureUX {
	return &failureUX{
		conn:      kit.NewBannerState(),
		probeFail: kit.NewBannerState(),
		version:   kit.NewBannerState(),
		login:     kit.NewBannerState(),
		detached:  kit.NewBannerState(),
		closeDlg:  newCloseConfirmDialog(),
	}
}

// applyProbe folds one probe read into the carry: a failure names the CLI
// problem, answers name the version and sign-in warnings. The first answer
// that carries each warning wins; stage A probes one vendor unless the
// session list grew more.
func (f *failureUX) applyProbe(probes []client.VendorProbe, err error) {
	c := probeCarry{}
	if err != nil {
		c.failedErr = err.Error()
	} else {
		for _, p := range probes {
			if c.versionVendor == "" && !p.VersionTested && p.VersionWarning != "" {
				c.versionVendor, c.version, c.versionWarning = p.Vendor, p.Version, p.VersionWarning
			}
			if c.loggedOutVendor == "" && p.Login == client.AccountLoginLoggedOut {
				c.loggedOutVendor = p.Vendor
			}
		}
	}
	f.carry = c
	// The shown keys move here, with the reset: a changed answer
	// un-dismisses its banner — the dismissal belonged to the old answer,
	// not to the topic — so specs stays side-effect-free for the layout.
	if f.versionShown != c.versionKey() {
		f.versionDismiss = false
	}
	f.versionShown = c.versionKey()
	if f.loginShown != c.loginKey() {
		f.loginDismiss = false
	}
	f.loginShown = c.loginKey()
}

// specs derives the visible window-level banners in display order: the lost
// connection first, then the CLI problems, then the open session's detached
// banner. reason is the connection state's own reason and dataDir the host
// data directory the banner names for a manual `fugue serve` start. hasOpen
// names whether a session is open, in openSess. It reads no clock and
// mutates nothing: the dismissals the specs reflect are folded in by
// applyEvents and applyProbe.
func (f *failureUX) specs(state hostconn.State, reason, dataDir string, hasOpen bool, openSess session.Session) []bannerSpec {
	specs := make([]bannerSpec, 0, 4)
	if state != hostconn.StateConnected {
		msg := "Fugue reconnects on its own and replays what it missed."
		if reason != "" {
			msg = reason + ". " + msg
		}
		if dataDir != "" {
			msg += " Data directory: " + dataDir
		}
		specs = append(specs, bannerSpec{
			st: f.conn, kind: kit.BannerDanger,
			title:   "Connection to the Fugue core lost",
			message: msg, visible: true,
		})
	}
	if f.carry.failedErr != "" {
		specs = append(specs, bannerSpec{
			st: f.probeFail, kind: kit.BannerDanger,
			title:   "Vendor CLI unavailable",
			message: f.carry.failedErr + " Install or upgrade the vendor CLI; Fugue probes it again when the session list changes.",
			visible: true,
		})
		return specs
	}
	if f.carry.versionVendor != "" {
		specs = append(specs, bannerSpec{
			st: f.version, kind: kit.BannerWarning,
			title:       f.carry.versionVendor + " " + f.carry.version + " is outside the tested range",
			message:     f.carry.versionWarning,
			dismissible: true,
			visible:     !f.versionDismiss,
		})
	}
	if f.carry.loggedOutVendor != "" {
		specs = append(specs, bannerSpec{
			st: f.login, kind: kit.BannerWarning,
			title:       f.carry.loggedOutVendor + " is not signed in",
			message:     "Sessions need a signed-in vendor CLI. Sign in from Accounts with the CLI's own login flow.",
			actionLabel: "Open Accounts",
			dismissible: true,
			visible:     !f.loginDismiss,
		})
	}
	if hasOpen && openSess.State == stateDetached {
		specs = append(specs, bannerSpec{
			st: f.detached, kind: kit.BannerWarning,
			title:       "Session detached",
			message:     "The host restarted while this session ran. Resume continues it with the vendor's own resume; Close in the composer ends it for good.",
			actionLabel: "Resume session",
			visible:     true,
		})
	}
	return specs
}

// layout draws the visible banners stacked at the top of the window and folds
// their events in, returning the stack's dimensions.
func (f *failureUX) layout(env *kit.Env, gtx *layout.Context, sc *screens) layout.Dimensions {
	state, reason := sc.m.State()
	sess, hasOpen := sc.sidebar.Lookup(sc.open)
	specs := f.specs(state, reason, sc.m.DataDir(), hasOpen, sess)
	if len(specs) == 0 {
		return layout.Dimensions{}
	}
	children := make([]layout.FlexChild, 0, len(specs))
	for _, spec := range specs {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layout.Inset{Bottom: theme.SpaceXXS}.Layout(local, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				dims, ev, err := layoutBannerSpec(env, &l, spec)
				if err != nil {
					sc.fail(err)
				}
				f.applyEvents(spec, ev, sc)
				return dims
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

// applyEvents folds one banner's frame events in: a dismissal pins to the
// answer on show, the needs-login action routes to the accounts pane (which
// owns the vendor's own login flow, F10.5c), and the detached action resumes
// the open session.
func (f *failureUX) applyEvents(spec bannerSpec, ev kit.BannerEvents, sc *screens) {
	if ev.Dismissed {
		switch spec.st {
		case f.version:
			f.versionDismiss = true
		case f.login:
			f.loginDismiss = true
		}
	}
	if !ev.ActionClicked {
		return
	}
	switch spec.st {
	case f.login:
		sc.openAccounts()
	case f.detached:
		if sess, ok := sc.sidebar.Lookup(sc.open); ok && sess.State == stateDetached {
			sc.poster.ResumeSession(sess.ID, client.SessionProfile{})
		}
	}
}

// layoutBannerSpec lays one banner out, the shared renderer for the window's
// failure banners.
func layoutBannerSpec(env *kit.Env, gtx *layout.Context, spec bannerSpec) (layout.Dimensions, kit.BannerEvents, error) {
	dims, ev, err := spec.st.Layout(env, gtx, kit.BannerProps{
		Kind: spec.kind, Title: spec.title, Message: spec.message,
		ActionLabel: spec.actionLabel, Dismissible: spec.dismissible, Visible: spec.visible,
	})
	if err != nil {
		return dims, ev, fmt.Errorf("layout the failure banner: %w", err)
	}
	return dims, ev, nil
}

// openAccounts is the needs-login banner's action: the accounts pane owns the
// vendor's own login flow (F10.5c).
func (s *screens) openAccounts() {
	s.accountsOpen = true
	s.inboxOpen = false
	s.settingsOpen = false
}

// activeSessionCount counts the sessions whose turn is live, the count the
// close confirmation speaks about (DA-E08).
func activeSessionCount(sessions []session.Session) int {
	n := 0
	for _, sess := range sessions {
		if turnActive(sess.State) {
			n++
		}
	}
	return n
}

// requestWindowClose runs one attempt to close the window: a close that
// would leave live turns opens the close confirmation instead, and the
// caller's close action runs only when nothing asks. It reports whether the
// close may proceed; a false means the dialog decides what happens next.
func requestWindowClose(sc *screens, closeFn func()) bool {
	active := activeSessionCount(sc.sidebar.List())
	if sc.failure.closeDlg.shouldAsk(active) {
		sc.failure.closeDlg.open(active)
		return false
	}
	closeFn()
	return true
}

// closeConfirmDialog asks before the last window closes while a session's
// turn still runs (DA-E08): the safe choice is cancel, and confirming
// re-triggers the close the dialog interrupted.
type closeConfirmDialog struct {
	dialog        *kit.DialogState
	cancel, close *kit.ButtonState
	// requestClose is the window's close action the event loop installs; the
	// confirmed close performs it again.
	requestClose func()
	// active is the count the dialog opened on, and armed marks a confirmed
	// close: the next destroy event passes instead of re-asking.
	active int
	armed  bool
}

func newCloseConfirmDialog() closeConfirmDialog {
	return closeConfirmDialog{
		dialog: kit.NewDialogState(),
		cancel: kit.NewButtonState(),
		close:  kit.NewButtonState(),
	}
}

// shouldAsk reports a destroy the dialog must interrupt: sessions run and the
// user has not confirmed this close yet.
func (d *closeConfirmDialog) shouldAsk(active int) bool {
	return !d.armed && active > 0
}

// open arms the dialog on the current count.
func (d *closeConfirmDialog) open(active int) {
	d.active = active
	d.dialog.Open()
}

// confirmed folds the primary in: arm the close, then re-trigger it through
// the window action.
func (d *closeConfirmDialog) confirmed() {
	d.armed = true
	d.dialog.Close()
	if d.requestClose != nil {
		d.requestClose()
	}
}

// cancelled folds the safe choice in: the dialog closes and the next destroy
// asks again.
func (d *closeConfirmDialog) cancelled() { d.dialog.Close() }

// layout draws the dialog over the window while it is open, naming the live
// count the ask is about.
func (d *closeConfirmDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	word := "sessions"
	if d.active == 1 {
		word = "session"
	}
	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:        kit.DialogConfirm,
		Title:          fmt.Sprintf("%d %s still running", d.active, word),
		Description:    "Closing the last window leaves the running turns without a client. The host keeps them running until it stops.",
		Cancel:         d.cancel,
		Primary:        d.close,
		PrimaryLabel:   "Close anyway",
		PrimaryVariant: kit.Danger,
	})
	if err != nil {
		sc.fail(err)
	}
	switch {
	case res.Submitted:
		d.confirmed()
	case res.Cancelled:
		d.cancelled()
	}
}

// vaultWaitingCount counts the sessions the vault lock suspends, the count
// the unlock card shows (activation §3: `Waiting(vault_locked)` with the
// number of waiting runs).
func vaultWaitingCount(sessions []session.Session) int {
	n := 0
	for _, sess := range sessions {
		if sess.State == stateWaiting && sess.WaitingReason == reasonVaultLocked {
			n++
		}
	}
	return n
}

// formatPausedUntil renders a vendor-reported pause reset for the start
// dialog's paused line; an absent or unparsable time renders empty, never a
// guess (desktop-app §6: the reset time is the vendor's to report).
func formatPausedUntil(pausedUntil string) string {
	t, err := time.Parse(time.RFC3339Nano, pausedUntil)
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02 15:04 MST")
}
