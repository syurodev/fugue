package appwin

import (
	"fmt"
	"image"
	"testing"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

const (
	testFalse = "false"
	testTrue  = "true"
)

// policyTestDiff is a diff of keys boolean flips.
func policyTestDiff(keys int) []workspace.DiffEntry {
	diff := make([]workspace.DiffEntry, 0, keys)
	for i := range keys {
		diff = append(diff, workspace.DiffEntry{Key: fmt.Sprintf("policy.approvals.key_%d", i), OldValue: testFalse, NewValue: testTrue})
	}
	return diff
}

// policyRows lays the Q-64 card's body out at width w and scale and returns
// the rows' boxes plus the body size the dialog scrolls over.
func policyRows(t *testing.T, keys int, scale float32) ([]image.Rectangle, image.Point) {
	t.Helper()
	sc, _ := newTestScreens(t)
	env := newInboxTestEnv(t)
	env.UIScale = scale
	det := wsTestDetail(policyTestDiff(keys))
	sc.dlg.policy.openFor(det)

	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(420, 1<<20)),
	}
	var bands kit.DialogBands
	body := newWSBody(env, &gtx, &bands, sc.fail)
	sc.dlg.policy.fill(env, body)
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

// TestPolicyCardRowsDoNotOverlap pins FUG-33 and FUG-35: the diff is one
// panel row inside the body, for one key and for enough keys that the dialog
// scrolls, and the Reject action is not among the body rows at all.
func TestPolicyCardRowsDoNotOverlap(t *testing.T) {
	t.Parallel()
	for _, keys := range []int{1, 3, 60} {
		for _, scale := range []float32{1, 1.25} {
			t.Run(fmt.Sprintf("keys%d-ui%.0f", keys, scale*100), func(t *testing.T) {
				t.Parallel()
				rows, size := policyRows(t, keys, scale)
				if len(rows) != 1 {
					t.Fatalf("rows = %d, want the one diff panel", len(rows))
				}
				assertRowsStack(t, rows, image.Rectangle{Max: size})
				if rows[0].Dy() < keys*10 {
					t.Fatalf("panel %v is too short for %d keys", rows[0], keys)
				}
			})
		}
	}
}

// policyFrame lays the whole card out as an inline specimen in a window and
// returns the boxes the frame drew.
func policyFrame(t *testing.T, keys int, scale float32) kit.DialogBoxes {
	t.Helper()
	sc, _ := newTestScreens(t)
	env := newInboxTestEnv(t)
	env.UIScale = scale
	d := &sc.dlg.policy
	d.openFor(wsTestDetail(policyTestDiff(keys)))

	var ops op.Ops
	router := new(input.Router)
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(860, 560)),
		Source:      router.Source(),
	}
	env.TrackInput(&gtx)
	props := d.props(env, sc)
	props.Inline = true
	res, err := d.dialog.Layout(env, &gtx, props)
	if err != nil {
		t.Fatalf("lay the card out: %v", err)
	}
	if sc.lastErr != "" {
		t.Fatalf("layout failed: %s", sc.lastErr)
	}
	return res.Boxes
}

// assertFooterRow fails unless the three footer buttons are non-empty,
// inside the frame, below the body, on one row, in order, with Reject on the
// body's left edge.
func assertFooterRow(t *testing.T, b *kit.DialogBoxes) {
	t.Helper()
	for name, r := range map[string]image.Rectangle{"reject": b.Extra, "cancel": b.Cancel, "confirm": b.Primary} {
		if r.Empty() || !r.In(b.Frame) {
			t.Fatalf("%s box %v is empty or outside the frame %v", name, r, b.Frame)
		}
		if r.Min.Y < b.Body.Max.Y {
			t.Fatalf("%s box %v intersects the body %v", name, r, b.Body)
		}
	}
	if b.Extra.Min.Y != b.Cancel.Min.Y || b.Cancel.Min.Y != b.Primary.Min.Y {
		t.Fatalf("footer buttons are not on one row: %v %v %v", b.Extra, b.Cancel, b.Primary)
	}
	if b.Extra.Max.X > b.Cancel.Min.X || b.Cancel.Max.X > b.Primary.Min.X {
		t.Fatalf("footer buttons overlap or are out of order: %v %v %v", b.Extra, b.Cancel, b.Primary)
	}
	if b.Extra.Min.X != b.Body.Min.X {
		t.Fatalf("reject %v is not on the left content edge %v", b.Extra, b.Body)
	}
}

// TestPolicyRejectSitsInTheFooter pins FUG-35: Reject and restore is the
// footer's left action, on the row of Cancel and Confirm, clear of both and
// of the body that holds the diff panel, at 100% and 125%.
func TestPolicyRejectSitsInTheFooter(t *testing.T) {
	t.Parallel()
	for _, keys := range []int{1, 60} {
		for _, scale := range []float32{1, 1.25} {
			t.Run(fmt.Sprintf("keys%d-ui%.0f", keys, scale*100), func(t *testing.T) {
				t.Parallel()
				b := policyFrame(t, keys, scale)
				assertFooterRow(t, &b)
			})
		}
	}
}
