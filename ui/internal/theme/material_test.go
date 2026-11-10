package theme_test

import (
	"image/color"
	"testing"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestMaterialReflectsTokens pins the bridge to stock Gio widgets: the
// material theme Material() returns carries the mode's Fg, Bg, ContrastBg
// (the action fill) and ContrastFg, and shares the shaper it was built with.
func TestMaterialReflectsTokens(t *testing.T) {
	t.Parallel()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build the shaper: %v", err)
	}
	for _, m := range []struct {
		name  string
		theme theme.Theme
	}{
		{name: "light", theme: theme.Light(shaper)},
		{name: "dark", theme: theme.Dark(shaper)},
	} {
		mt, c := m.theme.Material(), m.theme.Chrome()
		if mt.Fg != c.Fg {
			t.Errorf("%s: material Fg is %#v, want the chrome fg %#v", m.name, mt.Fg, c.Fg)
		}
		if mt.Bg != c.AppBg {
			t.Errorf("%s: material Bg is %#v, want the app bg %#v", m.name, mt.Bg, c.AppBg)
		}
		if mt.ContrastBg != c.Action {
			t.Errorf("%s: material ContrastBg is %#v, want the action fill %#v", m.name, mt.ContrastBg, c.Action)
		}
		if mt.ContrastFg != c.ActionFg {
			t.Errorf("%s: material ContrastFg is %#v, want the action fg %#v", m.name, mt.ContrastFg, c.ActionFg)
		}
		if mt.Shaper != shaper {
			t.Errorf("%s: material theme does not share the shaper it was built with", m.name)
		}
	}
}

// TestMaterialReturnsIndependentShallowCopies pins the per-frame contract:
// the mode's material theme is built once, Material() hands out shallow
// copies (distinct pointers sharing the cached icons), and restyling one
// copy never leaks into the next call.
func TestMaterialReturnsIndependentShallowCopies(t *testing.T) {
	t.Parallel()
	dark := theme.Dark(nil)
	first := dark.Material()
	second := dark.Material()
	if first == second {
		t.Fatal("Material() returned the same pointer; callers must get independent copies")
	}
	if first.Icon.CheckBoxChecked != second.Icon.CheckBoxChecked {
		t.Error("copies do not share the icon cache; widget.Icon would re-render per copy")
	}
	first.Fg = color.NRGBA{R: 0xde, G: 0xad, B: 0xbe, A: 0xff}
	if dark.Material().Fg == first.Fg {
		t.Error("a restyled copy leaked into later Material() calls")
	}
}
