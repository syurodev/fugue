//go:build regenerate

package icondata

import (
	"os"
	"testing"
)

// TestRegenerateIconData rewrites icondata.go from the Lucide data in
// testdata. It exists under the regenerate build tag so an ordinary
// `go test` never writes source files:
//
//	go test -tags regenerate ./internal/kit/icondata -run TestRegenerateIconData
func TestRegenerateIconData(t *testing.T) {
	const path = "icondata.go"
	if err := os.WriteFile(path, []byte(composeFile(t)), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
