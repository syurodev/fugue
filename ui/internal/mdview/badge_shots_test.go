package mdview

import (
	"bytes"
	"image"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	badgeShotW = 280
	badgeShotH = 260
)

type badgeShotSpec struct {
	name    string
	light   bool
	uiScale float32
}

func badgeShotSpecs() []badgeShotSpec {
	specs := make([]badgeShotSpec, 0, 6)
	for _, sc := range []struct {
		label string
		scale float32
	}{{"ui090", 0.9}, {"ui100", 1.0}, {"ui125", 1.25}} {
		for _, mode := range []struct {
			label string
			light bool
		}{{"light", true}, {"dark", false}} {
			specs = append(specs, badgeShotSpec{
				name:    "file-badges-" + mode.label + "-" + sc.label,
				light:   mode.light,
				uiScale: sc.scale,
			})
		}
	}
	return specs
}

func renderBadgeShot(t *testing.T, win *headless.Window, spec badgeShotSpec) *image.RGBA {
	t.Helper()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Dark(shaper)
	if spec.light {
		th = theme.Light(shaper)
	}
	env := &kit.Env{Th: &th, Sh: shaper, UIScale: spec.uiScale, ReducedMotion: true}
	var ops op.Ops
	var router input.Router
	now := time.Unix(1_700_000_000, 0)
	size := image.Pt(badgeShotW, badgeShotH)
	specs := []BadgeSpec{
		{Kind: BadgeFile, File: FileRef{Path: "main.go"}},
		{Kind: BadgeFile, File: FileRef{Path: "index.ts"}},
		{Kind: BadgeFile, File: FileRef{Path: "package.json"}},
		{Kind: BadgeFile, File: FileRef{Path: "README.md"}},
		{Kind: BadgeFile, File: FileRef{Path: "Dockerfile"}},
		{Kind: BadgeFolder, File: FileRef{Path: "ui/internal/mdview"}},
		{Kind: BadgeFile, File: FileRef{Path: "unknown.xyz"}},
	}
	clicks := make([]widget.Clickable, len(specs))
	for range 2 {
		ops.Reset()
		env.Begin(now)
		paint.FillShape(&ops, th.Chrome().CanvasBg, clip.Rect{Max: size}.Op())
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
			Constraints: layout.Exact(size),
			Source:      router.Source(),
			Now:         now,
		}
		layout.UniformInset(theme.SpaceMD).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			kids := make([]layout.FlexChild, 0, len(specs)*2)
			for i := range specs {
				idx := i
				if idx > 0 {
					kids = append(kids, layout.Rigid(layout.Spacer{Height: theme.SpaceXS}.Layout))
				}
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return DrawBadge(env, &l, &clicks[idx], specs[idx], nil)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		})
		router.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatalf("render badge shot %s: %v", spec.name, err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("screenshot %s: %v", spec.name, err)
	}
	return img
}

// TestFileBadgeShotsRender renders the UK-E13 file and folder badge matrix
// (Go, TypeScript, JSON, Markdown, Dockerfile, folder, unknown file) in light
// and dark modes at 90%, 100%, and 125% UI scale.
func TestFileBadgeShotsRender(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(badgeShotW, badgeShotH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range badgeShotSpecs() {
		img := renderBadgeShot(t, win, spec)
		if isUniformRGBA(img) {
			t.Fatalf("shot %s rendered uniform image", spec.name)
		}
		shottest.Compare(t, spec.name, img)
		rendered[spec.name] = img
	}
	if bytes.Equal(rendered["file-badges-light-ui100"].Pix, rendered["file-badges-dark-ui100"].Pix) {
		t.Fatal("file badge light and dark shots are identical")
	}
	if bytes.Equal(rendered["file-badges-light-ui090"].Pix, rendered["file-badges-light-ui100"].Pix) ||
		bytes.Equal(rendered["file-badges-light-ui100"].Pix, rendered["file-badges-light-ui125"].Pix) {
		t.Fatal("file badge shots at 90/100/125% must differ")
	}
}

func isUniformRGBA(img *image.RGBA) bool {
	if len(img.Pix) < 4 {
		return true
	}
	first := img.Pix[:4]
	for i := 4; i+4 <= len(img.Pix); i += 4 {
		if !bytes.Equal(img.Pix[i:i+4], first) {
			return false
		}
	}
	return true
}
