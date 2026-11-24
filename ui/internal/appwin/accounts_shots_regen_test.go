//go:build regenerate

package appwin

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/ui/internal/glwin"
)

// TestRegenerateAccountsShots writes the F10.5c accounts and quota screenshots
// (light/dark) under testdata/shots:
//
//	go test -race -tags regenerate ./internal/appwin -run TestRegenerateAccountsShots
func TestRegenerateAccountsShots(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	outDir := filepath.Join("testdata", "shots")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		t.Fatalf("create shots dir: %v", err)
	}
	for _, spec := range accountsShotSpecs() {
		img := renderAccountsShot(t, win, spec)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("encode %s: %v", spec.name, err)
		}
		path := filepath.Join(outDir, spec.name+".png")
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for _, spec := range claudeLoginCodeShotSpecs() {
		env := newAccountsTestEnv(t, spec.light, spec.uiScale)
		sc := buildClaudeLoginCodeScreens(t, spec.scene)
		img := renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), spec.name)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("encode %s: %v", spec.name, err)
		}
		path := filepath.Join(outDir, spec.name+".png")
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}
