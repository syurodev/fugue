package main

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Toast page (UK-09, ui-kit §6.18): inline specimens of the manager's
// own pixels — info, with an action, a warning, one merged from two events
// (the count badge), one paused under the pointer (the timer frozen; the
// pixels are the same, the pause is behavioral) — the three-stack the corner
// shows — and live pushes that exercise the timer, the merge and the
// overflow in the window's bottom-right corner.

const (
	tstInfo     = "info (4 s)"
	tstAction   = "with action (6 s)"
	tstWarning  = "warning (6 s)"
	tstMerged   = "merged (×2)"
	tstPaused   = "paused on hover"
	tstStack    = "the three-stack"
	tstLive     = "live pushes"
	tstLiveInfo = "Push info"
	tstLiveAct  = "Push action"
	tstLiveWarn = "Push warning"
	tstLiveMrg  = "Push merge"
	tstLiveOvr  = "Push overflow"
)

func toastStates() []string {
	return []string{tstInfo, tstAction, tstWarning, tstMerged, tstPaused, tstStack, tstLive}
}

func toastPushStates() []string {
	return []string{tstLiveInfo, tstLiveAct, tstLiveWarn, tstLiveMrg, tstLiveOvr}
}

// Shared specimen copy, so no literal repeats (goconst).
const (
	toastObjInfo   = "gallery-info"
	toastObjAction = "gallery-action"
	toastObjWarn   = "gallery-warn"
	tstInfoTitle   = "Query finished"
	tstInfoDetail  = "12 rows in 1.2 s"
	tstActionTitle = "Snapshot stored"
	tstActionDet   = "Roll back or keep it."
	tstWarnTitle   = "Vault locked"
	tstWarnDetail  = "Background sync is paused."
	tstMergeTitle  = "Index rebuilt"
	tstOvrTitle    = "Session ended"
	toastOpenLbl   = "Open"
)

// numToastSpecimens is the one-toast specimens; the stack's three sit after
// them.
const numToastSpecimens = 5

type toastPage struct {
	specimens [numToastSpecimens + 3]*kit.ToastSpecimen
	triggers  [5]*kit.ButtonState
}

func newToastPage() toastPage {
	var tp toastPage
	specs := []kit.Toast{
		{Object: toastObjInfo, Icon: kit.IconCheck, Title: tstInfoTitle, Detail: tstInfoDetail},
		{Object: toastObjAction, Icon: kit.IconCopy, Title: tstActionTitle, Detail: tstActionDet, Action: toastOpenLbl},
		{Object: toastObjWarn, Icon: kit.IconHelp, Title: tstWarnTitle, Detail: tstWarnDetail, Warning: true},
		{Object: "gallery-merge", Icon: kit.IconRestart, Title: tstMergeTitle},
		{Object: toastObjInfo, Icon: kit.IconCheck, Title: tstInfoTitle, Detail: tstInfoDetail},
		{Object: toastObjInfo, Icon: kit.IconCheck, Title: tstInfoTitle, Detail: tstInfoDetail},
		{Object: toastObjAction, Icon: kit.IconCopy, Title: tstActionTitle, Detail: tstActionDet, Action: toastOpenLbl},
		{Object: toastObjWarn, Icon: kit.IconHelp, Title: tstWarnTitle, Detail: tstWarnDetail, Warning: true},
	}
	for i := range tp.specimens {
		tp.specimens[i] = kit.NewToastSpecimen(&specs[i])
	}
	tp.specimens[3].SetCount(2)
	for i := range tp.triggers {
		tp.triggers[i] = kit.NewButtonState()
	}
	return tp
}

func (p *pages) layoutToasts(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Toast §6.18: at the window's bottom-right corner, above the floating layers. At most three; the same object merges with a count badge; 4 s, or 6 s with an action or a warning; the timer pauses under the pointer; a toast never takes focus. Push some and watch the corner — slow motion (S) shows the slide.") + gtx.Dp(theme.SpaceXL)
	blocks := []block{
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "specimens", []string{tstInfo, tstAction},
				[][]cellDraw{p.toastSpecimenRow(gtx, 0, 2)}, dry)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "warning and merged", []string{tstWarning, tstMerged},
				[][]cellDraw{p.toastSpecimenRow(gtx, 2, 4)}, dry)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "paused", []string{tstPaused},
				[][]cellDraw{p.toastSpecimenRow(gtx, 4, 5)}, dry)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "the corner stack", []string{tstStack}, [][]cellDraw{{p.toastStackCell(gtx)}}, dry)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "live", toastPushStates()[:3], [][]cellDraw{p.toastLiveRow(gtx, 0, 3)}, dry)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "live, merge and overflow", toastPushStates()[3:], [][]cellDraw{p.toastLiveRow(gtx, 3, 5)}, dry)
		},
	}
	p.flow(gtx, y, blocks)
}

// toastSpecimenRow is the row of specimen cells from index a to b.
func (p *pages) toastSpecimenRow(gtx *layout.Context, a, b int) []cellDraw {
	row := make([]cellDraw, 0, b-a)
	for i := a; i < b; i++ {
		row = append(row, p.toastSpecimenCell(gtx, i, i-a))
	}
	return row
}

// toastSpecimenCell is one inline specimen of the manager's pixels.
func (p *pages) toastSpecimenCell(gtx *layout.Context, i, col int) cellDraw {
	sp := p.tst.specimens[i]
	return cellDraw{
		col: col, key: cell{compToast, 0, 0, toastStates()[i]}, tag: sp,
		box: measure(gtx, func(gtx *layout.Context) image.Point { return sp.Measure(p.env, gtx) }),
		lay: func(gtx *layout.Context) {
			p.tst.specimens[i].Layout(p.env, gtx)
		},
	}
}

// toastStackCell is the corner's three-stack: the info over the action over
// the warning, spaced as the manager spaces them, in the specimens' own
// copies so no state draws twice in a frame.
func (p *pages) toastStackCell(gtx *layout.Context) cellDraw {
	stack := p.tst.specimens[numToastSpecimens:]
	gap := gtx.Dp(theme.SpaceSM)
	info := stack[0].Measure(p.env, gtx)
	action := stack[1].Measure(p.env, gtx)
	warn := stack[2].Measure(p.env, gtx)
	w := max(info.X, max(action.X, warn.X))
	h := info.Y + action.Y + warn.Y + 2*gap
	return cellDraw{
		col: 0, key: cell{compToast, 0, 0, tstStack}, tag: stack[0],
		box: image.Pt(w, h),
		lay: func(gtx *layout.Context) {
			at(gtx, image.Pt(0, h-info.Y), func(gtx *layout.Context) { stack[0].Layout(p.env, gtx) })
			at(gtx, image.Pt(0, h-info.Y-action.Y-gap), func(gtx *layout.Context) { stack[1].Layout(p.env, gtx) })
			at(gtx, image.Point{}, func(gtx *layout.Context) { stack[2].Layout(p.env, gtx) })
		},
	}
}

// toastLiveRow is the row of push triggers from index a to b. Each push is a
// live event of the window's toast manager; the outcomes land in ToastEvents.
func (p *pages) toastLiveRow(gtx *layout.Context, a, b int) []cellDraw {
	pushes := []struct {
		label string
		varnt kit.ButtonVariant
		toast kit.Toast
	}{
		{
			tstLiveInfo, kit.Secondary,
			kit.Toast{Object: toastObjInfo, Icon: kit.IconCheck, Title: tstInfoTitle, Detail: tstInfoDetail},
		},
		{
			tstLiveAct, kit.Secondary,
			kit.Toast{Object: toastObjAction, Icon: kit.IconCopy, Title: tstActionTitle, Detail: tstActionDet, Action: toastOpenLbl},
		},
		{
			tstLiveWarn, kit.Danger,
			kit.Toast{Object: toastObjWarn, Icon: kit.IconHelp, Title: tstWarnTitle, Detail: tstWarnDetail, Warning: true},
		},
		{
			tstLiveMrg, kit.Secondary,
			kit.Toast{Object: "gallery-merge", Icon: kit.IconRestart, Title: tstMergeTitle},
		},
		{
			tstLiveOvr, kit.Bordered,
			kit.Toast{Object: "gallery-overflow", Icon: kit.IconEye, Title: tstOvrTitle},
		},
	}
	row := make([]cellDraw, 0, b-a)
	for i := a; i < b; i++ {
		push := &pushes[i]
		btn := p.tst.triggers[i]
		pr := kit.ButtonProps{Text: push.label, Variant: push.varnt}
		row = append(row, cellDraw{
			col: i - a, key: cell{compToast, 1, 0, push.label}, tag: btn.Tag(),
			box: measure(gtx, func(gtx *layout.Context) image.Point { return btn.Measure(p.env, gtx, pr).Size }),
			lay: func(gtx *layout.Context) {
				clicked, err := btn.Layout(p.env, gtx, pr)
				if err != nil {
					p.err = err
				}
				if clicked {
					toast := push.toast
					p.env.ToastPush(&toast)
				}
			},
		})
	}
	return row
}
