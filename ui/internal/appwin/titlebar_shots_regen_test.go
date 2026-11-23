//go:build regenerate

package appwin

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestRegenerateTitleBarShots writes the FUG-58 title bar goldens:
//
//	go test -race -tags regenerate ./internal/appwin -run '^TestRegenerateTitleBarShots$'
func TestRegenerateTitleBarShots(t *testing.T) {
	t.Parallel()
	for _, shot := range renderTitleBarShots(t) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, shot.img); err != nil {
			t.Fatalf("encode %s: %v", shot.name, err)
		}
		path := filepath.Join("testdata", "shots", shot.name+".png")
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}
