package appwin

import (
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// shellPlanAt plans the fold for a shell shellDp wide at a 1:1 metric.
func shellPlanAt(sc *screens, shellDp int) PlanFoldResult {
	gtx := layout.Context{
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(shellDp, 600)),
	}
	return sc.planShell(&gtx)
}

// TestShellFoldUsesTheShellWidth is the FUG-32 regression: the fold counted
// the rail and sidebar twice, so the default 900 dp window (a 876 dp shell
// after drawWindow's insets) always folded the sidebar.
func TestShellFoldUsesTheShellWidth(t *testing.T) {
	t.Parallel()
	oneColMin := int(theme.RailWidth) + int(theme.RightStripeWidth) + int(theme.SidebarWidth) + int(theme.IslandGap) + int(theme.MainSlotMinWidth)
	twoColMin := oneColMin + int(theme.SlotMinWidth) + int(theme.IslandGap)
	cases := []struct {
		name         string
		cols         int
		shellDp      int
		wantFolded   int
		wantSidebarF bool
	}{
		{"default 900 dp window keeps the sidebar", 1, 876, 0, false},
		{"exact minimum keeps the sidebar", 1, oneColMin, 0, false},
		{"one dp short folds the sidebar", 1, oneColMin - 1, 0, true},
		{"two columns fit exactly", 2, twoColMin, 0, false},
		{"one dp short folds the regular column", 2, twoColMin - 1, 1, false},
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
			got := shellPlanAt(sc, tc.shellDp)
			if got.FoldedCols != tc.wantFolded || got.TempSidebarFolded != tc.wantSidebarF {
				t.Fatalf("plan at shell %d dp with %d columns = %+v, want folded=%d sidebar=%v",
					tc.shellDp, tc.cols, got, tc.wantFolded, tc.wantSidebarF)
			}
		})
	}
}
