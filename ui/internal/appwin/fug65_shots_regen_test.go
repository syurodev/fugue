//go:build regenerate

package appwin

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestRegenerateFUG65Shots writes the FUG-65 visual pass goldens under
// testdata/shots:
//
//	go test -race -tags regenerate ./internal/appwin -run TestRegenerateFUG65Shots
func TestRegenerateFUG65Shots(t *testing.T) {
	t.Parallel()
	outDir := filepath.Join("testdata", "shots")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		t.Fatalf("create shots dir: %v", err)
	}
	for _, spec := range fug65ShotSpecs() {
		img := renderFUG65Shot(t, spec)
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
