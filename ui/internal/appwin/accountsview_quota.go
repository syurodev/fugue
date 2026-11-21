package appwin

import (
	"fmt"
	"image"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type windowRowHover struct {
	tag     int
	hovered bool
	hasPtr  bool
	ptr     image.Point
}

func layoutQuotaWindows(env *kit.Env, w *accountCardWidgets, acc *client.Account, now time.Time) []layout.FlexChild {
	if !acc.QuotaKnown {
		return []layout.FlexChild{rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layoutUnknownQuotaRow(env, gtx, "Unknown")
		})}
	}
	if len(acc.Windows) == 0 {
		return []layout.FlexChild{rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layoutUnknownQuotaRow(env, gtx, "No data")
		})}
	}
	children := make([]layout.FlexChild, 0, len(acc.Windows)*2)
	for i := range acc.Windows {
		win := acc.Windows[i]
		prog := ensureProgress(w.progress, win.Name)
		rh := ensureRowHover(w.rowHover, win.Name)
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layoutWindowRow(env, gtx, prog, rh, acc.Windows, &win, acc.QuotaGate, now)
		}))
		if i < len(acc.Windows)-1 {
			children = append(children, spacerH(theme.SpaceXS))
		}
	}
	return children
}

func layoutUnknownQuotaRow(env *kit.Env, gtx *layout.Context, rightLabel string) layout.Dimensions {
	c := env.Th.Chrome()
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return kit.LineRow(env, &gtx, kit.LineRowProps{Size: kit.LineMD, Fallback: c.LineRest})
		}),
		spacerW(theme.SpaceMD),
		rigidLabel(env, theme.TypeSmall(), c.FgMuted, rightLabel),
	)
}

func layoutWindowRow(env *kit.Env, gtx *layout.Context, prog *kit.ProgressState, rh *windowRowHover, all []client.QuotaWindow, win *client.QuotaWindow, gate float64, now time.Time) layout.Dimensions {
	c := env.Th.Chrome()
	util := clampUnit(win.Utilization)
	gateFrac := clampUnit(gate)
	tone := windowProgressTone(win.Status)
	label := shortWindowLabel(win.Name)

	rightTxt := fmt.Sprintf("%.0f%%", util*100)
	rightCol := c.FgMuted
	if win.Status == client.QuotaStatusRejected {
		rightTxt = "Limit"
		rightCol = env.Th.Status().Danger
	}
	labelW := measureLabelWidth(env, gtx, theme.TypeSmall(), "7d")
	for i := range all {
		if lw := measureLabelWidth(env, gtx, theme.TypeSmall(), shortWindowLabel(all[i].Name)); lw > labelW {
			labelW = lw
		}
	}
	rightW := max(measureLabelWidth(env, gtx, theme.TypeCodeSmall(), "100%"), measureLabelWidth(env, gtx, theme.TypeCodeSmall(), "Limit"))

	dims := layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			local := *gtx
			local.Constraints.Min.X = labelW
			d := kit.Label(env, &local, theme.TypeSmall(), c.FgMuted, label)
			return layout.Dimensions{Size: image.Pt(max(d.Size.X, labelW), d.Size.Y), Baseline: d.Baseline}
		}),
		spacerW(theme.SpaceMD),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return prog.Layout(env, &gtx, kit.ProgressProps{
				Value:     float32(util),
				Secondary: float32(gateFrac),
				Tone:      tone,
				Size:      kit.LineMD,
			})
		}),
		spacerW(theme.SpaceMD),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			local := *gtx
			local.Constraints.Min.X = 0
			rec := op.Record(local.Ops)
			d := kit.Label(env, &local, theme.TypeCodeSmall(), rightCol, rightTxt)
			call := rec.Stop()
			x := max(rightW-d.Size.X, 0)
			off := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			off.Pop()
			return layout.Dimensions{Size: image.Pt(max(rightW, d.Size.X), d.Size.Y), Baseline: d.Baseline}
		}),
	)

	updateWindowRowHover(gtx, rh, dims.Size)
	if tip := formatWindowTooltip(win.ResetsAt, gateFrac, now); tip != "" {
		env.TooltipAnchor(&rh.tag, image.Rectangle{Max: dims.Size}, rh.hovered, false, rh.hasPtr, rh.ptr, kit.TooltipProps{Text: tip})
	}
	return dims
}

func measureLabelWidth(env *kit.Env, gtx *layout.Context, tok theme.Type, txt string) int {
	rec := op.Record(gtx.Ops)
	local := *gtx
	local.Constraints.Min = image.Point{}
	d := kit.Label(env, &local, tok, env.Th.Chrome().Fg, txt)
	rec.Stop()
	return d.Size.X
}

func updateWindowRowHover(gtx *layout.Context, rh *windowRowHover, size image.Point) {
	if size.X <= 0 || size.Y <= 0 {
		return
	}
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &rh.tag)
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target: &rh.tag,
			Kinds:  pointer.Enter | pointer.Leave | pointer.Move | pointer.Cancel,
		})
		if !ok {
			return
		}
		pe, isPtr := ev.(pointer.Event)
		if !isPtr {
			continue
		}
		switch pe.Kind {
		case pointer.Enter, pointer.Move:
			rh.hovered = true
			rh.hasPtr = true
			rh.ptr = image.Pt(int(pe.Position.X+0.5), int(pe.Position.Y+0.5))
		case pointer.Leave, pointer.Cancel:
			rh.hovered = false
			rh.hasPtr = false
		case pointer.Press, pointer.Release, pointer.Drag, pointer.Scroll:
		}
	}
}

func ensureProgress(m map[string]*kit.ProgressState, name string) *kit.ProgressState {
	if p, ok := m[name]; ok {
		return p
	}
	p := kit.NewProgressState()
	m[name] = p
	return p
}

func ensureRowHover(m map[string]*windowRowHover, name string) *windowRowHover {
	if rh, ok := m[name]; ok {
		return rh
	}
	rh := &windowRowHover{}
	m[name] = rh
	return rh
}

func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func shortWindowLabel(name string) string {
	switch name {
	case "five_hour":
		return "5h"
	case "seven_day":
		return "7d"
	default:
		return name
	}
}

func windowProgressTone(status string) kit.ProgressTone {
	switch status {
	case client.QuotaStatusRejected:
		return kit.ProgressDanger
	case client.QuotaStatusAllowedWarning:
		return kit.ProgressWarning
	default:
		return kit.ProgressDefault
	}
}

func formatPausedChip(pausedUntil string, now time.Time) string {
	if pausedUntil == "" {
		return "Paused"
	}
	t, err := time.Parse(time.RFC3339Nano, pausedUntil)
	if err != nil {
		return "Paused"
	}
	if !now.IsZero() {
		if d := t.Sub(now); d > 0 {
			return "Paused · " + formatShortDuration(d)
		}
	}
	return "Paused · " + t.Local().Format("15:04")
}

func formatWindowTooltip(resetsAt string, gate float64, now time.Time) string {
	resetPart := formatWindowReset(resetsAt, now)
	if gate > 0 {
		return fmt.Sprintf("%s · Teams stop at %.0f%%", resetPart, gate*100)
	}
	return resetPart
}

func formatWindowReset(resetsAt string, now time.Time) string {
	if resetsAt == "" {
		return "Reset unknown"
	}
	t, err := time.Parse(time.RFC3339Nano, resetsAt)
	if err != nil {
		return "Reset unknown"
	}
	localStr := t.Local().Format("15:04")
	if now.IsZero() {
		return "Resets " + localStr
	}
	if d := t.Sub(now); d > 0 {
		return fmt.Sprintf("Resets %s (in %s)", localStr, formatShortDuration(d))
	}
	return fmt.Sprintf("Reset passed (%s)", localStr)
}

func formatShortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "<1m"
	}
	hours := int(d / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	if hours >= 24 {
		days := hours / 24
		remH := hours % 24
		return fmt.Sprintf("%dd%dh", days, remH)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
