package appwin

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func TestRevisionHistoryRestoreCallsWithRowRevision(t *testing.T) {
	t.Parallel()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("shaper: %v", err)
	}
	th := theme.Dark(sh)
	env := &kit.Env{Th: &th, Sh: sh}
	var router input.Router
	var ops op.Ops

	rh := NewRevisionHistoryState()
	rows := []RevisionRow{
		{Revision: 3, Time: time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC), Author: "user", Operation: "update", Summary: "Latest", Current: true, CanRestore: false},
		{Revision: 1, Time: time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC), Author: "agent", Operation: "add", Summary: "Initial", Current: false, CanRestore: true},
	}

	step := func() (uint64, bool) {
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(500, 300)),
			Source:      router.Source(),
			Now:         time.Unix(1_700_000_000, 0),
		}
		env.Begin(gtx.Now)
		rev, ok, _ := rh.Layout(env, &gtx, rows, false, func(err error) {
			if err != nil {
				t.Fatalf("layout err: %v", err)
			}
		})
		if tag := rh.RestoreTag(1); tag != nil {
			gtx.Execute(key.FocusCmd{Tag: tag})
		}
		env.FrameEnd(&gtx)
		router.Frame(&ops)
		return rev, ok
	}

	// Frame 1: lay out and focus row 1's Restore button.
	if _, ok := step(); ok {
		t.Fatal("unexpected restore click on first frame")
	}
	// Frame 2: press + release Return to activate row 1's Restore button.
	router.Queue(
		key.Event{Name: key.NameReturn, State: key.Press},
		key.Event{Name: key.NameReturn, State: key.Release},
	)
	rev, ok := step()
	if !ok || rev != 1 {
		t.Fatalf("restore = (%d, %v), want (1, true)", rev, ok)
	}
}
