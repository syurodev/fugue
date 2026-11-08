package appwin

import (
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/ui/internal/hostconn"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestClampDp pins the window-size clamp against the F10.1 finding: 900x1000
// dp overflowed an 864 dp-high 1080p screen at scale 1.25, so the height
// clamps to the pixel budget floor-divided through the scale.
func TestClampDp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		w, h               int
		scale, screenW, h2 float64
		wantW, wantH       int
	}{
		{"the F10.1 overflow", 900, 1000, 1.25, 1920, 1080, 900, 864},
		{"fits already", 900, 800, 1.25, 1920, 1080, 900, 800},
		{"width clamps too", 2000, 400, 1, 1920, 1080, 1920, 400},
		{"retina scale", 900, 1000, 2, 2880, 1800, 900, 900},
		{"tiny screen keeps the minimum", 900, 1000, 1, 200, 200, 360, 240},
		{"unknown scale leaves the request", 900, 1000, 0, 1920, 1080, 900, 1000},
		{"unknown work area leaves the request", 900, 1000, 1.25, 0, 0, 900, 1000},
		{"fractional budgets floor down", 900, 900, 1.5, 2000, 1000, 900, 666},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotW, gotH := ClampDp(tc.w, tc.h, tc.scale, tc.screenW, tc.h2)
			if gotW != tc.wantW || gotH != tc.wantH {
				t.Errorf("ClampDp(%d, %d, %v, %v, %v) = %dx%d, want %dx%d",
					tc.w, tc.h, tc.scale, tc.screenW, tc.h2, gotW, gotH, tc.wantW, tc.wantH)
			}
		})
	}
}

// TestStatusLine pins the status bar text: the reason appears in
// parentheses, and the plain state stands alone.
func TestStatusLine(t *testing.T) {
	t.Parallel()
	if got := statusLine(hostconn.StateConnected, ""); got != "connection: connected" {
		t.Errorf("statusLine(connected, %q) = %q", "", got)
	}
	if got := statusLine(hostconn.StateDisconnected, "host not running"); got != "connection: disconnected (host not running)" {
		t.Errorf("statusLine(disconnected, host not running) = %q", got)
	}
}
