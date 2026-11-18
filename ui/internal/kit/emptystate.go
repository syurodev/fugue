package kit

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// EmptyStateProps configures an EmptyState for one frame (§6.25).
type EmptyStateProps struct {
	Icon              IconName
	Title             string
	Description       string
	PrimaryAction     string
	NoResults         bool
	ClearFilterAction string
}

// EmptyStateEvents reports button clicks on an EmptyState in the current
// frame.
type EmptyStateEvents struct {
	PrimaryClicked     bool
	ClearFilterClicked bool
}

// EmptyStateState holds the long-lived presence and button state of one
// EmptyState (§6.25).
type EmptyStateState struct {
	presence   motion.Presence
	primaryBtn *ButtonState
	clearBtn   *ButtonState
	inited     bool
}

// NewEmptyStateState builds an EmptyStateState at rest.
func NewEmptyStateState() *EmptyStateState {
	return &EmptyStateState{
		presence:   motion.NewPresence(motion.Smooth(), motion.Exit()),
		primaryBtn: NewButtonState(),
		clearBtn:   NewButtonState(),
	}
}

// Presence returns the current fade/rise progress in [0..1] for motion tests.
func (s *EmptyStateState) Presence() float32 { return s.presence.Progress() }

// Layout lays out the EmptyState centered within constraints, with Smooth fade
// + 4 dp upward translate, optional icon, inviting title, one-sentence
// description, primary action, and the "no results" clear-filter variant (§6.25).
func (s *EmptyStateState) Layout(env *Env, gtx *layout.Context, pr *EmptyStateProps) (layout.Dimensions, EmptyStateEvents, error) {
	var ev EmptyStateEvents
	if !s.inited {
		s.inited = true
		s.presence.Show(true)
		if env.ReducedMotion {
			s.presence.Snap(true)
		}
	}
	p := unit01(stepPresence(env, &s.presence))
	dy := int(float32(gtx.Dp(theme.SpaceXS))*(1-p) + 0.5)
	if env.ReducedMotion {
		dy = 0
	}

	maxW := gtx.Constraints.Max.X
	if maxW <= 0 {
		maxW = gtx.Dp(theme.DialogWidth)
	}
	padY := gtx.Dp(theme.SpaceXXL)
	y, err := s.drawHeader(env, gtx, pr, maxW, padY+dy, p)
	if err != nil {
		return layout.Dimensions{}, ev, err
	}
	y, err = s.layoutActions(env, gtx, pr, maxW, y, &ev)
	return layout.Dimensions{Size: image.Pt(maxW, y+padY)}, ev, err
}

func (s *EmptyStateState) drawHeader(env *Env, gtx *layout.Context, pr *EmptyStateProps, maxW, y int, p float32) (int, error) {
	c := env.Th.Chrome()
	gapSM, gapMD := gtx.Dp(theme.SpaceSM), gtx.Dp(theme.SpaceMD)
	if pr.Icon != "" {
		badge := gtx.Dp(theme.SpaceXXXL)
		bx := (maxW - badge) / 2
		fillLayer(gtx, image.Rect(bx, y, bx+badge, y+badge), badge/2, c.FillSoft, p)
		iconPx := gtx.Dp(theme.IconLG)
		if err := drawIconAt(env, gtx, image.Pt(bx+(badge-iconPx)/2, y+(badge-iconPx)/2), pr.Icon, theme.IconLG, fade(c.FgMuted, p)); err != nil {
			return y, err
		}
		y += badge + gapMD
	}
	if pr.Title != "" {
		ts := measureLabel(env, gtx, theme.TypeTitle(), pr.Title)
		tx := max((maxW-ts.X)/2, 0)
		drawRunAt(env, gtx, image.Pt(tx, y), maxW-2*tx, ts.Y, theme.TypeTitle(), fade(c.Fg, p), pr.Title, true)
		y += ts.Y + gapSM
	}
	if pr.Description != "" {
		ds := measureLabel(env, gtx, theme.TypeBody(), pr.Description)
		dx := max((maxW-ds.X)/2, 0)
		drawRunAt(env, gtx, image.Pt(dx, y), maxW-2*dx, ds.Y, theme.TypeBody(), fade(c.FgMuted, p), pr.Description, true)
		y += ds.Y + gapMD
	}
	return y, nil
}

func (s *EmptyStateState) layoutActions(env *Env, gtx *layout.Context, pr *EmptyStateProps, maxW, y int, ev *EmptyStateEvents) (int, error) {
	gap := gtx.Dp(theme.SpaceMD)
	var btns []struct {
		st  *ButtonState
		pr  ButtonProps
		dst *bool
	}
	if pr.NoResults && pr.ClearFilterAction != "" {
		btns = append(btns, struct {
			st  *ButtonState
			pr  ButtonProps
			dst *bool
		}{s.clearBtn, ButtonProps{Text: pr.ClearFilterAction, Variant: Secondary}, &ev.ClearFilterClicked})
	}
	if pr.PrimaryAction != "" {
		btns = append(btns, struct {
			st  *ButtonState
			pr  ButtonProps
			dst *bool
		}{s.primaryBtn, ButtonProps{Text: pr.PrimaryAction, Variant: Primary}, &ev.PrimaryClicked})
	}
	if len(btns) == 0 {
		return y, nil
	}

	sizes := make([]image.Point, len(btns))
	totalW, maxH := 0, 0
	for i, b := range btns {
		sizes[i] = b.st.Measure(env, gtx, b.pr).Size
		totalW += sizes[i].X
		if i > 0 {
			totalW += gap
		}
		maxH = max(maxH, sizes[i].Y)
	}
	x := max((maxW-totalW)/2, 0)
	for i, b := range btns {
		var berr error
		at(gtx, image.Pt(x, y), func(gtx *layout.Context) {
			var clicked bool
			clicked, berr = b.st.Layout(env, gtx, b.pr)
			if clicked {
				*b.dst = true
			}
		})
		if berr != nil {
			return y + maxH, berr
		}
		x += sizes[i].X + gap
	}
	return y + maxH, nil
}
