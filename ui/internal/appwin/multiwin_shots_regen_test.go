//go:build regenerate

package appwin

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestRegenerateMultiWindowShots writes the three F10.21 evidence PNGs:
//
//	go run ./cmd/fuguegate exclusive -- go -C ui test -race -shuffle=on -tags regenerate ./internal/appwin -run '^TestRegenerateMultiWindowShots$'
func TestRegenerateMultiWindowShots(t *testing.T) {
	t.Parallel()
	for _, shot := range renderMultiWindowShots(t) {
		writeMultiWindowShot(t, shot)
	}
}

func writeMultiWindowShot(t *testing.T, shot multiWindowShot) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, shot.img); err != nil {
		t.Fatalf("encode %s: %v", shot.name, err)
	}
	path := filepath.Join("testdata", "shots", shot.name+".png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
