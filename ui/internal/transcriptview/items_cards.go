package transcriptview

import (
	"encoding/json"
	"fmt"
	"image"
	"regexp"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func layoutCommitCard(env *kit.Env, gtx *layout.Context, c *cardState, item *session.Item, eff commitEffect, cb Callbacks) layout.Dimensions {
	ch := env.Th.Chrome()
	ref := mdview.GitRef{Kind: mdview.GitCommit, Text: shortHash(eff.Hash), Hash: eff.Hash, Subject: eff.Message}
	dims, _, err := c.commitCard.Layout(env, gtx, kit.CardProps{Padding: theme.SpaceMD}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								l := gtx
								return kit.Label(env, &l, theme.TypeBody(), ch.FgMuted, "Committed")
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									l := gtx
									return mdview.DrawGitRefChip(env, &l, c.chipClick("commit:"+ref.Text), ref, gitRegistryFor(cb))
								})
							}),
						)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if eff.Message == "" {
							return layout.Dimensions{}
						}
						l := gtx
						return kit.Label(env, &l, theme.TypeSmall(), ch.FgMuted, eff.Message)
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := gtx
				bp := kit.ButtonProps{Text: "View diff ↗", Variant: kit.Ghost, Size: kit.SizeSM}
				clicked, btnErr := c.viewDiff.Layout(env, &l, bp)
				logTVErr(btnErr)
				if clicked && cb.OnViewDiff != nil {
					cb.OnViewDiff(item.ItemID)
				}
				return c.viewDiff.Measure(env, &l, bp)
			}),
		)
	})
	logTVErr(err)
	return dims
}

func layoutFileChange(env *kit.Env, gtx *layout.Context, st *State, c *cardState, item *session.Item, cb Callbacks) layout.Dimensions {
	spec := fileChangeSpec(item)
	clicked := spec.expandable && c.click.Clicked(*gtx)
	if clicked {
		st.list.ToggleExpanded(item.ItemID)
	}
	expanded := spec.expandable && st.list.Expanded(item.ItemID)
	vis, prog := c.anim.step(env, expanded, clicked)
	fileCB := cb
	if spec.verb == verbEdited && cb.OnViewDiff != nil {
		fileCB.OnOpenFile = func(mdview.FileRef) { cb.OnViewDiff(item.ItemID) }
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			drawHeader := func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return renderToolRowLine(env, &local, c, &spec, "", false, expanded, fileCB)
			}
			if spec.expandable {
				return c.click.Layout(gtx, drawHeader)
			}
			return drawHeader(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !spec.expandable {
				return layout.Dimensions{}
			}
			local := gtx
			return layoutAnimatedCollapse(&local, vis, prog, func(gtx *layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceSM}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return drawDiffOrOutputExpanded(env, &l, c, item, spec.path, true, cb)
				})
			})
		}),
	)
}

func layoutPlanCard(env *kit.Env, gtx *layout.Context, c *cardState, item *session.Item) layout.Dimensions {
	ch := env.Th.Chrome()
	steps := planSteps(item)
	cur := int(item.Metadata.CurrentStep)
	dims, _, err := c.planCard.Layout(env, gtx, kit.CardProps{Padding: theme.SpaceMD}, func(gtx layout.Context) layout.Dimensions {
		children := make([]layout.FlexChild, 0, len(steps)+1)
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, theme.TypeLabel(), ch.Fg, "Plan")
			})
		}))
		for i, raw := range steps {
			state, label := parsePlanStep(raw, i+1, cur)
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return layoutPlanStepRow(env, &l, state, label)
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
	logTVErr(err)
	return dims
}

func planSteps(item *session.Item) []string {
	v := item.Display()
	if len(v.Steps) > 0 {
		return v.Steps
	}
	if v.Text == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(v.Text), "\n")
}

func parsePlanStep(raw string, idx1, cur int) (status, label string) {
	label = strings.TrimSpace(raw)
	for _, p := range []struct{ prefix, status string }{
		{"[completed] ", statusDone},
		{"[done] ", statusDone},
		{"[in_progress] ", statusRunning},
		{"[pending] ", statusPending},
	} {
		if after, ok := strings.CutPrefix(label, p.prefix); ok {
			return p.status, after
		}
	}
	switch {
	case cur > 0 && idx1 < cur:
		return statusDone, label
	case cur > 0 && idx1 == cur:
		return statusRunning, label
	default:
		return statusPending, label
	}
}

func layoutPlanStepRow(env *kit.Env, gtx *layout.Context, status, label string) layout.Dimensions {
	ch := env.Th.Chrome()
	tp := theme.TypeBody()
	textCol := ch.FgMuted
	switch status {
	case statusRunning:
		tp = theme.TypeLabel()
		textCol = ch.Fg
	case statusDone:
		textCol = ch.Fg
	}
	dotSize := gtx.Dp(theme.SpaceMD)
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			box := image.Rect(0, 0, dotSize, dotSize)
			switch status {
			case statusDone:
				paint.FillShape(gtx.Ops, env.Th.Status().Success, clip.Ellipse(box).Op(gtx.Ops))
			case statusRunning:
				paint.FillShape(gtx.Ops, ch.Action, clip.Ellipse(box).Op(gtx.Ops))
			default:
				inset := max(gtx.Dp(theme.Hairline), 1)
				ring := image.Rect(inset, inset, dotSize-inset, dotSize-inset)
				paint.FillShape(gtx.Ops, ch.FgSubtle, clip.Stroke{
					Path:  clip.Ellipse(ring).Path(gtx.Ops),
					Width: float32(max(gtx.Dp(theme.Hairline), 1)),
				}.Op())
			}
			return layout.Dimensions{Size: image.Pt(dotSize, dotSize)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Dp(theme.SpaceSM), 0)}
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return kit.Label(env, &l, tp, textCol, label)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, theme.TypeSmall(), ch.FgSubtle, status)
			})
		}),
	)
}

func layoutCompactCard(env *kit.Env, gtx *layout.Context, st *State, c *cardState, item *session.Item) layout.Dimensions {
	clicked := c.click.Clicked(*gtx)
	if clicked {
		st.list.ToggleExpanded(item.ItemID)
	}
	expanded := st.list.Expanded(item.ItemID) || !knownKind(item.Kind)
	vis, prog := c.anim.step(env, expanded, clicked)
	ch := env.Th.Chrome()
	header := compactHeader(item)
	bodyText := item.Display().Text
	col := colorForNotice(env.Th, item.Metadata.Level)

	rec := op.Record(gtx.Ops)
	dims := layout.UniformInset(theme.SpaceMD).Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return c.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return quietLine(env, &l, header, "", expanded, bodyText != "")
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if bodyText == "" {
					return layout.Dimensions{}
				}
				l := gtx
				return layoutAnimatedCollapse(&l, vis, prog, func(gtx *layout.Context) layout.Dimensions {
					return layout.Inset{Top: theme.SpaceSM}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
						l := gtx
						return kit.Paragraph(env, &l, theme.TypeBody(), col, bodyText)
					})
				})
			}),
		)
	})
	call := rec.Stop()
	paint.FillShape(gtx.Ops, ch.SurfaceRaised,
		clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(theme.RadiusLG)).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, ch.BorderSubtle, clip.Stroke{
		Path:  clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(theme.RadiusLG)).Path(gtx.Ops),
		Width: float32(max(gtx.Dp(theme.Hairline), 1)),
	}.Op())
	call.Add(gtx.Ops)
	return dims
}

func knownKind(kind string) bool {
	switch kind {
	case kindApproval, kindPlan, kindSubagent, kindCommand, kindTeamProposal, kindTaskLink, kindUsage, kindQuota, kindNotice:
		return true
	default:
		return false
	}
}

func compactHeader(item *session.Item) string {
	m := item.Metadata
	switch item.Kind {
	case kindApproval:
		return fmt.Sprintf("approval · %s · %s", m.RequestKind, decisionOf(&m))
	case kindCommand:
		return commandTitle(item)
	case kindTeamProposal:
		return fmt.Sprintf("team proposal · %d agents · %s", m.AgentCount, m.Decision)
	case kindTaskLink:
		return fmt.Sprintf("task %s · %s", m.TaskID, m.Status)
	case kindUsage:
		return fmt.Sprintf("usage · in %d · out %d · cache %d", m.InputTokens, m.OutputTokens, m.CacheTokens)
	case kindQuota:
		return fmt.Sprintf("quota · %s · %s · %.0f%%", m.Account, m.Window, m.Used*100)
	case kindNotice:
		return "notice · " + m.Level + " · " + m.Code
	default:
		if !knownKindName(item.Kind) {
			return "Unknown item (kind " + item.Kind + ")"
		}
		return item.Kind
	}
}

func knownKindName(kind string) bool {
	switch kind {
	case kindUserMessage, kindAssistantMessage, kindReasoning, kindToolCall, kindFileChange,
		kindApproval, kindPlan, kindSubagent, kindCommand, kindTeamProposal, kindTaskLink, kindUsage, kindQuota, kindNotice:
		return true
	default:
		return false
	}
}

func headerFor(item *session.Item) string {
	switch item.Kind {
	case kindReasoning:
		return reasoningLabel(item)
	case kindToolCall:
		return toolShortTitle(item)
	case kindFileChange:
		return "Edited " + fileChangePath(item)
	default:
		return compactHeader(item)
	}
}

func decisionOf(m *client.TranscriptMetadata) string {
	if m.Decision == "" {
		return statusPending
	}
	return m.Decision + " by " + m.Decider
}

var commitBracketRe = regexp.MustCompile(`^\[[^\s\]]+\s+([0-9a-f]{7,40})\]\s+(.+)$`)

func detectCommitEffect(it *session.Item) (commitEffect, bool) {
	if it.Kind != kindToolCall || isToolFailed(it) || isItemRunning(it) {
		return commitEffect{}, false
	}
	txt := it.Display().Text
	for line := range strings.SplitSeq(txt, "\n") {
		line = strings.TrimSpace(line)
		if m := commitBracketRe.FindStringSubmatch(line); len(m) == 3 {
			return commitEffect{Hash: m[1], Message: m[2]}, true
		}
		if rest, ok := strings.CutPrefix(line, "Committed "); ok {
			fields := strings.Fields(rest)
			if len(fields) >= 1 && len(fields[0]) >= 7 {
				msg := strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
				return commitEffect{Hash: fields[0], Message: msg}, true
			}
		}
	}
	// TODO(F11.5): add push and branch known-effect cards once git integration
	// supplies structured push/branch metadata on transcript items.
	return commitEffect{}, false
}

func reasoningLabel(it *session.Item) string {
	if ms := itemDurationMs(it); ms > 0 {
		return "Thought for " + formatDuration(ms)
	}
	return "Thought"
}

func itemDurationMs(it *session.Item) int64 {
	if it.Metadata.DurationMillis > 0 {
		return it.Metadata.DurationMillis
	}
	c, ok1 := parseTS(it.CreatedAt)
	u, ok2 := parseTS(it.UpdatedAt)
	if ok1 && ok2 && u.After(c) {
		return u.Sub(c).Milliseconds()
	}
	return 0
}

func formatDuration(ms int64) string {
	if ms <= 0 {
		return ""
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	secs := (ms + 500) / 1000
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	rem := secs % 60
	if rem == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm %ds", mins, rem)
}

func parseTS(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func failedReasonLine(it *session.Item) string {
	if txt := strings.TrimSpace(it.Display().Text); txt != "" {
		first, _, _ := strings.Cut(txt, "\n")
		return strings.TrimSpace(first)
	}
	if it.Metadata.Status == statusDenied {
		return "Permission denied"
	}
	if it.Metadata.ExitCode != nil && *it.Metadata.ExitCode != 0 {
		return fmt.Sprintf("Command exited with code %d", *it.Metadata.ExitCode)
	}
	return "Tool execution failed"
}

func toolRightSlot(it *session.Item) (text string, danger bool) {
	m := it.Metadata
	if isItemRunning(it) {
		return "running…", false
	}
	if m.ExitCode != nil && *m.ExitCode != 0 {
		return fmt.Sprintf("exit %d", *m.ExitCode), true
	}
	if m.Status == statusDenied {
		return statusDenied, true
	}
	if m.Status == statusFailed {
		return statusFailed, true
	}
	if m.DurationMillis > 0 {
		return formatDuration(m.DurationMillis), false
	}
	return "", false
}

func detectOutputFormat(it *session.Item) string {
	txt := strings.TrimSpace(it.Display().Text)
	if txt == "" {
		return "text"
	}
	if strings.HasPrefix(txt, "{") || strings.HasPrefix(txt, "[") {
		if json.Valid([]byte(txt)) {
			return "json"
		}
	}
	if strings.HasPrefix(txt, "diff --git ") || (strings.Contains(txt, "\n--- ") && strings.Contains(txt, "\n+++ ")) {
		return formatDiff
	}
	return "text"
}
