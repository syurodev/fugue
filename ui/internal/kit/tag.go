package kit

import (
	"errors"
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Tag, ui-kit §6.4: a non-interactive pill chip (TypeSmall, optional IconXS,
// radius.full, flat tinted fill, no border). Variants are neutral (FillSoft),
// status (Info, Success, Warning, Danger), hue (8 palette hues) and env
// ("prod" danger, "stg" warning, "dev" neutral, unknown warning). Text always
// carries the meaning, never color alone. Color transitions follow Snappy.
//
//	dims, err := tagState.Layout(env, gtx, kit.TagProps{
//	    Variant: kit.TagStatus,
//	    Status:  theme.StatusSuccess,
//	    Text:    "Ready",
//	})

// TagVariant selects the color family of a Tag (§6.4).
type TagVariant uint8

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
}

// TagState is the long-lived state the caller keeps per Tag (UK-02).
type TagState struct {
	colorF motion.Follower
	init   bool
	from   theme.Tone
	target theme.Tone
}

// NewTagState builds the state for one Tag.
func NewTagState() *TagState {
	return &TagState{colorF: motion.NewFollower(motion.Snappy())}
}

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
	return layout.Dimensions{Size: tagSize(env, gtx, p.Icon, label)}, nil
}

// Layout draws the Tag and advances any Snappy tone transition (§6.4).
func (s *TagState) Layout(env *Env, gtx *layout.Context, p TagProps) (layout.Dimensions, error) {
	label := tagLabel(p)
	if label == "" {
		return layout.Dimensions{}, errTagEmptyText
	}
	box := tagSize(env, gtx, p.Icon, label)
	tone := s.stepTone(env, ResolveTagTone(env.Th, p))

	radius := box.Y / 2
	fillRRect(gtx.Ops, image.Rectangle{Max: box}, radius, tone.Fill)

	padX := gtx.Dp(theme.SpaceSM)
	x := padX
	if p.Icon != "" {
		iconPx := gtx.Dp(theme.IconXS)
		iconY := (box.Y - iconPx) / 2
		if err := drawIconAt(env, gtx, image.Pt(x, iconY), p.Icon, theme.IconXS, tone.Fg); err != nil {
			return layout.Dimensions{}, err
		}
		x += iconPx + gtx.Dp(theme.SpaceXS)
	}
	textY := centerLabelY(env, gtx, theme.TypeSmall(), box.Y)
	at(gtx, image.Pt(x, textY), func(g *layout.Context) {
		drawText(env, g, theme.TypeSmall(), tone.Fg, label, true)
	})
	return layout.Dimensions{Size: box}, nil
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

func tagSize(env *Env, gtx *layout.Context, icon IconName, label string) image.Point {
	padX := gtx.Dp(theme.SpaceSM)
	padY := gtx.Dp(theme.SpaceXXS)
	lbl := measureLabel(env, gtx, theme.TypeSmall(), label)
	w := padX*2 + lbl.X
	h := max(padY*2+labelLine(env, gtx, theme.TypeSmall()), padY*2+lbl.Y)
	if icon != "" {
		iconPx := gtx.Dp(theme.IconXS)
		w += iconPx + gtx.Dp(theme.SpaceXS)
		h = max(h, padY*2+iconPx)
	}
	return gtx.Constraints.Constrain(image.Pt(w, h))
}
