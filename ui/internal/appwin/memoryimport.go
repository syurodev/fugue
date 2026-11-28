package appwin

import (
	"fmt"
	"slices"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/memory"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type memoryImportRowState struct {
	itemID    string
	class     string
	choice    string
	classTag  *kit.TagState
	cmp       *kit.CompareViewState
	seg       *kit.SegmentedState
	renameInp *kit.TextFieldState
}

func newMemoryImportRowState(item *client.MemoryImportPreviewItem) *memoryImportRowState {
	choice := item.DefaultChoice
	if item.Class == client.MemoryImportClassConflict && choice == "" {
		choice = client.MemoryImportChoiceKeepLocal
	}
	if item.Class == client.MemoryImportClassLabelConflict && choice == "" {
		choice = client.MemoryImportChoiceSkip
	}
	inp := kit.NewTextFieldState()
	if item.Label != "" {
		inp.SetText(item.Label + " (imported)")
	}
	return &memoryImportRowState{
		itemID:    item.ItemID,
		class:     item.Class,
		choice:    choice,
		classTag:  kit.NewTagState(),
		cmp:       kit.NewCompareViewState(),
		seg:       kit.NewSegmentedState(),
		renameInp: inp,
	}
}

func (v *memoryView) loadPreviewForTest(prev *client.MemoryImportPreview, raw []byte, fileName string) {
	v.loadPreview(prev, raw, fileName)
}

func (v *memoryView) loadPreview(prev *client.MemoryImportPreview, raw []byte, fileName string) {
	v.importPreview = prev
	v.importBytes = append([]byte(nil), raw...)
	v.importFile = fileName
	v.importStale = false
	v.importErr = ""
	v.importResult = nil
	v.importRows = make([]*memoryImportRowState, 0, len(prev.Items))
	for i := range prev.Items {
		v.importRows = append(v.importRows, newMemoryImportRowState(&prev.Items[i]))
	}
	v.mode = memModeImport
}

func (v *memoryView) setImportChoice(itemID, choice, newLabel string) {
	for _, r := range v.importRows {
		if r.itemID == itemID {
			r.choice = choice
			if newLabel != "" {
				r.renameInp.SetText(newLabel)
			}
			return
		}
	}
}

func (v *memoryView) applyImportDiff(sc *screens) {
	if sc.memoryWorker == nil || v.importPreview == nil || v.importBusy {
		return
	}
	v.importBusy = true
	v.importErr = ""
	choices := make([]client.MemoryImportChoiceInput, 0, len(v.importRows))
	for _, r := range v.importRows {
		if r.class == client.MemoryImportClassRejected {
			continue
		}
		ch := client.MemoryImportChoiceInput{ItemID: r.itemID, Choice: r.choice}
		if r.class == client.MemoryImportClassLabelConflict && r.choice == client.MemoryImportChoiceRename {
			ch.NewLabel = strings.TrimSpace(r.renameInp.Text())
		}
		choices = append(choices, ch)
	}
	sc.memoryWorker.Do(&memory.Request{
		Kind:        memory.OpApplyImport,
		WorkspaceID: v.wsID,
		Apply: client.ApplyMemoryImportInput{
			WorkspaceID: v.wsID,
			PreviewID:   v.importPreview.PreviewID,
			Choices:     choices,
			ImportFile:  v.importFile,
		},
	})
}

func (v *memoryView) repreviewImport(sc *screens) {
	if sc.memoryWorker == nil || len(v.importBytes) == 0 || v.importBusy {
		return
	}
	v.importBusy = true
	v.importErr = ""
	sc.memoryWorker.Do(&memory.Request{
		Kind:        memory.OpPreviewImportBytes,
		WorkspaceID: v.wsID,
		FilePath:    v.importFile,
		ImportBytes: v.importBytes,
	})
}

func (v *memoryView) applyImportResult(sc *screens, res *memory.Result) {
	v.importBusy = false
	if res.Err != nil {
		v.importErr = hostErrorMessage(res.Err)
		if code, ok := client.MemoryCodeOf(res.Err); ok && code == client.MemoryCodeImportStale {
			v.importStale = true
		}
		return
	}
	v.importErr = ""
	v.importStale = false
	r := res.ApplyResult
	v.importResult = &r
	v.requestQuery(sc)
}

func (v *memoryView) layoutImportDiff(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	c := env.Th.Chrome()
	return v.importScroll.Layout(env, gtx, kit.ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
		children := make([]layout.FlexChild, 0, 16)
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutImportHeader(gtx, env, sc)
		}), spacerH(theme.SpaceMD))

		if v.importErr != "" {
			kind := kit.BannerDanger
			title := "Import failed"
			act := ""
			if v.importStale {
				kind = kit.BannerWarning
				title = "Import preview is out of date"
				act = "Preview again"
			}
			children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				d, ev, err := v.errBanner.Layout(env, gtx, kit.BannerProps{
					Kind: kind, Title: title, Message: v.importErr, ActionLabel: act, Visible: true,
				})
				sc.fail(err)
				if ev.ActionClicked && v.importStale {
					v.repreviewImport(sc)
				}
				return d
			}), spacerH(theme.SpaceSM))
		}

		if v.importResult != nil {
			r := v.importResult
			children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				d, _, err := v.infoBanner.Layout(env, gtx, kit.BannerProps{
					Kind:    kit.BannerInfo,
					Title:   "Import applied",
					Message: fmt.Sprintf("Added %d, updated %d, skipped %d.", r.Added, r.Updated, r.Skipped),
					Visible: true,
				})
				sc.fail(err)
				return d
			}), spacerH(theme.SpaceSM))
		}

		if v.importPreview == nil {
			children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return kit.Label(env, gtx, theme.TypeBody(), c.FgMuted, "No import preview loaded.")
			}))
			return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
		}

		for i := range v.importPreview.Items {
			item := &v.importPreview.Items[i]
			row := v.importRows[i]
			children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return v.layoutImportItem(gtx, env, sc, item, row)
			}), spacerH(theme.SpaceMD))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
	})
}

func (v *memoryView) layoutImportHeader(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	canApply := !v.importBusy && v.importPreview != nil && !v.importStale && v.importResult == nil
	items := []kit.FlowItem{
		{
			Size: v.importBack.Measure(env, gtx, kit.ButtonProps{Text: "Back", Icon: kit.IconArrowLeft, Variant: kit.Ghost, Size: kit.SizeSM}).Size,
			Draw: func(g *layout.Context) {
				if clicked, err := v.importBack.Layout(env, g, kit.ButtonProps{Text: "Back", Icon: kit.IconArrowLeft, Variant: kit.Ghost, Size: kit.SizeSM}); err != nil {
					sc.fail(err)
				} else if clicked {
					v.mode = memModeList
				}
			},
		},
		{
			Size: v.importApply.Measure(env, gtx, kit.ButtonProps{Text: "Apply import", Variant: kit.Primary, Size: kit.SizeSM, Busy: v.importBusy, Disabled: !canApply}).Size,
			Draw: func(g *layout.Context) {
				if clicked, err := v.importApply.Layout(env, g, kit.ButtonProps{Text: "Apply import", Variant: kit.Primary, Size: kit.SizeSM, Busy: v.importBusy, Disabled: !canApply}); err != nil {
					sc.fail(err)
				} else if clicked {
					v.applyImportDiff(sc)
				}
			},
		},
		{
			Size: v.importCancel.Measure(env, gtx, kit.ButtonProps{Text: "Cancel", Variant: kit.Secondary, Size: kit.SizeSM, Disabled: v.importBusy}).Size,
			Draw: func(g *layout.Context) {
				if clicked, err := v.importCancel.Layout(env, g, kit.ButtonProps{Text: "Cancel", Variant: kit.Secondary, Size: kit.SizeSM, Disabled: v.importBusy}); err != nil {
					sc.fail(err)
				} else if clicked {
					v.mode = memModeList
				}
			},
		},
	}
	return kit.FlowRow(gtx, gtx.Dp(theme.SpaceSM), gtx.Dp(theme.SpaceXS), items)
}

func (v *memoryView) layoutImportItem(gtx *layout.Context, env *kit.Env, sc *screens, item *client.MemoryImportPreviewItem, row *memoryImportRowState) layout.Dimensions {
	c := env.Th.Chrome()
	tagProps := importClassTagProps(item.Class)
	children := make([]layout.FlexChild, 0, 6)
	children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return kit.Label(env, &gtx, theme.TypeLabel(), c.Fg, item.Label)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, err := row.classTag.Layout(env, &gtx, tagProps)
				sc.fail(err)
				return d
			}),
		)
	}))

	switch item.Class {
	case client.MemoryImportClassRejected:
		children = append(children, spacerH(theme.SpaceXS), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return kit.Paragraph(env, gtx, theme.TypeSmall(), env.Th.Status().Danger, item.RejectReason)
		}))
	case client.MemoryImportClassConflict:
		rows := buildImportCompareRows(item.Local, item.Imported)
		children = append(children, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			d, err := row.cmp.Layout(env, gtx, kit.CompareViewProps{
				LeftTitle: "On this machine", RightTitle: "In the file", Rows: rows,
			})
			sc.fail(err)
			return d
		}), spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			opts := []kit.SegmentedOption{
				{ID: client.MemoryImportChoiceKeepLocal, Label: "Keep local"},
				{ID: client.MemoryImportChoiceTakeImported, Label: "Take imported"},
				{ID: client.MemoryImportChoiceKeepBoth, Label: "Keep both"},
			}
			sel := indexOfChoice(opts, row.choice)
			pr := kit.SegmentedProps{Options: opts, Selected: sel, Disabled: v.importBusy}
			changed, next, err := row.seg.Layout(env, gtx, pr)
			sc.fail(err)
			if changed && next >= 0 && next < len(opts) {
				row.choice = opts[next].ID
			}
			return row.seg.Measure(env, gtx, pr)
		}))
	case client.MemoryImportClassLabelConflict:
		children = append(children, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			opts := []kit.SegmentedOption{
				{ID: client.MemoryImportChoiceSkip, Label: "Skip"},
				{ID: client.MemoryImportChoiceRename, Label: "Rename"},
			}
			sel := indexOfChoice(opts, row.choice)
			pr := kit.SegmentedProps{Options: opts, Selected: sel, Disabled: v.importBusy}
			changed, next, err := row.seg.Layout(env, gtx, pr)
			sc.fail(err)
			if changed && next >= 0 && next < len(opts) {
				row.choice = opts[next].ID
			}
			return row.seg.Measure(env, gtx, pr)
		}))
		if row.choice == client.MemoryImportChoiceRename {
			children = append(children, spacerH(theme.SpaceXS), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				pr := &kit.TextFieldProps{Label: "New label", Placeholder: "unique-label", Disabled: v.importBusy}
				_, err := row.renameInp.Layout(env, gtx, pr)
				sc.fail(err)
				return row.renameInp.Measure(env, gtx, pr)
			}))
		}
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func importClassTagProps(class string) kit.TagProps {
	switch class {
	case client.MemoryImportClassAdd:
		return kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusSuccess, Text: "add"}
	case client.MemoryImportClassSkip:
		return kit.TagProps{Variant: kit.TagNeutral, Text: "skip"}
	case client.MemoryImportClassConflict:
		return kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusWarning, Text: "conflict"}
	case client.MemoryImportClassLabelConflict:
		return kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusWarning, Text: "label conflict"}
	case client.MemoryImportClassRejected:
		return kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusDanger, Text: "rejected"}
	default:
		return kit.TagProps{Variant: kit.TagNeutral, Text: class}
	}
}

func buildImportCompareRows(local, imported *client.MemoryEntry) []kit.CompareRow {
	var l, r client.MemoryEntry
	if local != nil {
		l = *local
	}
	if imported != nil {
		r = *imported
	}
	return []kit.CompareRow{
		{Name: "Kind", Left: l.Kind, Right: r.Kind, Changed: l.Kind != r.Kind},
		{Name: "Summary", Left: l.Summary, Right: r.Summary, Changed: l.Summary != r.Summary},
		{Name: "Body", Left: l.Body, Right: r.Body, Changed: l.Body != r.Body},
		{Name: "Keywords", Left: strings.Join(l.Keywords, ", "), Right: strings.Join(r.Keywords, ", "), Changed: !slices.Equal(l.Keywords, r.Keywords)},
	}
}

func indexOfChoice(opts []kit.SegmentedOption, id string) int {
	for i, o := range opts {
		if o.ID == id {
			return i
		}
	}
	return 0
}
