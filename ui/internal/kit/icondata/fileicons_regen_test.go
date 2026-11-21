//go:build regenerate

package icondata

import (
	"os"
	"testing"
)

// TestRegenerateFileIconData rewrites fileicons.go from the vendored Symbols
// data. It exists under the regenerate build tag so an ordinary `go test`
// never writes source files:
//
//	go test -tags regenerate ./internal/kit/icondata -run TestRegenerateFileIconData
func TestRegenerateFileIconData(t *testing.T) {
	const path = "fileicons.go"
	if err := os.WriteFile(path, []byte(composeFileIcons(t)), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
