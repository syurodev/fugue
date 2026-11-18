package kit

import (
	"image"
	"image/color"
	"math"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

func (s *TreeViewState) layoutTreeRow(env *Env, gtx *layout.Context, r *flatTreeRow, loader TreeLoader, ev *TreeViewEvents) (layout.Dimensions, bool) {
	w := gtx.Constraints.Max.X
	fullH := gtx.Dp(theme.RowDense)
	h := rowHeight(fullH, r.alpha)
	if h <= 0 {
		return layout.Dimensions{Size: image.Pt(w, 0)}, false
	}

	rowBox := image.Rect(0, 0, w, h)
	pill := pillRect(gtx, image.Rect(0, 0, w, fullH))
	radius := gtx.Dp(theme.RadiusMD)
	ns := s.node(r.key)

	if r.alpha < 1 {
		defer clip.Rect(rowBox).Push(gtx.Ops).Pop()
		defer op.Offset(image.Pt(0, (h-fullH)/2)).Push(gtx.Ops).Pop()
	}
	if r.isLoading || r.isError {
		s.layoutSpecialRow(env, gtx, pill, fullH, r, ns)
		return layout.Dimensions{Size: image.Pt(w, h)}, false
	}

	indent := r.depth * gtx.Dp(theme.SpaceLG)
	chevSz := gtx.Dp(theme.IconXS)
	chevX := pill.Min.X + gtx.Dp(theme.SpaceMD) + indent
	chevBox := image.Rect(chevX-gtx.Dp(theme.SpaceXXS), pill.Min.Y, chevX+chevSz+gtx.Dp(theme.SpaceXXS), pill.Min.Y+fullH)

	s.pollRowPointer(env, gtx, pill, chevBox, r, loader, ev)
	s.paintTreeRow(env, gtx, pill, radius, fullH, indent, r, ns, ev)

	selected := r.key == s.selected && !r.node.Disabled
	return layout.Dimensions{Size: image.Pt(w, h)}, selected
}

func (s *TreeViewState) layoutSpecialRow(env *Env, gtx *layout.Context, pill image.Rectangle, fullH int, r *flatTreeRow, ns *treeNodeState) {
	s.drawTreeGuides(env, gtx, pill, fullH, r.depth, r.alpha)
	if r.isLoading {
		s.drawLoadingRow(env, gtx, pill, fullH, r.depth, ns, r.alpha)
		return
	}
	s.drawErrorRow(env, gtx, pill, fullH, r.depth, r.errText, r.alpha)
}

func (s *TreeViewState) paintTreeRow(env *Env, gtx *layout.Context, pill image.Rectangle, radius, fullH, indent int, r *flatTreeRow, ns *treeNodeState, ev *TreeViewEvents) {
	hov := !r.node.Disabled && (ns.hovered || ns.chevHover || env.previewHover(&ns.tag))
	foc := !r.node.Disabled && (env.FocusVisible(gtx, &ns.tag) || env.previewFocus(&ns.tag))
	hovA := followSmooth(env, &ns.hoverF, boolFloat(hov))
	focA := followSmooth(env, &ns.focusF, boolFloat(foc))
	if hovA > 0 {
		fillLayer(gtx, pill, radius, env.Th.Chrome().HoverWash, hovA*r.alpha)
	}
	s.drawTreeGuides(env, gtx, pill, fullH, r.depth, r.alpha)
	s.drawTreeContent(env, gtx, pill, fullH, indent, r, ns, ev)
	if focA > 0 {
		insetFocusRing(gtx, pill, radius, env.Th.Chrome().FocusBorder, focA*r.alpha)
	}
}

func (s *TreeViewState) drawTreeGuides(env *Env, gtx *layout.Context, pill image.Rectangle, h, depth int, alpha float32) {
	if depth <= 0 || alpha <= 0 {
		return
	}
	step := gtx.Dp(theme.SpaceLG)
	padX := gtx.Dp(theme.SpaceMD)
	chevHalf := gtx.Dp(theme.IconXS) / 2
	lineW := max(gtx.Dp(theme.Hairline), 1)
	col := fade(env.Th.Chrome().BorderSubtle, alpha)
	for d := range depth {
		x := pill.Min.X + padX + d*step + chevHalf
		r := image.Rect(x, pill.Min.Y, x+lineW, pill.Min.Y+h)
		paint.FillShape(gtx.Ops, col, clip.Rect(r).Op())
	}
}

func (s *TreeViewState) drawLoadingRow(env *Env, gtx *layout.Context, pill image.Rectangle, h, depth int, ns *treeNodeState, alpha float32) {
	c := env.Th.Chrome()
	x := pill.Min.X + gtx.Dp(theme.SpaceMD) + depth*gtx.Dp(theme.SpaceLG)
	sz := gtx.Dp(theme.IconXS)
	at(gtx, image.Pt(x, pill.Min.Y+(h-sz)/2), func(gtx *layout.Context) {
		ns.spinner.Layout(env, gtx, sz, fade(c.FgMuted, alpha))
	})
	x += sz + gtx.Dp(theme.SpaceSM)
	pt := image.Pt(x, pill.Min.Y+centerLabelY(env, gtx, theme.TypeSmall(), h))
	drawRunAt(env, gtx, pt, max(pill.Max.X-x-gtx.Dp(theme.SpaceMD), 0), h, theme.TypeSmall(), fade(c.FgMuted, alpha), "Loading…", true)
}

func (s *TreeViewState) drawErrorRow(env *Env, gtx *layout.Context, pill image.Rectangle, h, depth int, msg string, alpha float32) {
	if msg == "" {
		msg = "Failed to load"
	}
	x := pill.Min.X + gtx.Dp(theme.SpaceMD) + depth*gtx.Dp(theme.SpaceLG)
	pt := image.Pt(x, pill.Min.Y+centerLabelY(env, gtx, theme.TypeSmall(), h))
	col := fade(env.Th.Status().Danger, alpha)
	drawRunAt(env, gtx, pt, max(pill.Max.X-x-gtx.Dp(theme.SpaceMD), 0), h, theme.TypeSmall(), col, msg, true)
}

func (s *TreeViewState) drawTreeContent(env *Env, gtx *layout.Context, pill image.Rectangle, h, indent int, r *flatTreeRow, ns *treeNodeState, ev *TreeViewEvents) {
	fg, muted, iconInk := treeColors(env, r.node.Disabled, r.alpha)
	padX := gtx.Dp(theme.SpaceMD)
	gap := gtx.Dp(theme.SpaceSM)
	x := pill.Min.X + padX + indent
	right := pill.Max.X - padX

	chevPx := gtx.Dp(theme.IconXS)
	if r.hasChildren {
		ang := followSmooth(env, &ns.chevF, boolFloat(ns.expanded)) * math.Pi / 2
		pt := image.Pt(x, pill.Min.Y+(h-chevPx)/2)
		if err := drawRotatedIcon(env, gtx, pt, IconChevronRight, theme.IconXS, iconInk, ang); err != nil && ev.Err == nil {
			ev.Err = err
		}
	}
	x += chevPx + gap

	if r.node.Icon != "" {
		iconPx := gtx.Dp(theme.IconXS)
		pt := image.Pt(x, pill.Min.Y+(h-iconPx)/2)
		if err := drawIconAt(env, gtx, pt, r.node.Icon, theme.IconXS, iconInk); err != nil && ev.Err == nil {
			ev.Err = err
		}
		x += iconPx + gap
	}

	right = drawRowTrailing(env, gtx, pill, h, right, gap, r.node.Trailing, muted)
	if x < right && r.node.Label != "" {
		pt := image.Pt(x, pill.Min.Y+centerLabelY(env, gtx, theme.TypeBody(), h))
		drawRunAt(env, gtx, pt, max(right-x, 0), h, theme.TypeBody(), fg, r.node.Label, true)
	}
}

func treeColors(env *Env, disabled bool, alpha float32) (fg, muted, iconInk color.NRGBA) {
	c := env.Th.Chrome()
	if disabled {
		return fade(c.FgDisabled, alpha), fade(c.FgDisabled, alpha), fade(c.FgDisabled, alpha)
	}
	return fade(c.Fg, alpha), fade(c.FgMuted, alpha), fade(c.IconMuted, alpha)
}

func (s *TreeViewState) pollRowPointer(env *Env, gtx *layout.Context, pill, chevBox image.Rectangle, r *flatTreeRow, loader TreeLoader, ev *TreeViewEvents) {
	ns := s.node(r.key)
	if r.node.Disabled {
		ns.hovered, ns.chevHover, ns.pressed = false, false, false
		return
	}
	s.pollRowArea(env, gtx, pill, r, ns, ev)
	if r.hasChildren {
		s.pollChevronArea(env, gtx, chevBox, r, ns, loader, ev)
	}
}

func (s *TreeViewState) pollRowArea(env *Env, gtx *layout.Context, pill image.Rectangle, r *flatTreeRow, ns *treeNodeState, ev *TreeViewEvents) {
	defer clip.Rect(pill).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &ns.tag)
	pointer.CursorPointer.Add(gtx.Ops)
	for {
		e, ok := gtx.Event(
			pointer.Filter{Target: &ns.tag, Kinds: pointer.Enter | pointer.Leave | pointer.Press | pointer.Release | pointer.Cancel},
			key.FocusFilter{Target: &ns.tag},
		)
		if !ok {
			return
		}
		if pe, isPtr := e.(pointer.Event); isPtr {
			s.handleRowPointer(env, gtx, pe, r, ev)
		}
	}
}

func (s *TreeViewState) pollChevronArea(env *Env, gtx *layout.Context, chevBox image.Rectangle, r *flatTreeRow, ns *treeNodeState, loader TreeLoader, ev *TreeViewEvents) {
	defer clip.Rect(chevBox).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &ns.chevTag)
	pointer.CursorPointer.Add(gtx.Ops)
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &ns.chevTag, Kinds: pointer.Enter | pointer.Leave | pointer.Press | pointer.Cancel})
		if !ok {
			return
		}
		pe, isPtr := e.(pointer.Event)
		if !isPtr {
			continue
		}
		s.handleChevronPointer(env, pe, r, ns, loader, ev)
	}
}

func (s *TreeViewState) handleChevronPointer(env *Env, pe pointer.Event, r *flatTreeRow, ns *treeNodeState, loader TreeLoader, ev *TreeViewEvents) {
	if pe.Kind == pointer.Enter {
		ns.chevHover = true
		return
	}
	if pe.Kind == pointer.Leave || pe.Kind == pointer.Cancel {
		ns.chevHover = false
		return
	}
	if pe.Kind == pointer.Press && pe.Buttons.Contain(pointer.ButtonPrimary) {
		s.toggleBranch(env, &r.node, loader, !ns.expanded, ev)
	}
}

func (s *TreeViewState) handleRowPointer(env *Env, gtx *layout.Context, pe pointer.Event, r *flatTreeRow, ev *TreeViewEvents) {
	ns := s.node(r.key)
	if pe.Kind == pointer.Enter {
		ns.hovered = true
		return
	}
	if pe.Kind == pointer.Leave || pe.Kind == pointer.Cancel {
		ns.hovered, ns.pressed = false, false
		return
	}
	if pe.Kind == pointer.Press && pe.Buttons.Contain(pointer.ButtonPrimary) {
		ns.pressed = true
		gtx.Execute(key.FocusCmd{Tag: &ns.tag})
		return
	}
	if pe.Kind == pointer.Release && ns.pressed {
		ns.pressed = false
		if r.key != s.selected {
			s.selected = r.key
			ev.Selected = r.key
			ev.Changed = true
		}
		now := env.Now()
		if s.lastClickK == r.key && !s.lastClickT.IsZero() && now.Sub(s.lastClickT) <= treeDoubleClick {
			ev.Opened = r.key
			s.lastClickK = ""
			s.lastClickT = time.Time{}
			return
		}
		s.lastClickK = r.key
		s.lastClickT = now
	}
}
