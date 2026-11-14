package transcriptview

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// mdState is one message card's mdview state.
type mdState = mdview.State

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
	kindUsage            = "usage"
	kindQuota            = "quota"
	kindNotice           = "notice"
)

// layoutCard lays one item out, updating its cached state. Every kind renders
// something: an unknown kind renders as a generic card, never a crash or a
// drop.
func layoutCard(gtx *layout.Context, th *theme.Theme, st *State, item *session.Item) layout.Dimensions {
	key := cardKey{itemID: item.ItemID, revision: item.Revision}
	c, ok := st.cards.get(key)
	if !ok {
		c = &card{}
		st.cards.put(key, c)
	}
	if c.click.Clicked(*gtx) {
		c.expanded = !c.expanded
	}
	st.rendered++
	gap := layout.Inset{Bottom: unit.Dp(6)}
	return gap.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		dims := surface(&local, th, func(gtx *layout.Context) layout.Dimensions {
			return cardBody(gtx, th, c, item)
		})
		c.height = dims.Size.Y
		return dims
	})
}

// cardBody renders the item's kind onto the card surface.
func cardBody(gtx *layout.Context, th *theme.Theme, c *card, item *session.Item) layout.Dimensions {
	switch item.Kind {
	case kindUserMessage, kindAssistantMessage:
		return messageBody(gtx, th, c, item.View().Text)
	case kindReasoning, kindToolCall, kindFileChange:
		return collapsibleBody(gtx, th, c, headerFor(item), item)
	default:
		return plainBody(gtx, th, c, headerFor(item), item)
	}
}

// messageBody parses and renders a message's markdown once per revision.
func messageBody(gtx *layout.Context, th *theme.Theme, c *card, src string) layout.Dimensions {
	if c.doc == nil {
		c.doc = markdown.Parse(src)
	}
	return mdview.Layout(gtx, th, c.doc, &c.md)
}

// collapsibleBody renders the collapsed-by-default cards: reasoning, tool
// calls and file changes show their header; the text or diff expands on
// click.
func collapsibleBody(gtx *layout.Context, th *theme.Theme, c *card, header string, item *session.Item) layout.Dimensions {
	return flexBody(gtx, th, c, header, func(gtx *layout.Context) layout.Dimensions {
		return monoText(gtx, th, item.View().Text)
	}, c.expanded)
}

// plainBody renders the compact cards and the generic card: a header line,
// and, for an unknown kind, the view text underneath so no item is dropped.
func plainBody(gtx *layout.Context, th *theme.Theme, c *card, header string, item *session.Item) layout.Dimensions {
	return flexBody(gtx, th, c, header, func(gtx *layout.Context) layout.Dimensions {
		return wrappedText(gtx, th, item.View().Text, colorFor(th, item.Metadata.Level))
	}, c.expanded || !knownKind(item.Kind))
}

// flexBody stacks the clickable header and, when expanded, the body.
func flexBody(gtx *layout.Context, th *theme.Theme, c *card, header string, body func(*layout.Context) layout.Dimensions, expanded bool) layout.Dimensions {
	head := func() layout.Dimensions {
		return c.click.Layout(*gtx, func(layout.Context) layout.Dimensions {
			// Clickable calls its widget with the same context, so the
			// pointer form and the value form are the same frame here.
			return headerRow(gtx, th, header, c.expanded)
		})
	}
	if !expanded {
		return head()
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(layout.Context) layout.Dimensions { return head() }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return body(&local)
			})
		}),
	)
}

// surface paints the card's raised background and lays the body inside its
// padding; the background is recorded first so the text paints over it.
func surface(gtx *layout.Context, th *theme.Theme, body func(*layout.Context) layout.Dimensions) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	pad := layout.UniformInset(unit.Dp(8))
	dims := pad.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		return body(&local)
	})
	call := macro.Stop()
	paint.FillShape(gtx.Ops, th.Chrome().SurfaceBase,
		clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(unit.Dp(4))).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

// headerRow renders the header text; expansion is told by the caret.
func headerRow(gtx *layout.Context, th *theme.Theme, header string, expanded bool) layout.Dimensions {
	mt := th.Material()
	caret := "▸"
	if expanded {
		caret = "▾"
	}
	return layout.Flex{Axis: layout.Horizontal}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return label(mt, unit.Sp(12), caret+" ", th.Chrome().FgMuted).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := label(mt, unit.Sp(12), header, th.Chrome().FgMuted)
			l.WrapPolicy = text.WrapWords
			return l.Layout(gtx)
		}),
	)
}

// monoText renders text in the code face on the code surface, for tool
// output and diffs.
func monoText(gtx *layout.Context, th *theme.Theme, s string) layout.Dimensions {
	mt := th.Material()
	macro := op.Record(gtx.Ops)
	l := label(mt, unit.Sp(12), s, th.Chrome().Fg)
	l.Font.Typeface = fonts.Mono
	l.WrapPolicy = text.WrapWords
	dims := l.Layout(*gtx)
	call := macro.Stop()
	paint.FillShape(gtx.Ops, th.CodeSurface(),
		clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(unit.Dp(3))).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

// wrappedText renders plain wrapping text.
func wrappedText(gtx *layout.Context, th *theme.Theme, s string, c color.NRGBA) layout.Dimensions {
	mt := th.Material()
	l := label(mt, unit.Sp(13), s, c)
	l.WrapPolicy = text.WrapWords
	return l.Layout(*gtx)
}

// label builds a styled material label.
func label(mt *material.Theme, sp unit.Sp, s string, c color.NRGBA) material.LabelStyle {
	l := material.Label(mt, sp, s)
	l.Color = c
	return l
}

// colorFor maps a notice level onto the status tokens; anything else is main
// text.
func colorFor(th *theme.Theme, level string) color.NRGBA {
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

// knownKind reports whether the kind is one transcript §3 names.
func knownKind(kind string) bool {
	switch kind {
	case kindApproval, kindPlan, kindSubagent, kindCommand, kindUsage, kindQuota, kindNotice:
		return true
	default:
		return false
	}
}

// headerFor builds the compact header line of one item from its metadata
// only: identifiers and numbers, never content-derived strings.
func headerFor(item *session.Item) string {
	m := item.Metadata
	switch item.Kind {
	case kindReasoning:
		return "reasoning · " + m.ReasoningState
	case kindToolCall:
		h := m.Tool + " · " + m.Status
		if m.ExitCode != nil {
			h += fmt.Sprintf(" · exit %d", *m.ExitCode)
		}
		if m.DurationMillis > 0 {
			h += fmt.Sprintf(" · %dms", m.DurationMillis)
		}
		return h
	case kindFileChange:
		return fmt.Sprintf("%s · +%d −%d", m.ChangeKind, m.LinesAdded, m.LinesRemoved)
	default:
		return compactHeader(item)
	}
}

// compactHeader builds the header of the compact cards: approval, plan,
// subagent, command, usage, quota and notice.
func compactHeader(item *session.Item) string {
	m := item.Metadata
	switch item.Kind {
	case kindApproval:
		return fmt.Sprintf("approval · %s · %s", m.RequestKind, decisionOf(&m))
	case kindPlan:
		return fmt.Sprintf("plan · step %d of %d", m.CurrentStep, m.StepCount)
	case kindSubagent:
		return "subagent · " + m.Status
	case kindCommand:
		return "command · " + m.Source
	case kindUsage:
		return fmt.Sprintf("usage · in %d · out %d · cache %d", m.InputTokens, m.OutputTokens, m.CacheTokens)
	case kindQuota:
		return fmt.Sprintf("quota · %s · %s · %.0f%%", m.Account, m.Window, m.Used*100)
	case kindNotice:
		return "notice · " + m.Level + " · " + m.Code
	default:
		// The client carries a kind its proto does not name as the decimal
		// wire value; the generic card says so instead of dropping the item.
		if !knownKindName(item.Kind) {
			return "Unknown item (kind " + item.Kind + ")"
		}
		return item.Kind
	}
}

// knownKindName reports whether the string is a kind name transcript §3
// defines; anything else arrived from the wire unnamed.
func knownKindName(kind string) bool {
	switch kind {
	case kindUserMessage, kindAssistantMessage, kindReasoning, kindToolCall, kindFileChange,
		kindApproval, kindPlan, kindSubagent, kindCommand, kindUsage, kindQuota, kindNotice:
		return true
	default:
		return false
	}
}

// decisionOf names the approval's decision, or "pending".
func decisionOf(m *client.TranscriptMetadata) string {
	if m.Decision == "" {
		return "pending"
	}
	return m.Decision + " by " + m.Decider
}
