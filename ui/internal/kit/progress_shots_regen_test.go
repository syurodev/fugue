//go:build regenerate

package kit

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/ui/internal/glwin"
)

// TestRegenerateProgressShots writes the F10.22 Progress golden shots (light
// and dark at 90%, 100%, 125%) under testdata/shots:
//
//	go test -race -tags regenerate ./internal/kit -run TestRegenerateProgressShots
func TestRegenerateProgressShots(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(320, 140)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	outDir := filepath.Join("testdata", "shots")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		t.Fatalf("create shots dir: %v", err)
	}
	for _, spec := range progressShotSpecs() {
		img := renderProgressShot(t, win, spec)
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
