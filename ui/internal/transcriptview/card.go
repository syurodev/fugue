package transcriptview

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The item kinds the transcript defines (transcript §3).
const (
	kindUserMessage      = "user_message"
	kindAssistantMessage = "assistant_message"
	kindReasoning        = "reasoning"
	kindToolCall         = "tool_call"
	kindFileChange       = "file_change"
	kindApproval         = "approval"
	kindPlan             = "plan"
	kindSubagent         = "subagent"
	kindCommand          = "command"
	kindTeamProposal     = "team_proposal"
	kindTaskLink         = "task_link"
	kindUsage            = "usage"
	kindQuota            = "quota"
	kindNotice           = "notice"
)

type expandAnim struct {
	pres   motion.Presence
	inited bool
	last   bool
}

func newExpandAnim() expandAnim {
	return expandAnim{pres: motion.NewPresence(motion.Smooth(), motion.Exit())}
}

func (a *expandAnim) step(env *kit.Env, expanded, fromClick bool) (visible bool, progress float32) {
	if !a.inited {
		a.inited = true
		a.last = expanded
		a.pres.Snap(expanded)
	} else if expanded != a.last {
		a.last = expanded
		if fromClick && !env.ReducedMotion {
			a.pres.Show(expanded)
		} else {
			a.pres.Snap(expanded)
		}
	}
	if env.ReducedMotion {
		a.pres.Snap(a.last)
	} else if a.pres.Advance(env.DT()) {
		env.Keep()
	}
	return a.pres.Visible(), min(max(a.pres.Progress(), 0), 1)
}

type cardState struct {
	doc        *markdown.Doc
	docSrc     string
	md         mdview.State
	click      widget.Clickable
	code       *mdview.CodeBlockState
	planCard   *kit.CardState
	commitCard *kit.CardState
	viewDiff   *kit.ButtonState
	anim       expandAnim
	chips      map[string]*widget.Clickable
}

func cardOf(st *State, key string) *cardState {
	return kit.PayloadOf(st.list, key, func() *cardState {
		return &cardState{
			code:       mdview.NewCodeBlockState(),
			planCard:   kit.NewCardState(),
			commitCard: kit.NewCardState(),
			viewDiff:   kit.NewButtonState(),
			anim:       newExpandAnim(),
			chips:      map[string]*widget.Clickable{},
		}
	})
}

func (c *cardState) chipClick(key string) *widget.Clickable {
	if c.chips == nil {
		c.chips = map[string]*widget.Clickable{}
	}
	cl, ok := c.chips[key]
	if !ok {
		cl = &widget.Clickable{}
		c.chips[key] = cl
	}
	return cl
}

func layoutRow(env *kit.Env, gtx *layout.Context, st *State, row *displayRow, cb Callbacks) layout.Dimensions {
	gap := layout.Inset{Bottom: theme.SpaceMD}
	return gap.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		if row.group != nil {
			return layoutWorkGroup(env, &local, st, row.group, cb)
		}
		return layoutItem(env, &local, st, row.item, cb)
	})
}

func layoutItem(env *kit.Env, gtx *layout.Context, st *State, item *session.Item, cb Callbacks) layout.Dimensions {
	c := cardOf(st, item.ItemID)
	switch item.Kind {
	case kindAssistantMessage:
		// assistant_message: document page at type.prose; no bubble, no avatar, no border.
		return layoutMarkdownBody(env, gtx, c, item.Display().Text, cb)
	case kindUserMessage:
		return layoutUserPill(env, gtx, c, item, cb)
	case kindReasoning:
		return layoutReasoning(env, gtx, st, c, item)
	case kindToolCall:
		return layoutToolCall(env, gtx, st, c, item, cb)
	case kindFileChange:
		return layoutFileChange(env, gtx, st, c, item, cb)
	case kindPlan:
		return layoutPlanCard(env, gtx, c, item)
	case kindSubagent:
		return layoutSubagentLine(env, gtx, st, c, item)
	default:
		return layoutCompactCard(env, gtx, st, c, item)
	}
}

func layoutMarkdownBody(env *kit.Env, gtx *layout.Context, c *cardState, src string, cb Callbacks) layout.Dimensions {
	if c.doc == nil || c.docSrc != src {
		c.doc = markdown.Parse(src)
		c.docSrc = src
	}
	dims := mdview.LayoutEnv(env, gtx, c.doc, &c.md)
	drainMarkdownEvents(&c.md, cb)
	return dims
}

func drainMarkdownEvents(md *mdview.State, cb Callbacks) {
	for {
		op, ok := md.PollOpen()
		if !ok {
			break
		}
		if cb.OnOpenLink != nil {
			cb.OnOpenLink(op.URL)
		}
	}
	for {
		fc, ok := md.PollFileClick()
		if !ok {
			break
		}
		if cb.OnFileChip != nil {
			cb.OnFileChip(fc.Ref)
		}
	}
	for {
		ic, ok := md.PollIDClick()
		if !ok {
			break
		}
		if cb.OnIDChip != nil {
			cb.OnIDChip(ic.ID)
		}
	}
}

func layoutUserPill(env *kit.Env, gtx *layout.Context, c *cardState, item *session.Item, cb Callbacks) layout.Dimensions {
	colW := gtx.Constraints.Max.X
	maxPillW := max(colW*3/4, gtx.Dp(theme.SpaceXXL))
	padX := gtx.Dp(theme.SpaceLG)
	padY := gtx.Dp(theme.SpaceMD)

	inner := *gtx
	inner.Constraints.Min = image.Point{}
	inner.Constraints.Max.X = max(maxPillW-2*padX, 0)
	rec := op.Record(gtx.Ops)
	bodyDims := layoutMarkdownBody(env, &inner, c, item.Display().Text, cb)
	call := rec.Stop()

	pillSize := image.Pt(min(maxPillW, bodyDims.Size.X+2*padX), bodyDims.Size.Y+2*padY)
	originLabel := originLabelFor(item.Metadata.Origin)

	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if originLabel == "" {
				return layout.Dimensions{}
			}
			lbl := op.Record(gtx.Ops)
			local := gtx
			local.Constraints.Min = image.Point{}
			ld := kit.Label(env, &local, theme.TypeSmall(), env.Th.Chrome().FgSubtle, originLabel)
			lCall := lbl.Stop()
			off := op.Offset(image.Pt(max(colW-ld.Size.X, 0), 0)).Push(gtx.Ops)
			lCall.Add(gtx.Ops)
			off.Pop()
			return layout.Dimensions{Size: image.Pt(colW, ld.Size.Y+gtx.Dp(theme.SpaceXXS))}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			x := max(colW-pillSize.X, 0)
			off := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
			paint.FillShape(gtx.Ops, env.Th.Chrome().FillSoft,
				clip.UniformRRect(image.Rectangle{Max: pillSize}, gtx.Dp(theme.RadiusLG)).Op(gtx.Ops))
			boff := op.Offset(image.Pt(padX, padY)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			boff.Pop()
			off.Pop()
			return layout.Dimensions{Size: image.Pt(colW, pillSize.Y)}
		}),
	)
}

func originLabelFor(origin string) string {
	switch origin {
	case "lead":
		return "From lead"
	case "fugue":
		return "Fugue"
	default:
		return ""
	}
}

func layoutAnimatedCollapse(gtx *layout.Context, visible bool, progress float32, body func(*layout.Context) layout.Dimensions) layout.Dimensions {
	if !visible || progress <= 0 {
		return layout.Dimensions{}
	}
	rec := op.Record(gtx.Ops)
	dims := body(gtx)
	call := rec.Stop()
	h := int(float32(dims.Size.Y) * progress)
	if h <= 0 {
		return layout.Dimensions{Size: image.Pt(dims.Size.X, 0)}
	}
	clipped := clip.Rect(image.Rectangle{Max: image.Pt(dims.Size.X, h)}).Push(gtx.Ops)
	call.Add(gtx.Ops)
	clipped.Pop()
	return layout.Dimensions{Size: image.Pt(dims.Size.X, h)}
}

func colorForNotice(th *theme.Theme, level string) color.NRGBA {
	switch level {
	case "warning":
		return th.Status().Warning
	case "error":
		return th.Status().Danger
	case "info":
		return th.Status().Info
	default:
		return th.Chrome().Fg
	}
}
