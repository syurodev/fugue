//go:build regenerate

package appwin

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/ui/internal/glwin"
)

func TestRegenerateProposalCardShot(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(proposalShotWidth, proposalShotHeight)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join("testdata", "shots", proposalShotName+".png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, renderProposalShot(t, win)); err != nil {
		t.Error(err)
	}
	if err := f.Close(); err != nil {
		t.Error(err)
	}
}
