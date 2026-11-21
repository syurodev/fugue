package transcriptview

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/mdview"
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
	clicked := c.click.Clicked(*gtx)
	if clicked {
		st.list.ToggleExpanded(item.ItemID)
	}
	expanded := st.list.Expanded(item.ItemID)
	vis, prog := c.anim.step(env, expanded, clicked)
	failed := isToolFailed(item)
	icon := iconForCategory(toolCategory(item.Metadata.Tool))
	drawContent := func(gtx *layout.Context) layout.Dimensions {
		return layoutToolColumn(env, gtx, c, item, icon, expanded, vis, prog, failed, cb)
	}
	if !failed {
		return drawContent(gtx)
	}
	return wrapDangerSurface(env, gtx, drawContent)
}

func layoutToolColumn(
	env *kit.Env, gtx *layout.Context, c *cardState, item *session.Item,
	icon kit.IconName, expanded, vis bool, prog float32, failed bool, cb Callbacks,
) layout.Dimensions {
	rightText, rightDanger := toolRightSlot(item)
	title := toolShortTitle(item)
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return c.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return layoutToolHeaderRow(env, &local, c, icon, title, rightText, rightDanger, expanded, cb)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !failed {
				return layout.Dimensions{}
			}
			return layout.Inset{Top: theme.SpaceXXS, Left: theme.SpaceXL}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, theme.TypeSmall(), env.Th.Chrome().DangerFg, failedReasonLine(item))
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layoutToolCallBody(env, &local, c, item, icon, vis, prog, cb)
		}),
	)
}

func layoutToolCallBody(
	env *kit.Env, gtx *layout.Context, c *cardState, item *session.Item,
	icon kit.IconName, vis bool, prog float32, cb Callbacks,
) layout.Dimensions {
	commit, isCommit := detectCommitEffect(item)
	outText := item.Display().Text
	if !isCommit && outText == "" {
		return layout.Dimensions{}
	}
	return layoutAnimatedCollapse(gtx, vis, prog, func(gtx *layout.Context) layout.Dimensions {
		return layout.Inset{Top: theme.SpaceSM}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
			if isCommit {
				local := gtx
				return layoutCommitCard(env, &local, c, item, commit, cb)
			}
			local := gtx
			fmtStr := detectOutputFormat(item)
			dims, res := mdview.LayoutCodeBlock(env, &local, c.code, &mdview.CodeBlockProps{
				Title:        "Output",
				Format:       fmtStr,
				Icon:         icon,
				IsOutputCard: true,
				IsDiff:       fmtStr == formatDiff,
				Raw:          outText,
				Truncated:    item.Display().Truncated,
			})
			if res.RequestFull && cb.OnFullOutput != nil {
				cb.OnFullOutput(item.ItemID)
			}
			return dims
		})
	})
}

func wrapDangerSurface(env *kit.Env, gtx *layout.Context, draw func(*layout.Context) layout.Dimensions) layout.Dimensions {
	// Failed is the ONLY colored ground (DangerSoft).
	rec := op.Record(gtx.Ops)
	dims := layout.Inset{Top: theme.SpaceSM, Bottom: theme.SpaceSM, Left: theme.SpaceMD, Right: theme.SpaceMD}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		l := gtx
		return draw(&l)
	})
	call := rec.Stop()
	paint.FillShape(gtx.Ops, env.Th.Chrome().DangerSoft,
		clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(theme.RadiusMD)).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

func layoutToolHeaderRow(
	env *kit.Env, gtx *layout.Context, c *cardState,
	icon kit.IconName, title, right string, rightDanger, expanded bool, cb Callbacks,
) layout.Dimensions {
	ch := env.Th.Chrome()
	caret := kit.IconChevronRight
	if expanded {
		caret = kit.IconChevronDown
	}
	rightCol := ch.FgMuted
	if rightDanger {
		rightCol = env.Th.Status().Danger
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			d, err := env.Icon(&local, icon, theme.IconSM, ch.FgMuted)
			logTVErr(err)
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Dp(theme.SpaceSM), 0)}
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return drawInlineTitleWithChips(env, &local, c, title, ch.Fg, cb)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if right == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, theme.TypeSmall(), rightCol, right)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				d, err := env.Icon(&l, caret, theme.IconXS, ch.FgMuted)
				logTVErr(err)
				return d
			})
		}),
	)
}

func drawInlineTitleWithChips(env *kit.Env, gtx *layout.Context, c *cardState, title string, fg color.NRGBA, cb Callbacks) layout.Dimensions {
	runs := mdview.ParseChips(title)
	if !hasStyledChip(runs) {
		return kit.Label(env, gtx, theme.TypeBody(), fg, title)
	}
	children := make([]layout.FlexChild, 0, len(runs))
	for i := range runs {
		run := runs[i]
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return drawOneInlineRun(env, &local, c, run, fg, cb)
		}))
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, children...)
}

func hasStyledChip(runs []mdview.ChipRun) bool {
	for i := range runs {
		if runs[i].Kind != mdview.ChipNone {
			return true
		}
	}
	return false
}

func drawOneInlineRun(env *kit.Env, gtx *layout.Context, c *cardState, run mdview.ChipRun, fg color.NRGBA, cb Callbacks) layout.Dimensions {
	switch run.Kind {
	case mdview.ChipFile:
		cl := c.chipClick("f:" + run.Text)
		if cl.Clicked(*gtx) && cb.OnFileChip != nil {
			cb.OnFileChip(run.File)
		}
		return cl.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return drawFileChipPill(env, &l, run)
		})
	case mdview.ChipHash:
		cl := c.chipClick("h:" + run.Text)
		if cl.Clicked(*gtx) && cb.OnIDChip != nil {
			cb.OnIDChip(run.Text)
		}
		return cl.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return drawHashChipSpan(env, &l, run.Text)
		})
	case mdview.ChipNone:
		return kit.Label(env, gtx, theme.TypeBody(), fg, run.Text)
	}
	return kit.Label(env, gtx, theme.TypeBody(), fg, run.Text)
}

func drawFileChipPill(env *kit.Env, gtx *layout.Context, run mdview.ChipRun) layout.Dimensions {
	ch := env.Th.Chrome()
	rec := op.Record(gtx.Ops)
	inner := *gtx
	inner.Constraints.Min = image.Point{}
	dims := layout.Inset{Left: theme.SpaceXS, Right: theme.SpaceXS, Top: theme.SpaceXXS, Bottom: theme.SpaceXXS}.Layout(inner, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := gtx
				d, err := env.Icon(&l, mdview.FileIcon(run.File.Path), theme.IconXS, ch.FgMuted)
				logTVErr(err)
				return d
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(gtx.Dp(theme.SpaceXS), 0)}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, theme.TypeCodeSmall(), ch.Fg, run.Text)
			}),
		)
	})
	call := rec.Stop()
	paint.FillShape(gtx.Ops, ch.FillSoft,
		clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(theme.RadiusXS)).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

func drawHashChipSpan(env *kit.Env, gtx *layout.Context, text string) layout.Dimensions {
	ch := env.Th.Chrome()
	local := *gtx
	local.Constraints.Min = image.Point{}
	dims := kit.Label(env, &local, theme.TypeCodeSmall(), ch.Fg, text)
	thin := max(gtx.Dp(theme.Hairline), 1)
	y := max(dims.Size.Y-thin, 0)
	for dx := 0; dx < dims.Size.X; dx += 3 * thin {
		dot := image.Rect(dx, y, min(dx+thin, dims.Size.X), y+thin)
		paint.FillShape(gtx.Ops, ch.FgMuted, clip.Rect(dot).Op())
	}
	return dims
}
