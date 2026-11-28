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

func compareViewShotSpecs() []kitShotSpec {
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
				name:    "compareview-" + mode.label + "-" + sc.label,
				light:   mode.light,
				uiScale: sc.scale,
			})
		}
	}
	return specs
}

func renderCompareViewShot(t *testing.T, win *headless.Window, spec kitShotSpec) *image.RGBA {
	t.Helper()
	h := newHarness(t)
	th := theme.Dark(h.env.Sh)
	if spec.light {
		th = theme.Light(h.env.Sh)
	}
	h.env.Th = &th
	h.env.UIScale = spec.uiScale
	size := image.Pt(640, 320)
	h.frameSize(t, size.X, size.Y)
	paint.Fill(h.gtx.Ops, th.Chrome().CanvasBg)

	cv := NewCompareViewState()
	props := CompareViewProps{
		LeftTitle:  "On this machine",
		RightTitle: "In the file",
		Rows: []CompareRow{
			{Name: "Kind", Left: "decision", Right: "decision", Changed: false},
			{Name: "Summary", Left: "Use Argon2id for vault sealing", Right: "Use Argon2id (64 MiB) for vault sealing", Changed: true},
			{Name: "Body", Left: "Vault key derivation uses host defaults.", Right: "Vault key derivation uses 64 MiB memory and 3 iterations.", Changed: true},
		},
	}
	layout.UniformInset(theme.SpaceMD).Layout(h.gtx, func(gtx layout.Context) layout.Dimensions {
		d, err := cv.Layout(h.env, &gtx, props)
		if err != nil {
			t.Fatalf("CompareView shot layout: %v", err)
		}
		return d
	})
	if err := win.Frame(&h.ops); err != nil {
		t.Fatalf("render compareview shot %s: %v", spec.name, err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("screenshot %s: %v", spec.name, err)
	}
	return img
}

func TestCompareViewShotsRender(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(640, 320)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range compareViewShotSpecs() {
		img := renderCompareViewShot(t, win, spec)
		if isUniformImage(img) {
			t.Fatalf("shot %s rendered uniform image", spec.name)
		}
		shottest.Compare(t, spec.name, img)
		rendered[spec.name] = img
	}
	if imagesEqual(rendered["compareview-light-ui100"], rendered["compareview-dark-ui100"]) {
		t.Fatal("compareview light and dark shots are identical")
	}
	if imagesEqual(rendered["compareview-light-ui100"], rendered["compareview-light-ui125"]) {
		t.Fatal("compareview shots at ui100 and ui125 must differ")
	}
}
