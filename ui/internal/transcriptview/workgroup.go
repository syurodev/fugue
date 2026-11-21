package transcriptview

import (
	"image"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type workGroupState struct {
	click        widget.Clickable
	anim         expandAnim
	titleFade    motion.Follower
	lastTitle    string
	subExpanded  map[string]bool
	subClicks    map[string]*widget.Clickable
	subAnims     map[string]*expandAnim
	userOverride bool
}

func groupStateOf(st *State, key string) *workGroupState {
	return kit.PayloadOf(st.list, key, func() *workGroupState {
		f := motion.NewFollower(motion.Smooth())
		f.Snap(1)
		return &workGroupState{
			anim:        newExpandAnim(),
			titleFade:   f,
			subExpanded: map[string]bool{},
			subClicks:   map[string]*widget.Clickable{},
			subAnims:    map[string]*expandAnim{},
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
		gs.userOverride = true
		st.list.ToggleExpanded(grp.key)
	} else if !gs.userOverride && grp.running && !st.list.Expanded(grp.key) {
		st.list.SetExpanded(grp.key, true)
	}
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
			return layout.Inset{Top: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Divider(env, &l, kit.DividerProps{Orientation: kit.Horizontal})
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layoutAnimatedCollapse(&local, vis, prog, func(gtx *layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceSM}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return layoutSubGroups(env, &l, st, gs, grp.subGroups, cb)
				})
			})
		}),
	)
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
				d, _ := env.Icon(&l, caret, theme.IconXS, ch.FgSubtle)
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
		gs.subExpanded[sg.key] = !gs.subExpanded[sg.key]
	}
	expanded := gs.subExpanded[sg.key]
	vis, prog := gs.subAnim(sg.key).step(env, expanded, clicked)
	ch := env.Th.Chrome()
	caret := kit.IconChevronRight
	if expanded {
		caret = kit.IconChevronDown
	}
	icon := iconForCategory(sg.category)

	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return cl.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := gtx
						d, _ := env.Icon(&l, icon, theme.IconXS, ch.FgSubtle)
						return d
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Dimensions{Size: image.Pt(gtx.Dp(theme.SpaceSM), 0)}
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						l := gtx
						return kit.Label(env, &l, theme.TypeSmall(), ch.FgMuted, sg.title)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							l := gtx
							d, _ := env.Icon(&l, caret, theme.IconXS, ch.FgSubtle)
							return d
						})
					}),
				)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layoutAnimatedCollapse(&local, vis, prog, func(gtx *layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceXS, Left: theme.SpaceLG}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
					rows := make([]layout.FlexChild, 0, len(sg.items))
					for k := range sg.items {
						it := sg.items[k]
						rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								l := gtx
								return layoutWorkMember(env, &l, st, it, cb)
							})
						}))
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
				})
			})
		}),
	)
}

func layoutWorkMember(env *kit.Env, gtx *layout.Context, st *State, it *session.Item, cb Callbacks) layout.Dimensions {
	c := cardOf(st, it.ItemID)
	switch it.Kind {
	case kindToolCall:
		return layoutToolCall(env, gtx, st, c, it, cb)
	case kindFileChange:
		return layoutFileChange(env, gtx, st, c, it, cb)
	case kindSubagent:
		return layoutSubagentLine(env, gtx, st, c, it)
	default:
		return layoutCompactCard(env, gtx, st, c, it)
	}
}
