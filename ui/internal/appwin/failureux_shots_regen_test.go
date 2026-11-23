//go:build regenerate

package appwin

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/ui/internal/glwin"
)

// TestRegenerateFailureShots writes the A-E05 unproved-stop banner
// screenshots (light/dark) under testdata/shots:
//
//	go test -race -tags regenerate ./internal/appwin -run TestRegenerateFailureShots
func TestRegenerateFailureShots(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	outDir := filepath.Join("testdata", "shots")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		t.Fatalf("create shots dir: %v", err)
	}
	for _, spec := range failureShotSpecs() {
		img := renderFailureShot(t, win, spec)
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
