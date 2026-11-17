package kit

import (
	"image"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// ListItem pairs a stable object Key with its frame ListRowProps (§6.20, §6.26).
type ListItem struct {
	Key   string
	Props ListRowProps
}

// ListEvents reports list-level selection or activation in the frame.
type ListEvents struct {
	Selected      string
	Opened        string
	ActionKey     string
	ActionID      string
	ContextChosen string
	Changed       bool
}

// ListState coordinates selection, keyboard navigation (Up/Down/Enter/Menu),
// keyed row states, and the Smooth sliding selection pill across ListRows.
type ListState struct {
	selected string
	pill     slidingPill
	rows     map[string]*ListRowState
}

// NewListState builds a ListState with no row selected.
func NewListState() *ListState {
	return &ListState{pill: newSlidingPill(), rows: map[string]*ListRowState{}}
}

// Selected returns the selected item key, or "" if none.
func (l *ListState) Selected() string { return l.selected }

// SetSelected updates the selected item key.
func (l *ListState) SetSelected(itemKey string) { l.selected = itemKey }

// Row returns the ListRowState for itemKey, creating it on first use.
func (l *ListState) Row(itemKey string) *ListRowState {
	if l.rows == nil {
		l.rows = map[string]*ListRowState{}
	}
	rs, ok := l.rows[itemKey]
	if !ok {
		rs = NewListRowState()
		l.rows[itemKey] = rs
	}
	return rs
}

// Enter starts the insert animation on the keyed row.
func (l *ListState) Enter(itemKey string) { l.Row(itemKey).Enter() }

// Remove starts the removal animation on the keyed row.
func (l *ListState) Remove(itemKey string) { l.Row(itemKey).Remove() }

// Layout lays out items vertically, records each key's real y and height,
// slides the shared selected pill to the selected key's measured rect on Smooth,
// and handles Up/Down keyboard navigation.
func (l *ListState) Layout(env *Env, gtx *layout.Context, items []ListItem) (layout.Dimensions, ListEvents) {
	ev := ListEvents{Selected: l.selected}
	w := gtx.Constraints.Max.X

	l.pollArrows(gtx, items, &ev)

	m := op.Record(gtx.Ops)
	y, selRect, hasSel := l.layoutRows(env, gtx, items, &ev)
	call := m.Stop()

	l.pruneGone(items)
	l.pill.step(env, selRect, hasSel)
	l.pill.draw(gtx, gtx.Dp(theme.RadiusMD), env.Th.Chrome().SelectedWash)
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: image.Pt(w, y)}, ev
}

func (l *ListState) pruneGone(items []ListItem) {
	live := make(map[string]bool, len(items))
	for i := range items {
		live[items[i].Key] = true
	}
	for k, rs := range l.rows {
		if !live[k] && (!rs.removing || rs.Removed()) {
			delete(l.rows, k)
		}
	}
}

func (l *ListState) pollArrows(gtx *layout.Context, items []ListItem, ev *ListEvents) {
	for i := range items {
		rs := l.Row(items[i].Key)
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
		if next := l.nextEnabled(items, idx, dir); next >= 0 && items[next].Key != l.selected {
			l.selected = items[next].Key
			ev.Selected = l.selected
			ev.Changed = true
			gtx.Execute(key.FocusCmd{Tag: &l.Row(l.selected).tag})
		}
	}
}

func (l *ListState) layoutRows(env *Env, gtx *layout.Context, items []ListItem, ev *ListEvents) (y int, selRect image.Rectangle, hasSel bool) {
	for i := range items {
		item := items[i]
		rs := l.Row(item.Key)
		p := item.Props
		p.Selected = item.Key == l.selected
		var dims layout.Dimensions
		var rev ListRowEvents
		at(gtx, image.Pt(0, y), func(gtx *layout.Context) {
			dims, rev = rs.layoutRow(env, gtx, &p, true)
		})
		if p.Selected && !p.Disabled && dims.Size.Y > 0 {
			selRect = pillRect(gtx, image.Rect(0, y, dims.Size.X, y+dims.Size.Y))
			hasSel = true
		}
		l.foldRowEvents(env, item.Key, rev, ev)
		y += dims.Size.Y
	}
	return y, selRect, hasSel
}

func (l *ListState) foldRowEvents(env *Env, itemKey string, rev ListRowEvents, ev *ListEvents) {
	if rev.Clicked && itemKey != l.selected {
		l.selected = itemKey
		ev.Selected = itemKey
		ev.Changed = true
		env.Keep()
	}
	if rev.Opened {
		ev.Opened = itemKey
	}
	if rev.Action != "" {
		ev.ActionKey = itemKey
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
