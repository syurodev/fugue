package appwin

import (
	"fmt"
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// policyRows lays the Q-64 card's body out at width w and scale and returns
// the rows' boxes plus the body size the dialog scrolls over.
func policyRows(t *testing.T, keys int, scale float32) ([]image.Rectangle, image.Point) {
	t.Helper()
	sc, _ := newTestScreens(t)
	env := newInboxTestEnv(t)
	env.UIScale = scale
	diff := make([]workspace.DiffEntry, 0, keys)
	for i := range keys {
		diff = append(diff, workspace.DiffEntry{Key: fmt.Sprintf("policy.approvals.key_%d", i), OldValue: "false", NewValue: "true"})
	}
	det := wsTestDetail(diff)
	sc.dlg.policy.openFor(det)

	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(420, 1<<20)),
	}
	var bands kit.DialogBands
	body := newWSBody(env, &gtx, &bands, sc.fail)
	sc.dlg.policy.fill(env, body, sc)
	if sc.lastErr != "" {
		t.Fatalf("layout failed: %s", sc.lastErr)
	}
	return body.rows, body.size()
}

// assertRowsStack fails unless every row is non-empty, inside bounds and
// clear of the rows drawn before it.
func assertRowsStack(t *testing.T, rows []image.Rectangle, bounds image.Rectangle) {
	t.Helper()
	for i, r := range rows {
		if r.Empty() {
			t.Fatalf("row %d is empty: %v", i, r)
		}
		if !r.In(bounds) {
			t.Fatalf("row %d %v overflows the body %v", i, r, bounds)
		}
		for j := range i {
			if r.Overlaps(rows[j]) {
				t.Fatalf("row %d %v overlaps row %d %v", i, r, j, rows[j])
			}
		}
	}
}

// TestPolicyCardRowsDoNotOverlap pins FUG-33: every diff row and the Reject
// button sit in their own box, in order, inside the body, for one key and for
// enough keys that the dialog scrolls.
func TestPolicyCardRowsDoNotOverlap(t *testing.T) {
	t.Parallel()
	for _, keys := range []int{1, 3, 60} {
		for _, scale := range []float32{1, 1.25} {
			t.Run(fmt.Sprintf("keys%d-ui%.0f", keys, scale*100), func(t *testing.T) {
				t.Parallel()
				rows, size := policyRows(t, keys, scale)
				if len(rows) != keys+1 {
					t.Fatalf("rows = %d, want %d diff rows plus the reject button", len(rows), keys)
				}
				assertRowsStack(t, rows, image.Rectangle{Max: size})
				if last := rows[keys-1]; last.Max.Y > rows[keys].Min.Y {
					t.Fatalf("last diff row %v is not above the reject button %v", last, rows[keys])
				}
			})
		}
	}
}
