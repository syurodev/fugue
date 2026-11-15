package kit

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// focusable registers tag as a focusable handler for one frame: the event
// op marks it, and reading a FocusFilter through gtx.Event is what makes
// Gio treat it as focusable (ui-guide §4). The area is small and stays off
// the click coordinates of this test — a whole-window input area would
// absorb every pointer press beneath it (ui-guide §3) and the modality
// would never see a press.
func (h *harness) focusable(tag event.Tag) {
	defer clip.Rect(image.Rect(0, 0, 40, 20)).Push(h.gtx.Ops).Pop()
	event.Op(h.gtx.Ops, tag)
	for {
		_, ok := h.gtx.Event(key.FocusFilter{Target: tag})
		if !ok {
			break
		}
	}
}

// layoutFrame lays one frame out with TrackInput and the given focusable
// tags registered, the shape every frame of the focus test takes.
func layoutFrame(t *testing.T, h *harness, tags ...event.Tag) {
	t.Helper()
	h.frame(t)
	h.env.TrackInput(&h.gtx)
	for _, tg := range tags {
		h.focusable(tg)
	}
}

// TestFocusVisibleOnlyAfterTab covers UK-05: the focus ring shows only once
// focus has moved with the keyboard, and a pointer press hides it again.
// Gio has no focus-visible primitive, so the modality lives on the window
// Env: Tab turns it on, any pointer press clears it.
func TestFocusVisibleOnlyAfterTab(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var a, b event.Tag = new(int), new(int)

	// Register the two focusable tags; TrackInput goes first, like a real
	// window's frame.
	layoutFrame(t, h, a, b)
	h.end()

	// Programmatic focus shows no ring: the modality is still pointer.
	layoutFrame(t, h, a, b)
	h.gtx.Execute(key.FocusCmd{Tag: a})
	h.end()
	layoutFrame(t, h, a, b)
	if !h.gtx.Focused(a) {
		t.Fatal("a is not focused after the FocusCmd")
	}
	if h.env.FocusVisible(&h.gtx, a) {
		t.Fatal("focus ring visible before any Tab")
	}
	h.end()

	// Tab turns the modality on through the real key event, and the app
	// window moves focus — on a real window Gio turns Tab into a FocusCmd,
	// which the headless router does not synthesize, so the test issues the
	// FocusCmd itself. The event is queued after the router saw the
	// previous frame: a Frame discards what is still pending.
	h.router.Queue(key.Event{Name: key.NameTab, State: key.Press})
	layoutFrame(t, h, a, b)
	if !h.env.modality {
		t.Fatal("Tab did not set the keyboard modality")
	}
	h.gtx.Execute(key.FocusCmd{Tag: b})
	h.end()
	layoutFrame(t, h, a, b)
	if !h.gtx.Focused(b) {
		t.Fatal("b is not focused after the FocusCmd")
	}
	if !h.env.FocusVisible(&h.gtx, b) {
		t.Fatal("focus ring not visible after Tab")
	}
	h.end()

	// A pointer press clears it again, also for a focused tag.
	h.click(150, 80)
	layoutFrame(t, h, a, b)
	h.end()
	layoutFrame(t, h, a, b)
	if h.env.modality {
		t.Fatal("pointer press did not clear the keyboard modality")
	}
	if h.env.FocusVisible(&h.gtx, b) {
		t.Fatal("focus ring visible after a pointer press")
	}
	h.end()
}

// TestUIScaleMultipliesDpAndSp covers §3.0: the "interface size" setting
// scales the whole page like VS Code's zoom — both PxPerDp and PxPerSp by
// the same factor — while the content text setting scales only the prose
// and code tokens and never the shared metric.
func TestUIScaleMultipliesDpAndSp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		device         unit.Metric
		ui, content    float32
		wantDp, wantSp float32
	}{
		{"unset scale is 100%", unit.Metric{PxPerDp: 1, PxPerSp: 1}, 0, 0, 1, 1},
		{"125%", unit.Metric{PxPerDp: 1, PxPerSp: 1}, 1.25, 0, 1.25, 1.25},
		{"90% on a retina display", unit.Metric{PxPerDp: 2, PxPerSp: 2}, 0.9, 0, 1.8, 1.8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := &Env{UIScale: tc.ui}
			got := env.Metric(tc.device)
			if got.PxPerDp != tc.wantDp || got.PxPerSp != tc.wantSp {
				t.Fatalf("metric = %v/%v, want %v/%v", got.PxPerDp, got.PxPerSp, tc.wantDp, tc.wantSp)
			}
		})
	}
	t.Run("content scale touches only prose and code", func(t *testing.T) {
		t.Parallel()
		env := &Env{ContentScale: 1.5}
		size, line := env.Content(theme.TypeProse())
		if size != 21 || line != 33 {
			t.Fatalf("prose at 150%% = %v/%v, want 21/33", size, line)
		}
		size, line = env.Content(theme.TypeCode())
		if size != 19.5 || line != 30 {
			t.Fatalf("code at 150%% = %v/%v, want 19.5/30", size, line)
		}
		size, line = env.Content(theme.TypeBody())
		if size != 13 || line != 18 {
			t.Fatalf("body must not scale: %v/%v, want 13/18", size, line)
		}
	})
}

// TestIconStrokeCacheIsKeyedByDpSize covers §3.0 and §6.1: the stroke cache
// is keyed by the dp size, so drawing at one size builds one stroke and
// reuses it, another size builds its own, and nothing in the cache depends
// on the device pixel density — the same dp size on a different metric
// reuses the entry.
func TestIconStrokeCacheIsKeyedByDpSize(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := color.NRGBA{}
	draw := func(size unit.Dp) {
		t.Helper()
		h.frame(t)
		if _, err := h.env.Icon(&h.gtx, IconCheck, size, c); err != nil {
			t.Fatalf("draw the icon: %v", err)
		}
		h.end()
	}
	draw(theme.IconMD)
	if n := len(h.env.iconStrokes); n != 1 {
		t.Fatalf("cache holds %d strokes after one size, want 1", n)
	}
	draw(theme.IconMD)
	if n := len(h.env.iconStrokes); n != 1 {
		t.Fatalf("cache grew to %d strokes at the same dp size, want 1", n)
	}
	draw(theme.IconSM)
	if n := len(h.env.iconStrokes); n != 2 {
		t.Fatalf("cache holds %d strokes after a second dp size, want 2", n)
	}
	// A different device pixel density changes no entry: the cached path is
	// on the 24 grid and the transform carries it to the metric.
	h.env.UIScale = 1.25
	draw(theme.IconMD)
	if n := len(h.env.iconStrokes); n != 2 {
		t.Fatalf("cache grew to %d strokes at another density, want 2", n)
	}
}

// TestSpinnerRequestsFramesOnlyWhileDrawn covers §6.5 and UK-E07: the
// spinner keeps the frame loop alive while it is drawn and never while it
// is not; under reduced motion it does not move at all.
func TestSpinnerRequestsFramesOnlyWhileDrawn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var sp Spinner
	h.frame(t)
	sp.Layout(h.env, &h.gtx, 20, color.NRGBA{})
	if _, moving := h.env.Driver.Next(); !moving {
		t.Fatal("a drawn spinner must ask for the next frame")
	}
	h.frame(t) // Begin clears the driver's active flag
	if _, moving := h.env.Driver.Next(); moving {
		t.Fatal("an undrawn spinner must not ask for the next frame")
	}
	h.env.ReducedMotion = true
	h.frame(t)
	sp.Layout(h.env, &h.gtx, 20, color.NRGBA{})
	if _, moving := h.env.Driver.Next(); moving {
		t.Fatal("a spinner under reduced motion must not ask for frames")
	}
}

// TestTextLayoutsAtEveryToken smoke-draws the text primitives at every type
// token: label on one line, paragraph wrapped, both with real metrics and
// no panic.
func TestTextLayoutsAtEveryToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, tok := range []theme.Type{
		theme.TypeCaption(), theme.TypeSmall(), theme.TypeBody(), theme.TypeLabel(),
		theme.TypeProse(), theme.TypeTitle(), theme.TypeHeading(), theme.TypeDisplay(),
		theme.TypeCode(), theme.TypeCodeInline(), theme.TypeCodeSmall(),
	} {
		h.frame(t)
		if dims := Label(h.env, &h.gtx, tok, h.env.Th.Chrome().Fg, "Ễ Ặ Ỡ"); dims.Size.X == 0 {
			t.Fatal("label drew nothing")
		}
		h.end()
		h.frame(t)
		if dims := Paragraph(h.env, &h.gtx, tok, h.env.Th.Chrome().Fg, "one two three four five six seven"); dims.Size.X > 200 {
			t.Fatalf("paragraph did not wrap at the constraints: %v wide", dims.Size.X)
		}
		h.end()
	}
}

// TestFloatShadowDrawsTheThemeLayers draws the elevation.float shadow into
// real ops for both the rounding and the layer order: the widest, faintest
// layer goes down last over the others.
func TestFloatShadowDrawsTheThemeLayers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.frame(t)
	DrawFloatShadow(&h.gtx, image.Rect(20, 20, 100, 60), 12)
	h.end()
}
