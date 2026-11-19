//go:build fugue_regen

package teams

// Regenerates testdata/digest.golden after a reviewed format change (Q-124):
// run `task test-pkg PKG=./internal/teams/... RUN=TestRegenerateDigestGolden`
// with `-tags fugue_regen`. The normal build never writes the golden file.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegenerateDigestGolden(t *testing.T) {
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "digest.golden"), []byte(goldenDigest()), 0o644); err != nil {
		t.Fatal(err)
	}
}
