package mdview

import (
	"image"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The code block's input handling (F10.4c): pointer hover/press/scroll and
// the focused keyboard scrolling, shared by code, terminal, Output and diff
// blocks.

func (v *view) pollBlockEvents(env *kit.Env, gtx *layout.Context, st *CodeBlockState) {
	if !st.CopiedUntil.IsZero() {
		if env.Now().Before(st.CopiedUntil) {
			env.Driver.WakeAt(st.CopiedUntil)
		} else {
			st.CopiedUntil = time.Time{}
		}
	}
	for {
		ev, ok := gtx.Event(
			pointer.Filter{Target: &st.focusTag, Kinds: pointer.Enter | pointer.Leave | pointer.Press | pointer.Cancel | pointer.Scroll, ScrollX: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}},
			key.FocusFilter{Target: &st.focusTag},
			key.Filter{Focus: &st.focusTag, Name: key.NameLeftArrow},
			key.Filter{Focus: &st.focusTag, Name: key.NameRightArrow},
			key.Filter{Focus: &st.focusTag, Name: key.NameUpArrow},
			key.Filter{Focus: &st.focusTag, Name: key.NameDownArrow},
			key.Filter{Focus: &st.focusTag, Name: key.NamePageUp},
			key.Filter{Focus: &st.focusTag, Name: key.NamePageDown},
			key.Filter{Focus: &st.focusTag, Name: key.NameHome},
			key.Filter{Focus: &st.focusTag, Name: key.NameEnd},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case pointer.Event:
			v.handleBlockPointer(gtx, st, e)
		case key.Event:
			if e.State == key.Press {
				v.handleBlockKey(gtx, st, e)
			}
		}
	}
}

func (v *view) handleBlockPointer(gtx *layout.Context, st *CodeBlockState, e pointer.Event) {
	switch e.Kind {
	case pointer.Enter:
		st.hovered = true
	case pointer.Leave, pointer.Cancel:
		st.hovered = false
	case pointer.Press:
		if e.Buttons.Contain(pointer.ButtonPrimary) {
			gtx.Execute(key.FocusCmd{Tag: &st.focusTag})
		}
	case pointer.Scroll:
		if st.maxScrollX > 0 && e.Scroll.X != 0 {
			st.ScrollX = min(max(st.ScrollX+int(e.Scroll.X), 0), st.maxScrollX)
		}
		if st.maxScrollY > 0 && e.Scroll.Y != 0 {
			st.ScrollY = min(max(st.ScrollY+int(e.Scroll.Y), 0), st.maxScrollY)
		}
	case pointer.Move, pointer.Release, pointer.Drag:
	}
}

func (v *view) handleBlockKey(gtx *layout.Context, st *CodeBlockState, e key.Event) {
	stepX := max(gtx.Dp(theme.SpaceXXL), 1)
	stepY := max(gtx.Sp(theme.TypeCode().Line), 1)
	switch string(e.Name) {
	case string(key.NameLeftArrow):
		st.ScrollX = max(st.ScrollX-stepX, 0)
	case string(key.NameRightArrow):
		st.ScrollX = min(st.ScrollX+stepX, st.maxScrollX)
	case string(key.NameUpArrow):
		st.ScrollY = max(st.ScrollY-stepY, 0)
	case string(key.NameDownArrow):
		st.ScrollY = min(st.ScrollY+stepY, st.maxScrollY)
	case string(key.NamePageUp):
		st.ScrollY = max(st.ScrollY-max(st.viewH, stepY), 0)
	case string(key.NamePageDown):
		st.ScrollY = min(st.ScrollY+max(st.viewH, stepY), st.maxScrollY)
	case string(key.NameHome):
		st.ScrollX = 0
	case string(key.NameEnd):
		st.ScrollX = st.maxScrollX
	}
}

func (v *view) registerBlockInput(gtx *layout.Context, st *CodeBlockState, box image.Rectangle) {
	area := clip.Rect(box).Push(gtx.Ops)
	event.Op(gtx.Ops, &st.focusTag)
	area.Pop()
}
