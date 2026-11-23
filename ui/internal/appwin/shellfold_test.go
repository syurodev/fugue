package appwin

import (
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// shellPlanAt plans the fold the way drawWindow hands the shell its room:
// the window minus the side insets, at a 1:1 metric.
func shellPlanAt(sc *screens, windowDp int) PlanFoldResult {
	inset := 2 * int(theme.SpaceLG)
	gtx := layout.Context{
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(windowDp-inset, 600)),
	}
	return sc.planShell(&gtx)
}

// TestShellFoldUsesTheFullWindowWidth is the FUG-32 regression: the fold
// counted the rail and sidebar twice, so the default 900 dp window always
// folded the sidebar.
func TestShellFoldUsesTheFullWindowWidth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		cols         int
		windowDp     int
		wantFolded   int
		wantSidebarF bool
	}{
		{"default window keeps the sidebar", 1, 900, 0, false},
		{"exact minimum keeps the sidebar", 1, 808, 0, false},
		{"one dp short folds the sidebar", 1, 807, 0, true},
		{"two columns fit exactly", 2, 1108, 0, false},
		{"one dp short folds the regular column", 2, 1107, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc, _ := newTestScreens(t)
			sc.grid.doc = kindsFixture()
			if tc.cols == 2 {
				sc.grid.doc = kindsFixture(KindInbox)
			}
			sc.grid.doc.SidebarCollapsed = false
			got := shellPlanAt(sc, tc.windowDp)
			if got.FoldedCols != tc.wantFolded || got.TempSidebarFolded != tc.wantSidebarF {
				t.Fatalf("plan at %d dp with %d columns = %+v, want folded=%d sidebar=%v",
					tc.windowDp, tc.cols, got, tc.wantFolded, tc.wantSidebarF)
			}
		})
	}
}
