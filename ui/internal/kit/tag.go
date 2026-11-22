package kit

import (
	"errors"
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Tag, ui-kit §6.4: a non-interactive pill chip (TypeSmall, optional IconXS,
// radius.full, flat tinted fill, no border). Variants are neutral (FillSoft),
// status (Info, Success, Warning, Danger), hue (8 palette hues) and env
// ("prod" danger, "stg" warning, "dev" neutral, unknown warning). Text always
// carries the meaning, never color alone. Presence animates in with fade plus
// scale from 0.9 (Snappy), removes with fade then width collapse (Exit), and
// color changes follow Snappy; ReducedMotion snaps immediately (UK-04).
//
//	dims, err := tagState.Layout(env, gtx, kit.TagProps{
//	    Variant: kit.TagStatus,
//	    Status:  theme.StatusSuccess,
//	    Text:    "Ready",
//	})

// TagVariant selects the color family of a Tag (§6.4).
type TagVariant uint8

// Tag color families (§6.4).
const (
	TagNeutral TagVariant = iota
	TagStatus
	TagHue
	TagEnv
)

// TagProps are the per-frame, caller-owned properties of Tag (UK-02).
type TagProps struct {
	Text    string
	Icon    IconName
	Variant TagVariant
	Status  theme.StatusKind
	Hue     theme.Hue
	Env     string
	Code    bool // roomier monospace pill for identifiers
}

// TagState is the long-lived state the caller keeps per Tag (UK-02).
type TagState struct {
	pres   motion.Presence
	colorF motion.Follower
	init   bool
	from   theme.Tone
	target theme.Tone
}

// NewTagState builds the state for one Tag, initially present at rest.
func NewTagState() *TagState {
	p := motion.NewPresence(motion.Snappy(), motion.Exit())
	p.Snap(true)
	return &TagState{
		pres:   p,
		colorF: motion.NewFollower(motion.Snappy()),
	}
}

// Enter starts the §6.4 appear animation (fade + scale from 0.9 on Snappy).
func (s *TagState) Enter() {
	s.pres.Snap(false)
	s.pres.Show(true)
}

// Remove starts the §6.4 exit animation (fade out then width collapse on Exit).
func (s *TagState) Remove() { s.pres.Show(false) }

// Visible reports whether the Tag is present or still animating out.
func (s *TagState) Visible() bool { return s.pres.Visible() }

var errTagEmptyText = errors.New("kit.Tag: Text is required")

func tagLabel(p TagProps) string {
	if p.Text != "" {
		return p.Text
	}
	if p.Variant == TagEnv {
		return p.Env
	}
	return ""
}

// ResolveTagTone returns the target theme.Tone for p on th (§6.4).
func ResolveTagTone(th *theme.Theme, p TagProps) theme.Tone {
	switch p.Variant {
	case TagNeutral:
		return th.NeutralTone()
	case TagStatus:
		return th.StatusTone(p.Status)
	case TagHue:
		return th.HueTone(p.Hue)
	case TagEnv:
		switch p.Env {
		case "prod":
			return th.StatusTone(theme.StatusDanger)
		case "dev":
			return th.NeutralTone()
		default:
			// Both "stg" and any unknown environment tag map to warning (§6.4).
			return th.StatusTone(theme.StatusWarning)
		}
	default:
		return th.NeutralTone()
	}
}

// Measure returns the Tag's natural box without drawing or advancing motion.
func (s *TagState) Measure(env *Env, gtx *layout.Context, p TagProps) (layout.Dimensions, error) {
	label := tagLabel(p)
	if label == "" {
		return layout.Dimensions{}, errTagEmptyText
	}
	return layout.Dimensions{Size: tagSize(env, gtx, p, label)}, nil
}

// Layout draws the Tag and advances its presence and Snappy tone transitions (§6.4).
func (s *TagState) Layout(env *Env, gtx *layout.Context, p TagProps) (layout.Dimensions, error) {
	label := tagLabel(p)
	if label == "" {
		return layout.Dimensions{}, errTagEmptyText
	}
	if env.ReducedMotion {
		s.pres.Snap(s.pres.Shown())
	} else if s.pres.Advance(env.DT()) {
		env.Keep()
	}
	if !s.pres.Visible() {
		return layout.Dimensions{}, nil
	}

	box := tagSize(env, gtx, p, label)
	tone := s.stepTone(env, ResolveTagTone(env.Th, p))
	alpha, scale, outW := s.presenceVisuals(box.X)
	if alpha <= 0 || outW <= 0 {
		return layout.Dimensions{Size: image.Pt(outW, box.Y)}, nil
	}

	fill := fade(tone.Fill, alpha)
	fg := fade(tone.Fg, alpha)
	if scale < 1 {
		center := f32.Pt(float32(box.X)/2, float32(box.Y)/2)
		defer scaleAbout(gtx, center, scale).Pop()
	}

	radius := box.Y / 2
	fillRRect(gtx.Ops, image.Rectangle{Max: box}, radius, fill)

	if err := drawTagLabel(env, gtx, p, box, fg, label); err != nil {
		return layout.Dimensions{}, err
	}
	return layout.Dimensions{Size: image.Pt(outW, box.Y)}, nil
}

func drawTagLabel(env *Env, gtx *layout.Context, p TagProps, box image.Point, fg color.NRGBA, label string) error {
	t, padX, _ := tagMetrics(gtx, p.Code)
	x := padX
	if p.Icon != "" {
		iconPx := gtx.Dp(theme.IconXS)
		iconY := (box.Y - iconPx) / 2
		if err := drawIconAt(env, gtx, image.Pt(x, iconY), p.Icon, theme.IconXS, fg); err != nil {
			return err
		}
		x += iconPx + gtx.Dp(theme.SpaceXS)
	}
	textY := centerLabelY(env, gtx, t, box.Y)
	at(gtx, image.Pt(x, textY), func(g *layout.Context) {
		if p.Code {
			g.Constraints.Min.X = 0
			g.Constraints.Max.X = max(0, box.X-x-padX)
			MiddleEllipsisLabel(env, g, t, fg, label)
			return
		}
		drawText(env, g, t, fg, label, true)
	})
	return nil
}

func (s *TagState) presenceVisuals(fullW int) (alpha, scale float32, outW int) {
	prog := unit01(s.pres.Progress())
	if s.pres.Shown() {
		return prog, 0.9 + 0.1*prog, fullW
	}
	// Exit (§6.4): fade out during the first half (1 -> 0.5), then collapse width (0.5 -> 0).
	alpha = unit01((prog - 0.5) * 2)
	widthFrac := unit01(prog * 2)
	outW = int(math.Round(float64(fullW) * float64(widthFrac)))
	return alpha, 1, outW
}

func (s *TagState) stepTone(env *Env, target theme.Tone) theme.Tone {
	if !s.init {
		s.init = true
		s.from = target
		s.target = target
		s.colorF.Snap(1)
		return target
	}
	if target != s.target {
		t := unit01(s.colorF.Value())
		s.from = theme.Tone{
			Fill: mix(s.from.Fill, s.target.Fill, t),
			Fg:   mix(s.from.Fg, s.target.Fg, t),
		}
		s.target = target
		s.colorF.Snap(0)
	}
	t := followState(env, &s.colorF, true)
	return theme.Tone{
		Fill: mix(s.from.Fill, s.target.Fill, t),
		Fg:   mix(s.from.Fg, s.target.Fg, t),
	}
}

func tagMetrics(gtx *layout.Context, code bool) (theme.Type, int, int) {
	if code {
		return theme.TypeCodeSmall(), gtx.Dp(theme.SpaceMD), gtx.Dp(theme.SpaceXS)
	}
	return theme.TypeSmall(), gtx.Dp(theme.SpaceSM), gtx.Dp(theme.SpaceXXS)
}

func tagSize(env *Env, gtx *layout.Context, p TagProps, label string) image.Point {
	t, padX, padY := tagMetrics(gtx, p.Code)
	lbl := measureLabel(env, gtx, t, label)
	w := padX*2 + lbl.X
	h := max(padY*2+labelLine(env, gtx, t), padY*2+lbl.Y)
	if p.Icon != "" {
		iconPx := gtx.Dp(theme.IconXS)
		w += iconPx + gtx.Dp(theme.SpaceXS)
		h = max(h, padY*2+iconPx)
	}
	return gtx.Constraints.Constrain(image.Pt(w, h))
}
