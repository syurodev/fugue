package main

import (
	"fmt"
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kitspike"
)

// fieldsPage is the three TextField variants plus disabled and error-shown.
type fieldsPage struct {
	text   *kitspike.TextFieldState
	search *kitspike.TextFieldState
	secret *kitspike.TextFieldState
	dis    *kitspike.TextFieldState
	err    *kitspike.TextFieldState
	events string
}

func newFieldsPage() fieldsPage {
	f := fieldsPage{
		text: kitspike.NewTextFieldState(), search: kitspike.NewTextFieldState(),
		secret: kitspike.NewTextFieldState(), dis: kitspike.NewTextFieldState(),
		err: kitspike.NewTextFieldState(),
	}
	f.err.Touch() // the error-shown example
	f.search.SetText("fugue serve --vault locked")
	f.secret.SetText("hunter2")
	return f
}

func (f *fieldsPage) layout(env *kitspike.Env, gtx *layout.Context) {
	c := env.Th.Chrome()
	w := min(gtx.Constraints.Max.X, gtx.Dp(360))
	y := kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Constraints.Max.X, "One editing core (UK-08): select, copy, cut, paste, undo, redo; IME through the OS. Errors show only after blur or submit.", c.FgMuted).Size.Y + gtx.Dp(12)

	row := func(h int, draw func(gtx *layout.Context) layout.Dimensions) {
		at(gtx, image.Pt(0, y), func(gtx *layout.Context) layout.Dimensions {
			g := *gtx
			g.Constraints.Max.X = w
			draw(&g)
			return layout.Dimensions{}
		})
		y += h
	}
	fieldH := gtx.Dp(74)
	drawField := func(st *kitspike.TextFieldState, p kitspike.FieldProps) func(gtx *layout.Context) layout.Dimensions {
		return func(gtx *layout.Context) layout.Dimensions {
			ev := st.Layout(env, gtx, p)
			f.noteEvents(ev)
			return layout.Dimensions{}
		}
	}
	row(fieldH, drawField(f.text, kitspike.FieldProps{Label: "Connection name", Placeholder: "prod-pg", Helper: "Shown in the sidebar", Err: "A connection needs a name"}))
	row(fieldH, drawField(f.search, kitspike.FieldProps{Label: "Search", Variant: kitspike.FieldSearch, Helper: "Escape clears"}))
	row(fieldH, drawField(f.secret, kitspike.FieldProps{Label: "Master password", Variant: kitspike.FieldSecret, Helper: "Never pre-filled (§6.8)"}))
	row(fieldH, drawField(f.dis, kitspike.FieldProps{Label: "Disabled", Disabled: true, Helper: "Why it is off lives here, not in a tooltip"}))
	row(fieldH, drawField(f.err, kitspike.FieldProps{Label: "Port", Err: "30000 and above are reserved", Helper: ""}))
	at(gtx, image.Pt(0, y), func(gtx *layout.Context) layout.Dimensions {
		return kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Constraints.Max.X, f.events, c.FgSubtle)
	})
}

// noteEvents keeps one line of the last field event.
func (f *fieldsPage) noteEvents(ev kitspike.FieldEvents) {
	if ev.Submitted {
		f.events = "last event: Submitted"
	} else if ev.Changed {
		f.events = "last event: Changed"
	} else if ev.FocusLost {
		f.events = "last event: FocusLost"
	}
}

// menuPage is one dropdown opener plus a second opener near the window's
// right edge, so the flip is visible, and one item with a submenu.
type menuPage struct {
	trigger  *kitspike.ButtonState
	trigger2 *kitspike.ButtonState
	items    []kitspike.MenuItem
	// triggerRect is the laid-out rect of the first trigger in window
	// coordinates, refreshed every frame.
	triggerRect image.Rectangle
}

func newMenuPage(env *kitspike.Env) menuPage {
	return menuPage{
		trigger:  kitspike.NewButtonState(),
		trigger2: kitspike.NewButtonState(),
		items: []kitspike.MenuItem{
			{Label: "Open", Icon: "copy", Shortcut: "⌘O"},
			{Label: "Connect"},
			{Label: "Export", Submenu: []kitspike.MenuItem{
				{Label: "As JSON"},
				{Label: "As YAML"},
				{Label: "As SQL"},
			}},
			{Label: "Duplicate"},
			{Label: "", Separator: true},
			{Label: "Copy path", Shortcut: "⌥⌘C"},
			{Label: "Disabled item", Disabled: true},
			{Label: "Delete", Danger: true, Shortcut: "⌫"},
		},
	}
}

// layout draws the openers; it returns the current status line.
func (m *menuPage) layout(env *kitspike.Env, gtx *layout.Context, overlay *kitspike.Overlay, status string) string {
	c := env.Th.Chrome()
	y := kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Constraints.Max.X, "Opens Snappy, closes Exit, turns round mid-way. Arrows wrap, letters typeahead, Right opens the submenu, Escape returns focus to the opener.", c.FgMuted).Size.Y + gtx.Dp(12)

	// The first trigger: near the left edge. Each button is laid out
	// exactly once per frame; the click comes out of that layout call.
	clicked1 := false
	at(gtx, image.Pt(0, y), func(gtx *layout.Context) layout.Dimensions {
		clicked1 = m.trigger.Layout(env, gtx, kitspike.ButtonProps{Text: "Actions", Icon: "chevron-down", Variant: kitspike.Secondary, Size: kitspike.SizeMD})
		return layout.Dimensions{}
	})
	// The anchor is the button's rect in window coordinates: this page
	// knows its own offsets, which is exactly the kit-v1 problem the guide
	// documents (the transform stack is not exposed).
	m.triggerRect = image.Rect(gtx.Dp(navWidth+pagePad), gtx.Dp(pagePad)+y,
		gtx.Dp(navWidth+pagePad)+gtx.Dp(120), gtx.Dp(pagePad)+y+gtx.Dp(28))
	if clicked1 {
		overlay.Open(m.items, m.triggerRect, m.trigger.Tag())
	}
	// The second trigger: pinned to the right edge so the menu flips.
	clicked2 := false
	at(gtx, image.Pt(gtx.Constraints.Max.X-gtx.Dp(120), y+gtx.Dp(60)), func(gtx *layout.Context) layout.Dimensions {
		clicked2 = m.trigger2.Layout(env, gtx, kitspike.ButtonProps{Text: "Far right", Variant: kitspike.Ghost, Size: kitspike.SizeMD})
		return layout.Dimensions{}
	})
	if clicked2 {
		x := gtx.Constraints.Max.X - gtx.Dp(120)
		anchor := image.Rect(x, gtx.Dp(pagePad)+y+gtx.Dp(60), x+gtx.Dp(120), gtx.Dp(pagePad)+y+gtx.Dp(88))
		overlay.Open(m.items, anchor, m.trigger2.Tag())
	}
	// Status line.
	at(gtx, image.Pt(0, y+gtx.Dp(120)), func(gtx *layout.Context) layout.Dimensions {
		return kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Constraints.Max.X, status, c.FgSubtle)
	})
	return status
}

// anchorForShots is the trigger's most recent window-space rect.
func (m *menuPage) anchorForShots() image.Rectangle { return m.triggerRect }

// splitPage is a two-pane split with real content on each side.
type splitPage struct {
	split *kitspike.SplitPaneState
}

func newSplitPage() splitPage {
	return splitPage{split: kitspike.NewSplitPaneState(0.4)}
}

func (s *splitPage) layout(env *kitspike.Env, gtx *layout.Context) {
	c := env.Th.Chrome()
	y := kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Constraints.Max.X, "Drag follows the pointer with no spring; double-click resets with Smooth; arrows move the focused handle 16 dp, Shift+arrow 64 dp.", c.FgMuted).Size.Y + gtx.Dp(12)
	at(gtx, image.Pt(0, y), func(gtx *layout.Context) layout.Dimensions {
		g := *gtx
		g.Constraints.Max.Y = gtx.Constraints.Max.Y - y - gtx.Dp(24)
		left := func(gtx *layout.Context) layout.Dimensions {
			r := image.Rectangle{Max: gtx.Constraints.Max}
			kitspike.FillRRect(gtx, r, gtx.Dp(6), c.SurfaceBase)
			kitspike.StrokeRRect(gtx, r, gtx.Dp(6), float32(gtx.Dp(1)), c.Border)
			at(gtx, image.Pt(gtx.Dp(12), gtx.Dp(12)), func(gtx *layout.Context) layout.Dimensions {
				return kitspike.DrawText(gtx, env.Sh, kitspike.TypeBody, gtx.Constraints.Max.X-2*gtx.Dp(12), "Editor", c.Fg)
			})
			return layout.Dimensions{Size: gtx.Constraints.Max, Baseline: 0}
		}
		right := func(gtx *layout.Context) layout.Dimensions {
			r := image.Rectangle{Max: gtx.Constraints.Max}
			kitspike.FillRRect(gtx, r, gtx.Dp(6), c.CanvasBg)
			kitspike.StrokeRRect(gtx, r, gtx.Dp(6), float32(gtx.Dp(1)), c.Border)
			at(gtx, image.Pt(gtx.Dp(12), gtx.Dp(12)), func(gtx *layout.Context) layout.Dimensions {
				return kitspike.DrawText(gtx, env.Sh, kitspike.TypeBody, gtx.Constraints.Max.X-2*gtx.Dp(12), "Companion panel", c.Fg)
			})
			return layout.Dimensions{Size: gtx.Constraints.Max, Baseline: 0}
		}
		s.split.Layout(env, &g, left, right, kitspike.SplitProps{Default: 0.4, MinLeft: 120, MinRight: 160})
		return layout.Dimensions{}
	})
	at(gtx, image.Pt(0, gtx.Constraints.Max.Y-gtx.Dp(20)), func(gtx *layout.Context) layout.Dimensions {
		return kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Constraints.Max.X,
			fmt.Sprintf("ratio %.2f", s.split.Ratio()), c.FgSubtle)
	})
}
