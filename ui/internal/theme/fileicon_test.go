package theme_test

import (
	"testing"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestFileIconColorsMeetContrastLightAndDark pins UK-E13: every file-icon
// color name reaches at least 3:1 on the badge background (SurfaceBase since
// desktop-app v0.11, and SurfaceRaised per ui-kit v0.5) in both modes, and
// every name resolves to a distinct color.
func TestFileIconColorsMeetContrastLightAndDark(t *testing.T) {
	t.Parallel()
	for _, m := range bothModes() {
		ch := m.theme.Chrome()
		seen := make(map[theme.FileColorName]bool)
		for _, name := range theme.FileColorNames() {
			if seen[name] {
				t.Fatalf("%s: duplicate file-icon color name %q", m.name, name)
			}
			seen[name] = true
			c := m.theme.FileColor(name)
			if ratio := contrastRatio(c, ch.SurfaceBase); ratio < 3.0 {
				t.Errorf("%s file icon %s contrasts %.2f:1 on SurfaceBase, want at least 3:1", m.name, name, ratio)
			}
			if ratio := contrastRatio(c, ch.SurfaceRaised); ratio < 3.0 {
				t.Errorf("%s file icon %s contrasts %.2f:1 on SurfaceRaised, want at least 3:1", m.name, name, ratio)
			}
		}
	}
}
