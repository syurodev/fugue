//go:build regenerate

package appwin

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestRegenerateTeamBoardShots(t *testing.T) {
	t.Parallel()
	for _, spec := range teamShots() {
		img := renderTeamShot(t, spec)
		f, err := os.Create(filepath.Join("testdata", "shots", spec.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Error(err)
		}
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}
}
