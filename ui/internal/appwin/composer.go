package appwin

import (
	"image"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The composer (F10.4b, desktop-app §5): the open session's multi-line IME
// input, its slash menu over the vendor and `/fugue:` groups, and the
// Send/Steer/Stop actions the session state gates. The kit pieces hold the
// widget state (UK-02); the composer decides what they mean and posts every
// write through the session.Poster, never blocking the window goroutine.
// The submit key is the composer's own (§6.9): Enter sends or steers,
// Shift+Enter inserts the newline, and with a Vietnamese IME active the
// platform consumes plain letters, which is why every app shortcut elsewhere
// uses a modifier.

// composerMaxLines caps the input box; longer drafts scroll inside it and
// ScrollToCaret keeps the caret in view (§6.9).
const composerMaxLines = 8

// The session states the composer gates on, mirroring the vendor session
// state machine's wire values; reasonVaultLocked is the one waiting reason
// the failure UX names (F10.5e).
const (
	stateReady            = "ready"
	stateRunning          = "running"
	stateAwaitingApproval = "awaiting_approval"
	stateStarting         = "starting"
	stateWaiting          = "waiting"
	stateDetached         = "detached"
	stateClosed           = "closed"

	reasonVaultLocked = "vault_locked"
)

// // composerState is one session's composer view state, kept per session like
// the transcript scroll state: the kit pieces and the last failed write.
type composerState struct {
	area   *kit.TextAreaState
	menu   *kit.SuggestListState
	send   *kit.ButtonState
	stop   *kit.ButtonState
	resume *kit.ButtonState
	close  *kit.ButtonState
	// lastErr is the write failure the composer could not act on, shown
	// under the input; a failed send puts the draft back while the user has
	// not typed since.
	lastErr string
	// menuDraft is the draft the slash menu last synced: a choice rewrites
	// the draft to the command, and the menu only reopens once the draft
	// changes again — otherwise Enter on the chosen command would choose it
	// forever instead of sending it.
	menuDraft string
}

// newComposerState builds the state of one session's composer.
func newComposerState() *composerState {
	return &composerState{
		area:   kit.NewTextAreaState(),
		menu:   kit.NewSuggestListState(),
		send:   kit.NewButtonState(),
		stop:   kit.NewButtonState(),
		resume: kit.NewButtonState(),
		close:  kit.NewButtonState(),
	}
}

// composer is the window's write side: one view state per session, the start
// flow, and the vendor capabilities the probe read reported.
type composer struct {
	states map[string]*composerState
	start  *startFlow
	poster *session.Poster
	// caps hold the probed capabilities by vendor name; a vendor without a
	// probe supports nothing, so the composer offers only what §6 declares.
	caps map[string]client.Capability
}

// newComposer builds the composer over the poster.
func newComposer(poster *session.Poster) *composer {
	return &composer{
		states: map[string]*composerState{},
		start:  newStartFlow(),
		poster: poster,
		caps:   map[string]client.Capability{},
	}
}

// stateFor returns the session's composer state, building it on first use.
func (c *composer) stateFor(id string) *composerState {
	st, ok := c.states[id]
	if !ok {
		st = newComposerState()
		c.states[id] = st
	}
	return st
}

// prune drops the state of sessions that left the list.
func (c *composer) prune(known map[string]bool) {
	for id := range c.states {
		if !known[id] {
			delete(c.states, id)
		}
	}
}

// applyProbes replaces the probed capabilities.
func (c *composer) applyProbes(probes []client.VendorProbe) {
	for _, p := range probes {
		c.caps[p.Vendor] = p.Capability
	}
}

// knowsVendor reports a probed answer for the vendor, success or not: the
// composer offers a vendor without a probe nothing beyond the base actions,
// so re-probing it on every list change would only repeat the failure.
func (c *composer) knowsVendor(vendor string) bool {
	_, ok := c.caps[vendor]
	return ok
}

// steerOK reports whether the vendor accepts input mid-turn (§6).
func (c *composer) steerOK(vendor string) bool { return c.caps[vendor].Steer }

// interruptOK reports whether the vendor stops a running turn (§6).
func (c *composer) interruptOK(vendor string) bool { return c.caps[vendor].Interrupt }

// suggestItems maps the session's command model onto the menu's rows: the
// vendor group first in vendor order, the `/fugue:` group after it (§9),
// prefix matches before substring ones within each group.
func (c *composer) suggestItems(cmds *session.Commands, query string) []kit.SuggestItem {
	sugs := cmds.Suggest(query)
	items := make([]kit.SuggestItem, 0, len(sugs))
	for _, s := range sugs {
		items = append(items, kit.SuggestItem{ID: s.ID, Label: s.ID, Secondary: s.Secondary, Disabled: s.Disabled})
	}
	return items
}

// action decides the composer's primary action for the session state: send
// on a ready session, steer on a running one the vendor steers, nothing
// otherwise.
func (c *composer) action(sess session.Session) (kind session.PostKind, ok bool) {
	switch sess.State {
	case stateReady:
		return session.PostSend, true
	case stateRunning, stateAwaitingApproval:
		if c.steerOK(sess.Vendor) {
			return session.PostSteer, true
		}
	}
	return 0, false
}

// turnActive reports a state whose turn the stop button ends.
func turnActive(state string) bool {
	return state == stateRunning || state == stateAwaitingApproval
}

// measure returns the composer row's height at the current width: the input
// box at its cap, the action row, and the write-failure line. Everything
// measures with dead input, so no widget lays out twice in a frame (UK-02).
// The target is the session the row belongs to — the open session in the
// main slot, or the slot's own session (§5.3).
func (c *composer) measure(env *kit.Env, gtx *layout.Context, sc *screens, target string) int {
	if target == "" {
		return c.start.measure(env, gtx)
	}
	st := c.stateFor(target)
	sess, known := sc.sidebar.Lookup(target)
	if !known {
		return 0
	}
	area := st.area.Measure(env, gtx, &kit.TextAreaProps{
		MaxLines: composerMaxLines, EnterSubmits: true, Helper: stateHelper(sess),
	}).Size.Y
	row := max(area, c.actionMeasure(env, gtx, st, sess))
	if st.lastErr != "" {
		line := measureLabel(env, gtx, theme.TypeSmall(), env.Th.Status().Danger, st.lastErr, gtx.Constraints.Max.X)
		row += line.Y + gtx.Dp(theme.SpaceXS)
	}
	return row
}

// actionMeasure is the action row's height: the buttons the session state
// lays out.
func (c *composer) actionMeasure(env *kit.Env, gtx *layout.Context, st *composerState, sess session.Session) int {
	kind, actionable := c.action(sess)
	sendLabel := sendLabelText
	if kind == session.PostSteer {
		sendLabel = "Steer"
	}
	row := st.send.Measure(env, gtx, kit.ButtonProps{
		Text: sendLabel, Variant: kit.Primary, Size: kit.SizeLG, Disabled: !actionable,
	}).Size.Y
	if turnActive(sess.State) && c.interruptOK(sess.Vendor) {
		stop := st.stop.Measure(env, gtx, kit.ButtonProps{
			Text: "Stop", Variant: kit.Secondary, Size: kit.SizeLG,
		}).Size.Y
		row = max(row, stop)
	}
	if sess.State == stateDetached {
		resume := st.resume.Measure(env, gtx, kit.ButtonProps{
			Text: "Resume", Variant: kit.Primary, Size: kit.SizeLG,
		}).Size.Y
		closer := st.close.Measure(env, gtx, kit.ButtonProps{
			Text: "Close", Variant: kit.Secondary, Size: kit.SizeLG,
		}).Size.Y
		row = max(row, resume, closer)
	}
	return row
}

// layout draws the composer row for one target session: the start row when
// no session is open, the input and its actions otherwise. It returns the
// row it laid out — the row height the caller allotted is the row's own (§5:
// the transcript pane gives the composer everything above the measured row).
// The caller lays it out inside Env.Offset, so the slash menu's anchor knows
// its place in the window, and passes whether the host is reachable, which
// disables the composer while it is not.
func (c *composer) layout(env *kit.Env, gtx *layout.Context, sc *screens, target string, connected bool) layout.Dimensions {
	if target == "" {
		return c.start.row(env, gtx, sc)
	}
	st := c.stateFor(target)
	sess, known := sc.sidebar.Lookup(target)
	if !known {
		return layout.Dimensions{}
	}
	disabled := !connected || sess.State == stateStarting || sess.State == stateWaiting ||
		sess.State == stateDetached || sess.State == stateClosed

	// The draft as this frame's decisions see it: the editor applies the
	// frame's typing during the layout below, so the sync at the end records
	// this snapshot, not whatever the frame's own typing produced.
	draft := st.area.Text()
	query, isSlash := session.SlashQuery(draft)
	items, chosen, ateReturn := c.syncMenu(gtx, sc, st, target, draft, query, isSlash, disabled)

	var rowDims layout.Dimensions
	row := layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			rowDims = c.layoutInput(env, &local, sc, st, sess, disabled, ateReturn)
			return rowDims
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return c.layoutErr(env, &local, st)
		}),
	)
	c.layoutMenu(env, gtx, sc, st, target, draft, query, chosen, items, rowDims)
	return row
}

// syncMenu opens the slash menu while the draft names a command the composer
// has not synced yet and the input holds focus, closes it otherwise, and
// forwards the open menu's keys (§6.14: the list never takes focus). It
// returns the suggestion rows, the command a key chose, and whether a Return
// was the menu's, so the composer keeps it from also submitting the draft.
func (c *composer) syncMenu(gtx *layout.Context, sc *screens, st *composerState, target, draft, query string, isSlash, disabled bool) ([]kit.SuggestItem, string, bool) {
	if disabled || !isSlash {
		st.menu.Close()
		return nil, "", false
	}
	items := c.suggestItems(sc.commandsFor(target), query)
	if gtx.Focused(st.area.Tag()) && draft != st.menuDraft {
		st.menu.Open()
	}
	chosen, ateReturn := c.menuKeys(gtx, st, items)
	return items, chosen, ateReturn
}

// layoutMenu draws the open menu below the input row, anchors to the row, and
// makes a choice the whole prompt (§9: a command is the entire prompt, never
// a fragment inside it).
func (c *composer) layoutMenu(env *kit.Env, gtx *layout.Context, sc *screens, st *composerState, target, draft, query, chosen string, items []kit.SuggestItem, rowDims layout.Dimensions) {
	res, err := st.menu.Layout(env, gtx, &kit.SuggestProps{
		Items:     items,
		Anchor:    image.Rect(0, 0, rowDims.Size.X, rowDims.Size.Y),
		Query:     query,
		Loading:   sc.commandsPending(target),
		EmptyText: "No commands",
		NoResText: "No matching command",
		MinWidth:  rowDims.Size.X,
	})
	if err != nil {
		sc.fail(err)
	}
	if chosen == "" {
		chosen = res.Chosen
	}
	if chosen == "" {
		st.menuDraft = draft
		return
	}
	st.area.SetText(chosen)
	st.area.MoveCaretToEnd()
	st.menuDraft = chosen
}

// menuKeys forwards the menu's keys while it is open: navigation moves the
// highlight, Return and Enter choose, Escape closes. It reports the chosen
// command and whether a Return was the menu's, so the composer keeps it from
// also submitting the draft.
func (c *composer) menuKeys(gtx *layout.Context, st *composerState, items []kit.SuggestItem) (chosen string, ateReturn bool) {
	if !st.menu.IsOpen() {
		return "", false
	}
	tag := st.area.Tag()
	filters := []event.Filter{
		key.Filter{Focus: tag, Name: key.NameUpArrow},
		key.Filter{Focus: tag, Name: key.NameDownArrow},
		key.Filter{Focus: tag, Name: key.NameHome},
		key.Filter{Focus: tag, Name: key.NameEnd},
		key.Filter{Focus: tag, Name: key.NamePageUp},
		key.Filter{Focus: tag, Name: key.NamePageDown},
		key.Filter{Focus: tag, Name: key.NameReturn},
		key.Filter{Focus: tag, Name: key.NameEnter},
		key.Filter{Focus: tag, Name: key.NameEscape},
	}
	for {
		e, ok := gtx.Event(filters...)
		if !ok {
			return chosen, ateReturn
		}
		ke, isKey := e.(key.Event)
		if !isKey || ke.State != key.Press {
			continue
		}
		id, _, handled := st.menu.HandleKey(ke, items)
		if id != "" {
			chosen = id
		}
		if handled && (ke.Name == key.NameReturn || ke.Name == key.NameEnter) {
			ateReturn = true
		}
	}
}

// layoutInput draws the input and its action buttons; the area grows to
// composerMaxLines and scrolls inside past that, following the caret.
func (c *composer) layoutInput(env *kit.Env, gtx *layout.Context, sc *screens, st *composerState, sess session.Session, disabled, ateReturn bool) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.End}.Layout(*gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			props := &kit.TextAreaProps{
				Placeholder:  "Message, / for commands",
				MaxLines:     composerMaxLines,
				EnterSubmits: true,
				Disabled:     disabled,
				Helper:       stateHelper(sess),
			}
			ev := st.area.Layout(env, &local, props)
			if ev.FocusLost {
				st.menu.Close()
			}
			if ev.Submitted && !ateReturn {
				kind, ok := c.action(sess)
				c.submit(sc, st, sess, kind, ok)
			}
			st.area.ScrollToCaret()
			// The drawn height is the measured one: the box at its cap with
			// its helper line, the number the row's own height came from.
			return st.area.Measure(env, &local, props)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layout.Inset{Left: theme.SpaceSM}.Layout(local, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return c.layoutActions(env, &l, sc, st, sess, disabled)
			})
		}),
	)
}

// submit posts the draft as the session state's action and clears it; a
// failed send puts the words back (applyPostResult). A send or steer arms
// the new-turn anchor (F10.4d): the sent message lands near the top.
func (c *composer) submit(sc *screens, st *composerState, sess session.Session, kind session.PostKind, ok bool) {
	text := strings.TrimSpace(st.area.Text())
	if !ok || text == "" {
		return
	}
	switch kind {
	case session.PostSend:
		c.poster.Send(sess.ID, text)
		sc.armTurnAnchor(sess.ID)
	case session.PostSteer:
		c.poster.Steer(sess.ID, text)
		sc.armTurnAnchor(sess.ID)
	case session.PostStart, session.PostResume, session.PostInterrupt, session.PostClose:
		// The primary action never carries these kinds: starts go through
		// the start flow, stops and closes through their own buttons.
	}
	st.area.SetText("")
	st.lastErr = ""
}

// layoutActions draws the action row: the primary action (send or steer),
// the stop button on an active turn, and the resume and close buttons on a
// detached session (F10.5e).
func (c *composer) layoutActions(env *kit.Env, gtx *layout.Context, sc *screens, st *composerState, sess session.Session, disabled bool) layout.Dimensions {
	kind, actionable := c.action(sess)
	sendLabel := sendLabelText
	if kind == session.PostSteer {
		sendLabel = "Steer"
	}
	var children []layout.FlexChild
	children = append(children, c.button(env, sc, st.send, sendLabel, kit.Primary,
		disabled || !actionable || strings.TrimSpace(st.area.Text()) == "",
		func() { c.submit(sc, st, sess, kind, actionable) }))
	if turnActive(sess.State) && c.interruptOK(sess.Vendor) {
		children = append(children, c.button(env, sc, st.stop, "Stop", kit.Secondary, disabled,
			func() { c.poster.Interrupt(sess.ID) }))
	}
	if sess.State == stateDetached {
		children = append(children,
			c.button(env, sc, st.resume, "Resume", kit.Primary, disabled,
				func() { c.poster.ResumeSession(sess.ID, client.SessionProfile{}) }),
			c.button(env, sc, st.close, "Close", kit.Secondary, disabled,
				func() { c.poster.CloseSession(sess.ID) }))
	}
	local := *gtx
	return layout.Flex{Axis: layout.Horizontal}.Layout(local, children...)
}

// button lays one action button out and runs act on its click.
func (c *composer) button(env *kit.Env, sc *screens, st *kit.ButtonState, label string, variant kit.ButtonVariant, disabled bool, act func()) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		local := gtx
		return layout.Inset{Left: theme.SpaceSM}.Layout(local, func(gtx layout.Context) layout.Dimensions {
			l := gtx
			props := kit.ButtonProps{Text: label, Variant: variant, Size: kit.SizeLG, Disabled: disabled}
			clicked, err := st.Layout(env, &l, props)
			if err != nil {
				sc.fail(err)
			}
			if clicked {
				act()
			}
			return st.Measure(env, &l, props)
		})
	})
}

// layoutErr draws the composer's own write failure under the input.
func (c *composer) layoutErr(env *kit.Env, gtx *layout.Context, st *composerState) layout.Dimensions {
	if st.lastErr == "" {
		return layout.Dimensions{}
	}
	local := *gtx
	return layout.Inset{Top: theme.SpaceXS}.Layout(local, func(gtx layout.Context) layout.Dimensions {
		l := gtx
		l.Constraints.Min.X = 0
		return kit.Label(env, &l, theme.TypeSmall(), env.Th.Status().Danger, st.lastErr)
	})
}

// stateHelper is the input's helper line for a state that cannot accept
// input yet.
func stateHelper(sess session.Session) string {
	switch sess.State {
	case stateStarting:
		return "starting…"
	case stateWaiting:
		return "waiting: " + sess.WaitingReason
	case stateDetached:
		return "detached"
	case stateClosed:
		return "closed"
	}
	return ""
}
