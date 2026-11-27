//go:build regenerate

package appwin

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestRegenerateTitleBarShots writes the FUG-58/FUG-97 title bar goldens:
//
//	go test -race -tags regenerate ./internal/appwin -run '^TestRegenerateTitleBarShots$'
func TestRegenerateTitleBarShots(t *testing.T) {
	t.Parallel()
	writeTitleBarShots(t, renderTitleBarShots(t))
}

// TestRegenerateMacTitleStripShots writes the FUG-70 macOS title strip goldens:
//
//	go test -race -tags regenerate ./internal/appwin -run '^TestRegenerateMacTitleStripShots$'
func TestRegenerateMacTitleStripShots(t *testing.T) {
	t.Parallel()
	writeTitleBarShots(t, renderMacTitleStripShots(t))
}

func writeTitleBarShots(t *testing.T, shots []titleBarShot) {
	t.Helper()
	for _, shot := range shots {
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
