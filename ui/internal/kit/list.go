package kit

import (
	"image"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// ListState coordinates selection, keyboard navigation (Up/Down/Enter/Menu),
// and the Smooth sliding selection pill across a list of ListRows.
type ListState struct {
	selected int
	pill     slidingPill
	rows     []*ListRowState
}

// NewListState builds a ListState with no row selected (-1).
func NewListState() *ListState {
	return &ListState{selected: -1, pill: newSlidingPill()}
}

// Selected returns the selected row index, or -1 if none.
func (l *ListState) Selected() int { return l.selected }

// SetSelected updates the selected row index.
func (l *ListState) SetSelected(idx int) { l.selected = idx }

// Row returns the ListRowState at index i, growing as needed.
func (l *ListState) Row(i int) *ListRowState {
	for len(l.rows) <= i {
		l.rows = append(l.rows, NewListRowState())
	}
	return l.rows[i]
}

// ListItem pairs a ListRowProps with its key for ListState.Layout.
type ListItem struct {
	Props ListRowProps
}

// ListEvents reports list-level selection or activation in the frame.
type ListEvents struct {
	Selected      int
	Opened        int
	ActionIndex   int
	ActionID      string
	ContextChosen string
	Changed       bool
}

// Layout lays out items vertically, sliding the shared selected pill on Smooth
// and handling Up/Down keyboard navigation.
func (l *ListState) Layout(env *Env, gtx *layout.Context, items []ListItem) (layout.Dimensions, ListEvents) {
	ev := ListEvents{Selected: l.selected, Opened: -1, ActionIndex: -1}
	w := gtx.Constraints.Max.X
	rowH := gtx.Dp(theme.Row)

	l.pollArrows(gtx, items, &ev)

	var target image.Rectangle
	hasSel := l.selected >= 0 && l.selected < len(items) && !items[l.selected].Props.Disabled
	if hasSel {
		y := l.selected * rowH
		target = pillRect(gtx, image.Rect(0, y, w, y+rowH))
	}
	l.pill.step(env, target, hasSel)
	l.pill.draw(gtx, gtx.Dp(theme.RadiusMD), env.Th.Chrome().SelectedWash)

	y := l.layoutRows(env, gtx, items, &ev)
	return layout.Dimensions{Size: image.Pt(w, y)}, ev
}

func (l *ListState) pollArrows(gtx *layout.Context, items []ListItem, ev *ListEvents) {
	for i := range items {
		rs := l.Row(i)
		if !gtx.Focused(&rs.tag) {
			continue
		}
		l.stepArrowKeys(gtx, items, i, &rs.tag, ev)
		return
	}
}

func (l *ListState) stepArrowKeys(gtx *layout.Context, items []ListItem, idx int, tag *bool, ev *ListEvents) {
	for {
		ke, ok := gtx.Event(
			key.Filter{Focus: tag, Name: key.NameUpArrow},
			key.Filter{Focus: tag, Name: key.NameDownArrow},
		)
		if !ok {
			return
		}
		k, isKey := ke.(key.Event)
		if !isKey || k.State != key.Press {
			continue
		}
		dir := 1
		if k.Name == key.NameUpArrow {
			dir = -1
		}
		if next := l.nextEnabled(items, idx, dir); next >= 0 && next != l.selected {
			l.selected = next
			ev.Selected = next
			ev.Changed = true
			gtx.Execute(key.FocusCmd{Tag: &l.Row(next).tag})
		}
	}
}

func (l *ListState) layoutRows(env *Env, gtx *layout.Context, items []ListItem, ev *ListEvents) int {
	y := 0
	for i := range items {
		rs := l.Row(i)
		p := items[i].Props
		p.Selected = i == l.selected
		var dims layout.Dimensions
		var rev ListRowEvents
		at(gtx, image.Pt(0, y), func(gtx *layout.Context) {
			dims, rev = rs.layoutRow(env, gtx, &p, true)
		})
		l.foldRowEvents(env, i, rev, ev)
		y += dims.Size.Y
	}
	return y
}

func (l *ListState) foldRowEvents(env *Env, i int, rev ListRowEvents, ev *ListEvents) {
	if rev.Clicked && i != l.selected {
		l.selected = i
		ev.Selected = i
		ev.Changed = true
		env.Keep()
	}
	if rev.Opened {
		ev.Opened = i
	}
	if rev.Action != "" {
		ev.ActionIndex = i
		ev.ActionID = rev.Action
	}
	if rev.ContextChosen != "" {
		ev.ContextChosen = rev.ContextChosen
	}
}

func (l *ListState) nextEnabled(items []ListItem, from, dir int) int {
	for i := from + dir; i >= 0 && i < len(items); i += dir {
		if !items[i].Props.Disabled {
			return i
		}
	}
	return from
}
