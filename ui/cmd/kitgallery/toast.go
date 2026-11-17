package main

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Toast page (UK-09, ui-kit §6.18): the variants as live pushes — info
// (4 s), with an action and a warning (6 s), the same object merging into one
// toast with a count, and a fourth push exiting the oldest so at most three
// are visible. The toasts draw at the window's bottom-right corner, above the
// floating layers, where the timer pauses under the pointer and every
// outcome comes back through ToastEvents.

const (
	tstInfo     = "info (4 s)"
	tstAction   = "with action (6 s)"
	tstWarning  = "warning (6 s)"
	tstMerge    = "merge (same object)"
	tstOverflow = "overflow (fourth)"
)

func toastStates() []string {
	return []string{tstInfo, tstAction, tstWarning, tstMerge, tstOverflow}
}

type toastPage struct {
	triggers [5]*kit.ButtonState
}

func newToastPage() toastPage {
	var tp toastPage
	for i := range tp.triggers {
		tp.triggers[i] = kit.NewButtonState()
	}
	return tp
}

func (p *pages) layoutToasts(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Toast §6.18: at the window's bottom-right corner, above the floating layers. At most three; the same object merges; 4 s, or 6 s with an action or a warning; the timer pauses under the pointer; a toast never takes focus. Push some and watch the corner — slow motion (S) shows the slide.") + gtx.Dp(theme.SpaceXL)
	blocks := []block{
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "variants (push a toast)", toastStates(), [][]cellDraw{p.toastRow(gtx)}, dry)
		},
	}
	p.flow(gtx, y, blocks)
}

// toastRow is the row of push triggers. Each push is a live event of the
// window's toast manager; the outcomes land in ToastEvents.
func (p *pages) toastRow(gtx *layout.Context) []cellDraw {
	pushes := []struct {
		st    string
		label string
		varnt kit.ButtonVariant
		toast kit.Toast
	}{
		{
			tstInfo, "Push info", kit.Secondary,
			kit.Toast{Object: "gallery-info", Icon: kit.IconCheck, Title: "Query finished", Detail: "12 rows in 1.2 s"},
		},
		{
			tstAction, "Push action", kit.Secondary,
			kit.Toast{Object: "gallery-action", Icon: kit.IconCopy, Title: "Snapshot stored", Detail: "Roll back or keep it.", Action: "Open"},
		},
		{
			tstWarning, "Push warning", kit.Danger,
			kit.Toast{Object: "gallery-warn", Icon: kit.IconHelp, Title: "Vault locked", Detail: "Background sync is paused.", Warning: true},
		},
		{
			tstMerge, "Push merge", kit.Secondary,
			kit.Toast{Object: "gallery-merge", Icon: kit.IconRestart, Title: "Index rebuilt"},
		},
		{
			tstOverflow, "Push overflow", kit.Bordered,
			kit.Toast{Object: "gallery-overflow", Icon: kit.IconEye, Title: "Session ended"},
		},
	}
	row := make([]cellDraw, 0, len(pushes))
	for ci, push := range pushes {
		btn := p.tst.triggers[ci]
		pr := kit.ButtonProps{Text: push.label, Variant: push.varnt}
		row = append(row, cellDraw{
			col: ci, key: cell{compToast, 0, 0, push.st}, tag: btn.Tag(),
			box: measure(gtx, func(gtx *layout.Context) image.Point { return btn.Measure(p.env, gtx, pr).Size }),
			lay: func(gtx *layout.Context) {
				clicked, err := btn.Layout(p.env, gtx, pr)
				if err != nil {
					p.err = err
				}
				if clicked {
					p.env.ToastPush(push.toast)
				}
			},
		})
	}
	return row
}
