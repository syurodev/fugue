package kit

import (
	"image"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type kitShotSpec struct {
	name    string
	light   bool
	uiScale float32
}

func filterBarShotSpecs() []kitShotSpec {
	specs := make([]kitShotSpec, 0, 4)
	for _, sc := range []struct {
		label string
		scale float32
	}{{"ui100", 1.0}, {"ui125", 1.25}} {
		for _, mode := range []struct {
			label string
			light bool
		}{{modeLight, true}, {modeDark, false}} {
			specs = append(specs, kitShotSpec{
				name:    "filterbar-" + mode.label + "-" + sc.label,
				light:   mode.light,
				uiScale: sc.scale,
			})
		}
	}
	return specs
}

func renderFilterBarShot(t *testing.T, win *headless.Window, spec kitShotSpec) *image.RGBA {
	t.Helper()
	h := newHarness(t)
	th := theme.Dark(h.env.Sh)
	if spec.light {
		th = theme.Light(h.env.Sh)
	}
	h.env.Th = &th
	h.env.UIScale = spec.uiScale
	size := image.Pt(640, 120)
	h.frameSize(t, size.X, size.Y)
	paint.Fill(h.gtx.Ops, th.Chrome().CanvasBg)

	bar := NewFilterBarState()
	bar.SetQuery("auth")
	props := FilterBarProps{
		SearchPlaceholder: "Search memory…",
		Filters: []FilterSelect{
			{ID: "kind", Placeholder: "Kind", Selected: "fact", Options: []SelectOption{{ID: "fact", Label: "Fact"}, {ID: "decision", Label: "Decision"}}},
			{ID: "verified", Placeholder: "Verified", Options: []SelectOption{{ID: "unverified", Label: "Unverified"}}},
		},
	}
	layout.UniformInset(theme.SpaceMD).Layout(h.gtx, func(gtx layout.Context) layout.Dimensions {
		d, _, err := bar.Layout(h.env, &gtx, props)
		if err != nil {
			t.Fatalf("FilterBar shot layout: %v", err)
		}
		return d
	})
	if err := win.Frame(&h.ops); err != nil {
		t.Fatalf("render filterbar shot %s: %v", spec.name, err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("screenshot %s: %v", spec.name, err)
	}
	return img
}

func TestFilterBarShotsRender(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(640, 120)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range filterBarShotSpecs() {
		img := renderFilterBarShot(t, win, spec)
		if isUniformImage(img) {
			t.Fatalf("shot %s rendered uniform image", spec.name)
		}
		shottest.Compare(t, spec.name, img)
		rendered[spec.name] = img
	}
	if imagesEqual(rendered["filterbar-light-ui100"], rendered["filterbar-dark-ui100"]) {
		t.Fatal("filterbar light and dark shots are identical")
	}
	if imagesEqual(rendered["filterbar-light-ui100"], rendered["filterbar-light-ui125"]) {
		t.Fatal("filterbar shots at ui100 and ui125 must differ")
	}
}
