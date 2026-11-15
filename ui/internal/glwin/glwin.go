package glwin

import (
	"fmt"

	"gioui.org/gpu/headless"
)

// Open returns a new headless window of size w×h. The caller keeps it for
// the life of the process and never calls Release on it, see the package
// comment; a window is cheap to keep, and a test that needs one for many
// frames opens it once and reuses it.
func Open(w, h int) (*headless.Window, error) {
	win, err := headless.NewWindow(w, h)
	if err != nil {
		return nil, fmt.Errorf("open a %d×%d headless window: %w", w, h, err)
	}
	return win, nil
}
