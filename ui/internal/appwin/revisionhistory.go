package appwin

import (
	"fmt"
	"image"
	"time"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// RevisionRow describes one immutable revision entry rendered by RevisionHistory.
// It is domain-free so workspace memory and future history-backed owners can share it.
type RevisionRow struct {
	Revision   uint64
	Time       time.Time
	Author     string
	Operation  string
	Summary    string
	Current    bool
	CanRestore bool
}

// RevisionHistoryState holds per-row widget states for a RevisionHistory list.
type RevisionHistoryState struct {
	rows        []*kit.ListRowState
	restoreBtns []*kit.ButtonState
	currentTags []*kit.TagState
}

// NewRevisionHistoryState builds a RevisionHistoryState.
func NewRevisionHistoryState() *RevisionHistoryState {
	return &RevisionHistoryState{}
}

func (s *RevisionHistoryState) ensure(n int) {
	for len(s.rows) < n {
		s.rows = append(s.rows, kit.NewListRowState())
		s.restoreBtns = append(s.restoreBtns, kit.NewButtonState())
		s.currentTags = append(s.currentTags, kit.NewTagState())
	}
}

// RestoreTag returns the i-th row's restore ButtonState tag for tests.
func (s *RevisionHistoryState) RestoreTag(i int) any {
	if i < 0 || i >= len(s.restoreBtns) {
		return nil
	}
	return s.restoreBtns[i].Tag()
}

// Layout draws the revision list and returns the revision selected for restore,
// if a row's Restore button was clicked this frame.
func (s *RevisionHistoryState) Layout(
	env *kit.Env,
	gtx *layout.Context,
	rows []RevisionRow,
	busy bool,
	onErr func(error),
) (restoreRev uint64, ok bool, dims layout.Dimensions) {
	if len(rows) == 0 {
		return 0, false, layout.Dimensions{}
	}
	s.ensure(len(rows))
	children := make([]layout.FlexChild, 0, len(rows)*2)
	for i := range rows {
		r := rows[i]
		if i > 0 {
			children = append(children, spacerH(theme.SpaceXS))
		}
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return s.layoutRow(env, gtx, i, &r, busy, onErr, &restoreRev, &ok)
		}))
	}
	d := layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
	return restoreRev, ok, d
}

func (s *RevisionHistoryState) layoutRow(
	env *kit.Env,
	gtx *layout.Context,
	idx int,
	row *RevisionRow,
	busy bool,
	onErr func(error),
	restoreRev *uint64,
	ok *bool,
) layout.Dimensions {
	c := env.Th.Chrome()
	title := fmt.Sprintf("r%d · %s · %s", row.Revision, row.Operation, row.Author)
	when := ""
	if !row.Time.IsZero() {
		when = row.Time.UTC().Format("2006-01-02 15:04 UTC")
	}

	btnProps := kit.ButtonProps{
		Text:     "Restore",
		Variant:  kit.Secondary,
		Size:     kit.SizeXS,
		Disabled: busy || row.Current || !row.CanRestore,
	}
	tagProps := kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusSuccess, Text: "current"}
	drawRight := func(gtx *layout.Context) layout.Dimensions {
		if row.Current {
			d, err := s.currentTags[idx].Layout(env, gtx, tagProps)
			if onErr != nil {
				onErr(err)
			}
			return d
		}
		if !row.CanRestore {
			return layout.Dimensions{}
		}
		clicked, err := s.restoreBtns[idx].Layout(env, gtx, btnProps)
		if onErr != nil {
			onErr(err)
		}
		if clicked && !*ok {
			*restoreRev = row.Revision
			*ok = true
		}
		return s.restoreBtns[idx].Measure(env, gtx, btnProps)
	}

	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			g := gtx
			children := []layout.FlexChild{
				rigidWidget(func(gtx *layout.Context) layout.Dimensions {
					return kit.Label(env, gtx, theme.TypeLabel(), c.Fg, title)
				}),
			}
			meta := when
			if row.Summary != "" {
				if meta != "" {
					meta += " — "
				}
				meta += row.Summary
			}
			if meta != "" {
				children = append(children, spacerH(theme.SpaceXXS), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
					return kit.Paragraph(env, gtx, theme.TypeSmall(), c.FgMuted, meta)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(g, children...)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceMD}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				local.Constraints.Min = image.Point{}
				return drawRight(&local)
			})
		}),
	)
}
