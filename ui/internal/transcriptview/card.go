package transcriptview

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/mdview"
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
	kindUsage            = "usage"
	kindQuota            = "quota"
	kindNotice           = "notice"
)

// cardState is one item's view state the row func needs across frames: its
// parsed markdown when it is a message, the source it was parsed from, the
// mdview state and the click on its header. It lives in the VirtualList's
// payload slot, so the list's bound evicts it with the item's heights; the
// expansion lives per item_id in the list, so it survives a new revision.
type cardState struct {
	doc    *markdown.Doc
	docSrc string
	md     mdview.State
	click  widget.Clickable
}

// cardOf returns the item's payload card, building it on first use.
func cardOf(st *State, itemID string) *cardState {
	return kit.PayloadOf(st.list, itemID, func() *cardState { return &cardState{} })
}

// layoutCard lays one item out (transcript §3). Every kind renders
// something: an unknown kind renders as a generic card, never a crash or a
// drop. The height the list records comes from the returned dimensions, so
// a new revision's entry is seeded from the old one by the kit's cache and
// the list does not jump.
func layoutCard(env *kit.Env, gtx *layout.Context, st *State, item *session.Item) layout.Dimensions {
	c := cardOf(st, item.ItemID)
	if c.click.Clicked(*gtx) {
		st.list.ToggleExpanded(item.ItemID)
	}
	gap := layout.Inset{Bottom: theme.SpaceSM}
	return gap.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		return surface(&local, env, func(gtx *layout.Context) layout.Dimensions {
			return cardBody(env, gtx, st, c, item)
		})
	})
}

// cardBody renders the item's kind onto the card surface.
func cardBody(env *kit.Env, gtx *layout.Context, st *State, c *cardState, item *session.Item) layout.Dimensions {
	switch item.Kind {
	case kindUserMessage, kindAssistantMessage:
		return messageBody(gtx, env, c, item.Display().Text)
	case kindReasoning, kindToolCall, kindFileChange:
		return collapsibleBody(env, gtx, c, headerFor(item), item, st.list.Expanded(item.ItemID))
	default:
		return plainBody(env, gtx, c, headerFor(item), item, st.list.Expanded(item.ItemID))
	}
}

// messageBody re-parses a message's markdown when the display text changes —
// a streaming delta grows the text under the same revision — and renders it.
func messageBody(gtx *layout.Context, env *kit.Env, c *cardState, src string) layout.Dimensions {
	if c.doc == nil || c.docSrc != src {
		c.doc = markdown.Parse(src)
		c.docSrc = src
	}
	return mdview.Layout(gtx, env.Th, c.doc, &c.md)
}

// collapsibleBody renders the collapsed-by-default cards: reasoning, tool
// calls and file changes show their header; the text or diff expands on
// click.
func collapsibleBody(env *kit.Env, gtx *layout.Context, c *cardState, header string, item *session.Item, expanded bool) layout.Dimensions {
	return flexBody(env, gtx, c, header, func(gtx *layout.Context) layout.Dimensions {
		return monoText(env, gtx, item.Display().Text)
	}, expanded)
}

// plainBody renders the compact cards and the generic card: a header line,
// and, for an unknown kind, the view text underneath so no item is dropped.
func plainBody(env *kit.Env, gtx *layout.Context, c *cardState, header string, item *session.Item, expanded bool) layout.Dimensions {
	return flexBody(env, gtx, c, header, func(gtx *layout.Context) layout.Dimensions {
		return kit.Paragraph(env, gtx, theme.TypeBody(), colorFor(env.Th, item.Metadata.Level), item.Display().Text)
	}, expanded || !knownKind(item.Kind))
}

// flexBody stacks the clickable header and, when expanded, the body.
func flexBody(env *kit.Env, gtx *layout.Context, c *cardState, header string, body func(*layout.Context) layout.Dimensions, expanded bool) layout.Dimensions {
	head := func() layout.Dimensions {
		return c.click.Layout(*gtx, func(layout.Context) layout.Dimensions {
			// Clickable calls its widget with the same context, so the
			// pointer form and the value form are the same frame here.
			return headerRow(env, gtx, header, expanded)
		})
	}
	if !expanded {
		return head()
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(layout.Context) layout.Dimensions { return head() }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return body(&local)
			})
		}),
	)
}

// surface paints the card's raised background and lays the body inside its
// padding; the background is recorded first so the text paints over it.
func surface(gtx *layout.Context, env *kit.Env, body func(*layout.Context) layout.Dimensions) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	pad := layout.UniformInset(theme.SpaceMD)
	dims := pad.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		return body(&local)
	})
	call := macro.Stop()
	paint.FillShape(gtx.Ops, env.Th.Chrome().SurfaceBase,
		clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(theme.RadiusXS)).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

// headerRow renders the header text; expansion is told by the chevron.
func headerRow(env *kit.Env, gtx *layout.Context, header string, expanded bool) layout.Dimensions {
	caret := kit.IconChevronRight
	if expanded {
		caret = kit.IconChevronDown
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			icon, err := env.Icon(&local, caret, theme.IconXS, env.Th.Chrome().FgMuted)
			if err != nil {
				return layout.Dimensions{}
			}
			return icon
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return kit.Label(env, &local, theme.TypeSmall(), env.Th.Chrome().FgMuted, header)
			})
		}),
	)
}

// monoText renders text in the code face on the code surface, for tool
// output and diffs.
func monoText(env *kit.Env, gtx *layout.Context, s string) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := kit.Paragraph(env, gtx, theme.TypeCodeSmall(), env.Th.Chrome().Fg, s)
	call := macro.Stop()
	paint.FillShape(gtx.Ops, env.Th.CodeSurface(),
		clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(theme.RadiusXS)).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
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
