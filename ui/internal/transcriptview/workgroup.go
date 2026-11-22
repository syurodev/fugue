package transcriptview

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type workGroupState struct {
	click     widget.Clickable
	anim      expandAnim
	titleFade motion.Follower
	lastTitle string
	// wasRunning is the running flag of the previous frame: the running→done
	// edge fires the auto-collapse. It rides the payload cache, so a group
	// that leaves the render window for longer than the cache's bound may
	// miss the edge once — the expanded value itself never moves.
	wasRunning bool
	subClicks  map[string]*widget.Clickable
	subAnims   map[string]*expandAnim
}

func groupStateOf(st *State, key string) *workGroupState {
	return kit.PayloadOf(st.list, key, func() *workGroupState {
		f := motion.NewFollower(motion.Smooth())
		f.Snap(1)
		return &workGroupState{
			anim:      newExpandAnim(),
			titleFade: f,
			subClicks: map[string]*widget.Clickable{},
			subAnims:  map[string]*expandAnim{},
		}
	})
}

func (g *workGroupState) subClick(key string) *widget.Clickable {
	cl, ok := g.subClicks[key]
	if !ok {
		cl = &widget.Clickable{}
		g.subClicks[key] = cl
	}
	return cl
}

func (g *workGroupState) subAnim(key string) *expandAnim {
	a, ok := g.subAnims[key]
	if !ok {
		na := newExpandAnim()
		a = &na
		g.subAnims[key] = a
	}
	return a
}

func layoutWorkGroup(env *kit.Env, gtx *layout.Context, st *State, grp *workGroup, cb Callbacks) layout.Dimensions {
	gs := groupStateOf(st, grp.key)
	clicked := gs.click.Clicked(*gtx)
	if clicked {
		st.toggleUserChoice(grp.key)
	} else if !st.userChose(grp.key) {
		applyGroupAutoRules(st, gs, grp)
	}
	gs.wasRunning = grp.running
	expanded := st.list.Expanded(grp.key)
	vis, prog := gs.anim.step(env, expanded, clicked)

	headerText := workGroupHeaderText(grp)
	if gs.lastTitle == "" {
		gs.lastTitle = headerText
		gs.titleFade.Snap(1)
	} else if headerText != gs.lastTitle {
		gs.lastTitle = headerText
		if grp.running && !env.ReducedMotion {
			gs.titleFade.Snap(0)
		} else {
			gs.titleFade.Snap(1)
		}
	}
	if env.ReducedMotion {
		gs.titleFade.Snap(1)
	} else {
		gs.titleFade.Follow(1, env.DT())
		if gs.titleFade.Moving() {
			env.Keep()
		}
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return gs.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return layoutWorkGroupHeader(env, &local, headerText, grp.running, expanded, gs.titleFade.Value())
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layoutAnimatedCollapse(&local, vis, prog, func(gtx *layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceXS}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return layoutWorkRail(env, &l, func(gtx *layout.Context) layout.Dimensions {
						return layoutSubGroups(env, gtx, st, gs, grp.subGroups, cb)
					})
				})
			})
		}),
	)
}

// applyGroupAutoRules applies the v0.9 auto rules to one work group: a
// running group opens, and it closes when the turn finishes. The caller has
// already stood them down for a group the reader chose by hand.
func applyGroupAutoRules(st *State, gs *workGroupState, grp *workGroup) {
	if grp.running && !st.list.Expanded(grp.key) {
		st.list.SetExpanded(grp.key, true)
	} else if gs.wasRunning && !grp.running && st.list.Expanded(grp.key) {
		st.list.SetExpanded(grp.key, false)
	}
}

func layoutWorkRail(env *kit.Env, gtx *layout.Context, body func(*layout.Context) layout.Dimensions) layout.Dimensions {
	rec := op.Record(gtx.Ops)
	dims := layout.Inset{Left: theme.SpaceMD}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		l := gtx
		return body(&l)
	})
	call := rec.Stop()
	if dims.Size.Y > 0 {
		railW := max(gtx.Dp(theme.Hairline), 1)
		railX := gtx.Dp(theme.SpaceXS)
		rail := image.Rect(railX, 0, railX+railW, dims.Size.Y)
		// BorderSubtle vanishes on the light canvas; the rail is a divider, so it
		// uses the divider token.
		paint.FillShape(gtx.Ops, env.Th.Chrome().Border, clip.Rect(rail).Op())
	}
	call.Add(gtx.Ops)
	return dims
}

func workGroupHeaderText(grp *workGroup) string {
	if grp.running {
		if grp.currentTitle != "" {
			return grp.currentTitle
		}
		return "Working…"
	}
	if dur := formatDuration(grp.durationMs); dur != "" {
		return "Worked for " + dur
	}
	return "Worked"
}

func layoutWorkGroupHeader(env *kit.Env, gtx *layout.Context, title string, running, expanded bool, alpha float32) layout.Dimensions {
	ch := env.Th.Chrome()
	caret := kit.IconChevronRight
	if expanded {
		caret = kit.IconChevronDown
	}
	fg := ch.FgMuted
	if alpha < 1 {
		a := uint8(float32(fg.A) * min(max(alpha, 0.25), 1))
		fg.A = a
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !running {
				return layout.Dimensions{}
			}
			return layout.Inset{Right: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				// Running indicator uses type.small label dot/label.
				l := gtx
				return kit.Label(env, &l, theme.TypeSmall(), env.Th.Status().Info, "Working")
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return kit.Label(env, &l, theme.TypeSmall(), fg, title)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				d, err := env.Icon(&l, caret, theme.IconXS, ch.FgSubtle)
				logTVErr(err)
				return d
			})
		}),
	)
}

func layoutSubGroups(env *kit.Env, gtx *layout.Context, st *State, gs *workGroupState, sgs []subGroup, cb Callbacks) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(sgs))
	for i := range sgs {
		sg := &sgs[i]
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if len(sg.items) == 1 {
				return layout.Inset{Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return layoutWorkMember(env, &l, st, sg.items[0], cb)
				})
			}
			return layout.Inset{Bottom: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return layoutOneSubGroup(env, &l, st, gs, sg, cb)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func layoutOneSubGroup(env *kit.Env, gtx *layout.Context, st *State, gs *workGroupState, sg *subGroup, cb Callbacks) layout.Dimensions {
	cl := gs.subClick(sg.key)
	clicked := cl.Clicked(*gtx)
	if clicked {
		// The fold state rides the list's per-key state, not the payload: a
		// sub-group the reader folded keeps its fold when the group leaves
		// the render window and comes back (v0.9, "Thao tác của user thắng").
		st.list.ToggleExpanded(sg.key)
	}
	expanded := st.list.Expanded(sg.key)
	vis, prog := gs.subAnim(sg.key).step(env, expanded, clicked)
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return cl.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return layoutSubGroupHeader(env, &local, sg, expanded)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layoutAnimatedCollapse(&local, vis, prog, func(gtx *layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceXS, Left: theme.SpaceLG}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return layoutSubGroupMembers(env, &l, st, sg.items, cb)
				})
			})
		}),
	)
}

func layoutSubGroupHeader(env *kit.Env, gtx *layout.Context, sg *subGroup, expanded bool) layout.Dimensions {
	ch := env.Th.Chrome()
	caret := kit.IconChevronRight
	if expanded {
		caret = kit.IconChevronDown
	}
	g := *gtx
	g.Constraints.Min.Y = max(g.Constraints.Min.Y, gtx.Dp(theme.Row))
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(g,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			d, err := env.Icon(&l, iconForCategory(sg.category), theme.IconSM, ch.IconMuted)
			logTVErr(err)
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Dp(theme.SpaceSM), 0)}
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			l := gtx
			// Same size as a tool row, so a folded group reads as one of the rows.
			return kit.EllipsisLabel(env, &l, theme.TypeBody(), ch.FgMuted, sg.title)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				d, err := env.Icon(&l, caret, theme.IconXS, ch.FgSubtle)
				logTVErr(err)
				return d
			})
		}),
	)
}

func layoutSubGroupMembers(env *kit.Env, gtx *layout.Context, st *State, items []*session.Item, cb Callbacks) layout.Dimensions {
	rows := make([]layout.FlexChild, 0, len(items))
	for k := range items {
		it := items[k]
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return layoutWorkMember(env, &l, st, it, cb)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, rows...)
}

func layoutWorkMember(env *kit.Env, gtx *layout.Context, st *State, it *session.Item, cb Callbacks) layout.Dimensions {
	c := cardOf(st, it.ItemID)
	switch it.Kind {
	case kindToolCall:
		return layoutToolCall(env, gtx, st, c, it, cb)
	case kindFileChange:
		return layoutFileChange(env, gtx, st, c, it, cb)
	case kindSubagent:
		// Inside a work group a subagent is a "Ran subagent" row like the
		// other tools (desktop-app v0.10 table), with its icon and time.
		return layoutToolCall(env, gtx, st, c, it, cb)
	default:
		return layoutCompactCard(env, gtx, st, c, it)
	}
}
