package transcriptview

import (
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func layoutReasoning(env *kit.Env, gtx *layout.Context, st *State, c *cardState, item *session.Item) layout.Dimensions {
	clicked := c.click.Clicked(*gtx)
	if clicked {
		st.list.ToggleExpanded(item.ItemID)
	}
	expanded := st.list.Expanded(item.ItemID)
	vis, prog := c.anim.step(env, expanded, clicked)

	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return c.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return quietLine(env, &local, reasoningLabel(item), "", expanded, item.Display().Text != "")
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if item.Display().Text == "" {
				return layout.Dimensions{}
			}
			local := gtx
			return layoutAnimatedCollapse(&local, vis, prog, func(gtx *layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceXS, Left: theme.SpaceLG}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return kit.Paragraph(env, &l, theme.TypeSmall(), env.Th.Chrome().FgMuted, item.Display().Text)
				})
			})
		}),
	)
}

func layoutSubagentLine(env *kit.Env, gtx *layout.Context, st *State, c *cardState, item *session.Item) layout.Dimensions {
	hasResult := item.Display().Text != ""
	clicked := hasResult && c.click.Clicked(*gtx)
	if clicked {
		st.list.ToggleExpanded(item.ItemID)
	}
	expanded := st.list.Expanded(item.ItemID)
	vis, prog := c.anim.step(env, expanded, clicked)

	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return c.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return quietLine(env, &local, subagentTitle(item), item.Metadata.Status, expanded, hasResult)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !hasResult {
				return layout.Dimensions{}
			}
			local := gtx
			return layoutAnimatedCollapse(&local, vis, prog, func(gtx *layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceXS, Left: theme.SpaceLG}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return kit.Paragraph(env, &l, theme.TypeSmall(), env.Th.Chrome().FgMuted, item.Display().Text)
				})
			})
		}),
	)
}

func quietLine(env *kit.Env, gtx *layout.Context, label, right string, expanded, expandable bool) layout.Dimensions {
	ch := env.Th.Chrome()
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return kit.Label(env, &local, theme.TypeSmall(), ch.FgSubtle, label)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if right == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, theme.TypeSmall(), ch.FgSubtle, right)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !expandable {
				return layout.Dimensions{}
			}
			caret := kit.IconChevronRight
			if expanded {
				caret = kit.IconChevronDown
			}
			return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				d, err := env.Icon(&l, caret, theme.IconXS, ch.FgSubtle)
				logTVErr(err)
				return d
			})
		}),
	)
}

func layoutToolCall(env *kit.Env, gtx *layout.Context, st *State, c *cardState, item *session.Item, cb Callbacks) layout.Dimensions {
	spec := inspectToolRow(item)
	clicked := spec.expandable && c.click.Clicked(*gtx)
	if clicked {
		st.list.ToggleExpanded(item.ItemID)
	}
	expanded := spec.expandable && st.list.Expanded(item.ItemID)
	vis, prog := c.anim.step(env, expanded, clicked)
	failed := isToolFailed(item)
	if failed && !spec.expandable && item.Display().Text != "" {
		spec.expandable = true
	}
	rightText, rightDanger := toolRightSlot(item)
	header := func(gtx *layout.Context) layout.Dimensions {
		drawHeader := func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return renderToolRowLine(env, &local, c, &spec, rightText, rightDanger, expanded, cb)
		}
		if spec.expandable {
			return c.click.Layout(*gtx, drawHeader)
		}
		return drawHeader(*gtx)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layoutToolRowSurface(env, &local, item, &spec, failed, header)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return layoutToolCallBody(env, &l, c, item, &spec, vis, prog, cb)
		}),
	)
}

func layoutToolRowSurface(
	env *kit.Env, gtx *layout.Context, item *session.Item, spec *toolRowSpec, failed bool,
	header func(*layout.Context) layout.Dimensions,
) layout.Dimensions {
	draw := func(gtx *layout.Context) layout.Dimensions {
		if !failed {
			return header(gtx)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return header(&l)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceXXS, Bottom: theme.SpaceXS, Left: theme.SpaceXL}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return kit.Label(env, &l, theme.TypeSmall(), env.Th.Chrome().DangerFg, failedReasonLine(item))
				})
			}),
		)
	}
	if failed {
		return wrapSurfaceTone(env, gtx, env.Th.Chrome().DangerSoft, draw)
	}
	if spec.warningRow {
		return wrapSurfaceTone(env, gtx, env.Th.StatusTone(theme.StatusWarning).Fill, draw)
	}
	return draw(gtx)
}

func layoutToolCallBody(
	env *kit.Env, gtx *layout.Context, c *cardState, item *session.Item,
	spec *toolRowSpec, vis bool, prog float32, cb Callbacks,
) layout.Dimensions {
	if !spec.expandable {
		return layout.Dimensions{}
	}
	return layoutAnimatedCollapse(gtx, vis, prog, func(gtx *layout.Context) layout.Dimensions {
		return layout.Inset{Top: theme.SpaceSM}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return drawToolExpandedBody(env, &local, c, item, spec, cb)
		})
	})
}
