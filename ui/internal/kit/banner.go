package kit

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// BannerKind selects the semantic status tone of a Banner (§6.24).
type BannerKind int

// The §6.24 banner kinds.
const (
	BannerInfo BannerKind = iota
	BannerWarning
	BannerDanger
)

func (k BannerKind) statusKind() theme.StatusKind {
	switch k {
	case BannerInfo:
		return theme.StatusInfo
	case BannerWarning:
		return theme.StatusWarning
	case BannerDanger:
		return theme.StatusDanger
	default:
		return theme.StatusInfo
	}
}

// BannerProps configures an in-flow Banner for one frame (§6.24).
type BannerProps struct {
	Kind        BannerKind
	Title       string
	Message     string
	ActionLabel string
	Dismissible bool
	Visible     bool
}

// BannerEvents reports what happened on a Banner in the current frame.
type BannerEvents struct {
	ActionClicked bool
	Dismissed     bool
}

// BannerState holds the long-lived presence and button state of one Banner
// (§6.24).
type BannerState struct {
	presence  motion.Presence
	actionBtn *ButtonState
	closeBtn  *IconButtonState
	inited    bool
}

// NewBannerState builds a BannerState at rest.
func NewBannerState() *BannerState {
	return &BannerState{
		presence:  motion.NewPresence(motion.Smooth(), motion.Exit()),
		actionBtn: NewButtonState(),
		closeBtn:  NewIconButtonState(),
	}
}

// Presence returns the current open/close progress in [0..1] for motion tests.
func (s *BannerState) Presence() float32 { return s.presence.Progress() }

// Layout lays out the Banner in-flow at the top of its region with Smooth
// height + fade, status tone background, radius.lg, no border, optional action
// and dismiss button (§6.24).
func (s *BannerState) Layout(env *Env, gtx *layout.Context, pr BannerProps) (layout.Dimensions, BannerEvents, error) {
	var ev BannerEvents
	if !s.inited {
		s.inited = true
		s.presence.Snap(pr.Visible)
	} else {
		s.presence.Show(pr.Visible)
	}
	p := stepPresence(env, &s.presence)
	if !s.presence.Visible() {
		return layout.Dimensions{}, ev, nil
	}

	tone := env.Th.StatusTone(pr.Kind.statusKind())
	padX, padY, gap := gtx.Dp(theme.SpaceLG), gtx.Dp(theme.SpaceMD), gtx.Dp(theme.SpaceMD)
	maxW := gtx.Constraints.Max.X
	if maxW <= 0 {
		maxW = gtx.Dp(theme.DialogWidth)
	}

	actSize, actW, closeSize, closeW := s.measureButtons(env, gtx, pr, gap)
	textW := max(maxW-2*padX-actW-closeW, 0)
	alpha := unit01(p)
	textCall, textY := s.recordText(env, gtx, pr, padX, padY, textW, fade(tone.Fg, alpha))

	fullH := max(textY+padY, max(actSize.Y, closeSize.Y)+2*padY)
	visH := int(float32(fullH)*alpha + 0.5)
	if visH <= 0 {
		return layout.Dimensions{Size: image.Pt(maxW, 0)}, ev, nil
	}

	defer clip.Rect{Max: image.Pt(maxW, visH)}.Push(gtx.Ops).Pop()
	fillLayer(gtx, image.Rect(0, 0, maxW, fullH), gtx.Dp(theme.RadiusLG), tone.Fill, alpha)
	textCall.Add(gtx.Ops)

	err := s.layoutButtons(env, gtx, pr, maxW, fullH, padX, gap, actSize, closeSize, &ev)
	return layout.Dimensions{Size: image.Pt(maxW, visH)}, ev, err
}

func (s *BannerState) measureButtons(env *Env, gtx *layout.Context, pr BannerProps, gap int) (actSize image.Point, actW int, closeSize image.Point, closeW int) {
	if pr.ActionLabel != "" {
		actPr := ButtonProps{Text: pr.ActionLabel, Variant: Secondary, Size: SizeSM}
		actSize = s.actionBtn.Measure(env, gtx, actPr).Size
		actW = actSize.X + gap
	}
	if pr.Dismissible {
		closePr := IconButtonProps{Icon: IconClose, Label: "Dismiss", Variant: IconButtonGhost, Size: SizeXS}
		closeSize = s.closeBtn.Measure(env, gtx, &closePr).Size
		closeW = closeSize.X + gap
	}
	return actSize, actW, closeSize, closeW
}

func (s *BannerState) recordText(env *Env, gtx *layout.Context, pr BannerProps, padX, padY, textW int, ink color.NRGBA) (op.CallOp, int) {
	rec := op.Record(gtx.Ops)
	textY := padY
	if pr.Title != "" {
		textY = drawBannerTextLine(env, gtx, padX, textY, textW, theme.TypeLabel(), ink, pr.Title)
	}
	if pr.Message != "" {
		if pr.Title != "" {
			textY += gtx.Dp(theme.SpaceXXS)
		}
		textY = drawBannerTextLine(env, gtx, padX, textY, textW, theme.TypeSmall(), ink, pr.Message)
	}
	return rec.Stop(), textY
}

func drawBannerTextLine(env *Env, gtx *layout.Context, x, y, maxW int, t theme.Type, ink color.NRGBA, txt string) int {
	at(gtx, image.Pt(x, y), func(gtx *layout.Context) {
		g := *gtx
		g.Constraints = layout.Constraints{Max: image.Pt(maxW, gtx.Constraints.Max.Y)}
		d := drawText(env, &g, t, ink, txt, true)
		recordRun(env, image.Rectangle{Max: d.Size}.Add(image.Pt(x, y)))
		y += d.Size.Y
	})
	return y
}

func (s *BannerState) layoutButtons(env *Env, gtx *layout.Context, pr BannerProps, maxW, fullH, padX, gap int, actSize, closeSize image.Point, ev *BannerEvents) error {
	rx := maxW - padX
	if pr.Dismissible {
		rx -= closeSize.X
		closePr := IconButtonProps{Icon: IconClose, Label: "Dismiss", Variant: IconButtonGhost, Size: SizeXS}
		var cerr error
		at(gtx, image.Pt(rx, (fullH-closeSize.Y)/2), func(gtx *layout.Context) {
			var clicked bool
			clicked, cerr = s.closeBtn.Layout(env, gtx, &closePr)
			if clicked {
				ev.Dismissed = true
			}
		})
		if cerr != nil {
			return cerr
		}
		rx -= gap
	}
	if pr.ActionLabel != "" {
		rx -= actSize.X
		actPr := ButtonProps{Text: pr.ActionLabel, Variant: Secondary, Size: SizeSM}
		var aerr error
		at(gtx, image.Pt(rx, (fullH-actSize.Y)/2), func(gtx *layout.Context) {
			var clicked bool
			clicked, aerr = s.actionBtn.Layout(env, gtx, actPr)
			if clicked {
				ev.ActionClicked = true
			}
		})
		if aerr != nil {
			return aerr
		}
	}
	return nil
}
