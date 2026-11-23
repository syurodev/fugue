package kit

import (
	"errors"
	"image"
	"image/color"

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
	// Text wraps inside the side padding and is centered line by line: an
	// empty state in a 300 dp slot must show its whole sentence.
	textW := max(maxW-2*gtx.Dp(theme.SpaceLG), 0)
	paragraph := func(t theme.Type, col color.NRGBA, txt string) int {
		if txt == "" {
			return 0
		}
		var h int
		at(gtx, image.Pt((maxW-textW)/2, y), func(gtx *layout.Context) {
			g := *gtx
			g.Constraints = layout.Constraints{Max: image.Pt(textW, 1<<20)}
			dims := CenteredParagraph(env, &g, t, col, txt)
			recordRun(env, image.Rectangle{Max: dims.Size}.Add(image.Pt((maxW-textW)/2, y)))
			h = dims.Size.Y
		})
		return h
	}
	if h := paragraph(theme.TypeTitle(), fade(c.Fg, p), pr.Title); h > 0 {
		y += h + gapSM
	}
	if h := paragraph(theme.TypeBody(), fade(c.FgMuted, p), pr.Description); h > 0 {
		y += h + gapMD
	}
	return y, nil
}

func (s *EmptyStateState) layoutActions(env *Env, gtx *layout.Context, pr *EmptyStateProps, maxW, y int, ev *EmptyStateEvents) (int, error) {
	var items []FlowItem
	var berr error
	add := func(st *ButtonState, p ButtonProps, dst *bool) {
		items = append(items, FlowItem{Size: st.Measure(env, gtx, p).Size, Draw: func(g *layout.Context) {
			clicked, err := st.Layout(env, g, p)
			berr = errors.Join(berr, err)
			if clicked {
				*dst = true
			}
		}})
	}
	if pr.NoResults && pr.ClearFilterAction != "" {
		add(s.clearBtn, ButtonProps{Text: pr.ClearFilterAction, Variant: Secondary}, &ev.ClearFilterClicked)
	}
	if pr.PrimaryAction != "" {
		add(s.primaryBtn, ButtonProps{Text: pr.PrimaryAction, Variant: Primary}, &ev.PrimaryClicked)
	}
	if len(items) == 0 {
		return y, nil
	}
	// Buttons that do not fit side by side stack, each row centered.
	gap := gtx.Dp(theme.SpaceMD)
	rects, size := FlowPlace(maxW, gap, gap, items)
	centerFlowRows(rects, maxW)
	for i, it := range items {
		at(gtx, rects[i].Min.Add(image.Pt(0, y)), func(gtx *layout.Context) {
			g := *gtx
			g.Constraints = layout.Constraints{Max: rects[i].Size()}
			it.Draw(&g)
		})
	}
	return y + size.Y, berr
}

// centerFlowRows shifts every row of rects, told apart by x stepping back,
// so it is centered in maxW.
func centerFlowRows(rects []image.Rectangle, maxW int) {
	for lo := 0; lo < len(rects); {
		hi := lo + 1
		for hi < len(rects) && rects[hi].Min.X > rects[hi-1].Min.X {
			hi++
		}
		shift := (maxW - (rects[hi-1].Max.X - rects[lo].Min.X)) / 2
		for i := lo; i < hi; i++ {
			rects[i] = rects[i].Add(image.Pt(shift-rects[lo].Min.X, 0))
		}
		lo = hi
	}
}
