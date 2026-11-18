package appwin

import (
	"runtime"
	"testing"

	"gioui.org/app"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestAppWindowSetsPlatformAndInvalidate pins the F10.13 env wiring: the
// real window's Env names the platform's keyboard notation from the OS it
// runs on (macOS shows ⌃⌥⇧⌘ in Kbd) and carries the window's Invalidate, so
// TreeView's async loads and the future async kit pieces can ask for a frame
// from any goroutine, as kitgallery's window does.
func TestAppWindowSetsPlatformAndInvalidate(t *testing.T) {
	t.Parallel()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build the text shaper: %v", err)
	}
	light := theme.Light(sh)
	env := newEnv(sh, &light)

	switch runtime.GOOS {
	case "darwin":
		if env.Platform != kit.PlatformMac {
			t.Fatalf("platform = %d on darwin, want PlatformMac", env.Platform)
		}
	default:
		if env.Platform != 0 {
			t.Fatalf("platform = %d on %s, want the default notation", env.Platform, runtime.GOOS)
		}
	}
	if env.Th != &light {
		t.Fatal("the window does not start in light mode")
	}

	// Only the window can grant invalidation; attach wires it in.
	w := new(app.Window)
	attach(env, w)
	if env.Invalidate == nil {
		t.Fatal("the env carries no Invalidate after attach")
	}
}
