package appwin

import (
	"fmt"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/memory"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func (v *memoryView) setSelectedForTest(e *client.MemoryEntry) {
	v.selected = e
	v.mode = memModeDetail
}

func (v *memoryView) openEntry(sc *screens, id string) {
	for i := range v.entries {
		if v.entries[i].ID == id {
			cp := v.entries[i]
			v.selected = &cp
			break
		}
	}
	v.mode = memModeDetail
	v.detailErr = ""
	v.history = nil
	v.requestEntryDetail(sc, id)
}

func (v *memoryView) requestEntryDetail(sc *screens, id string) {
	if sc.memoryWorker == nil || v.wsID == "" || id == "" {
		return
	}
	v.detailLoading = true
	sc.memoryWorker.Do(&memory.Request{Kind: memory.OpGetEntry, WorkspaceID: v.wsID, EntryID: id})
	sc.memoryWorker.Do(&memory.Request{Kind: memory.OpGetEntryHistory, WorkspaceID: v.wsID, EntryID: id})
}

func (v *memoryView) startCreate() {
	v.creating = true
	v.selected = nil
	v.editKind = client.MemoryKindFact
	v.editLabel.SetText("")
	v.editSummary.SetText("")
	v.editKeywords.SetText("")
	v.editBody.SetText("")
	v.detailErr = ""
	v.mode = memModeEdit
}

func (v *memoryView) startEdit() {
	if v.selected == nil {
		return
	}
	v.creating = false
	v.editKind = v.selected.Kind
	if v.editKind == "" {
		v.editKind = client.MemoryKindFact
	}
	v.editLabel.SetText(v.selected.Label)
	v.editSummary.SetText(v.selected.Summary)
	v.editKeywords.SetText(strings.Join(v.selected.Keywords, ", "))
	v.editBody.SetText(v.selected.Body)
	v.detailErr = ""
	v.mode = memModeEdit
}

func (v *memoryView) saveEdit(sc *screens) {
	if sc.memoryWorker == nil || v.detailBusy {
		return
	}
	v.detailBusy = true
	v.detailErr = ""
	var kw []string
	for _, raw := range strings.Split(v.editKeywords.Text(), ",") {
		if s := strings.TrimSpace(raw); s != "" {
			kw = append(kw, s)
		}
	}
	in := client.WriteMemoryInput{
		WorkspaceID: v.wsID,
		Op:          client.MemoryOpAdd,
		Kind:        v.editKind,
		Label:       strings.TrimSpace(v.editLabel.Text()),
		Summary:     strings.TrimSpace(v.editSummary.Text()),
		Body:        v.editBody.Text(),
		Keywords:    kw,
	}
	if !v.creating && v.selected != nil {
		in.Op = client.MemoryOpUpdate
		in.ID = v.selected.ID
		in.ExpectedRevision = v.selected.Revision
		anchors := make([]client.MemoryAnchorInput, 0, len(v.selected.Anchors))
		for _, a := range v.selected.Anchors {
			anchors = append(anchors, client.MemoryAnchorInput{RootName: a.RootName, RelPath: a.RelPath})
		}
		in.Anchors = anchors
	}
	sc.memoryWorker.Do(&memory.Request{Kind: memory.OpWriteEntry, WorkspaceID: v.wsID, Write: in})
}

func (v *memoryView) verifyCurrent(sc *screens) {
	if sc.memoryWorker == nil || v.detailBusy || v.selected == nil {
		return
	}
	v.detailBusy = true
	v.detailErr = ""
	sc.memoryWorker.Do(&memory.Request{
		Kind:        memory.OpVerifyEntry,
		WorkspaceID: v.wsID,
		EntryID:     v.selected.ID,
		Revision:    v.selected.Revision,
	})
}

func (v *memoryView) restoreRevision(sc *screens, restoreRev uint64) {
	if sc.memoryWorker == nil || v.detailBusy || v.selected == nil {
		return
	}
	v.detailBusy = true
	v.detailErr = ""
	sc.memoryWorker.Do(&memory.Request{
		Kind:            memory.OpRestoreEntry,
		WorkspaceID:     v.wsID,
		EntryID:         v.selected.ID,
		RestoreRevision: restoreRev,
		Revision:        v.selected.Revision,
	})
}

func (v *memoryView) applyWriteResult(sc *screens, res *memory.Result) {
	v.detailBusy = false
	if v.invDlg.dialog.IsOpen() {
		v.invDlg.applied(res, v)
	}
	if res.Err != nil {
		v.detailErr = hostErrorMessage(res.Err)
		if code, ok := client.MemoryCodeOf(res.Err); ok && code == client.MemoryCodeRevisionConflict && v.selected != nil {
			v.requestEntryDetail(sc, v.selected.ID)
		}
		return
	}
	v.detailErr = ""
	e := res.Entry
	v.selected = &e
	v.creating = false
	v.mode = memModeDetail
	v.requestEntryDetail(sc, e.ID)
	v.requestQuery(sc)
}

func (v *memoryView) layoutDetail(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	c := env.Th.Chrome()
	return v.scroll.Layout(env, gtx, kit.ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
		children := make([]layout.FlexChild, 0, 16)
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutDetailActions(gtx, env, sc)
		}), spacerH(theme.SpaceMD))

		if v.detailErr != "" {
			children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				d, ev, err := v.errBanner.Layout(env, gtx, kit.BannerProps{
					Kind: kit.BannerDanger, Title: "Update failed", Message: v.detailErr, Dismissible: true, Visible: true,
				})
				sc.fail(err)
				if ev.Dismissed {
					v.detailErr = ""
				}
				return d
			}), spacerH(theme.SpaceSM))
		}

		if v.selected == nil {
			children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return kit.Label(env, gtx, theme.TypeBody(), c.FgMuted, "Loading entry…")
			}))
			return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
		}

		e := v.selected
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return kit.Label(env, gtx, theme.TypeHeading(), c.Fg, e.Label)
		}), spacerH(theme.SpaceXS))

		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutDetailTags(gtx, env, sc, e)
		}), spacerH(theme.SpaceSM))

		meta := fmt.Sprintf("kind: %s · author: %s · r%d · updated %s", e.Kind, e.Author, e.Revision, e.UpdatedAt.UTC().Format("2006-01-02 15:04 UTC"))
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return kit.Paragraph(env, gtx, theme.TypeSmall(), c.FgMuted, meta)
		}), spacerH(theme.SpaceMD))

		if e.InvalidReason != "" || e.SupersededBy != "" {
			msg := "Invalidated"
			if e.InvalidReason != "" {
				msg += ": " + e.InvalidReason
			}
			if e.SupersededBy != "" {
				msg += " (superseded by " + e.SupersededBy + ")"
			}
			children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return kit.Paragraph(env, gtx, theme.TypeSmall(), env.Th.Status().Danger, msg)
			}), spacerH(theme.SpaceSM))
		}

		children = append(children,
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return kit.Paragraph(env, gtx, theme.TypeLabel(), c.Fg, e.Summary)
			}),
			spacerH(theme.SpaceSM),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return kit.Paragraph(env, gtx, theme.TypeProse(), c.Fg, e.Body)
			}),
			spacerH(theme.SpaceMD),
		)

		if len(e.Keywords) > 0 {
			children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return kit.Paragraph(env, gtx, theme.TypeSmall(), c.FgMuted, "Keywords: "+strings.Join(e.Keywords, ", "))
			}), spacerH(theme.SpaceMD))
		}

		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutAnchors(gtx, env, sc, e)
		}), spacerH(theme.SpaceLG))

		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutHistorySection(gtx, env, sc, e)
		}))

		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
	})
}

func (v *memoryView) layoutDetailActions(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	items := []kit.FlowItem{
		{
			Size: v.backBtn.Measure(env, gtx, kit.ButtonProps{Text: "Back", Icon: kit.IconArrowLeft, Variant: kit.Ghost, Size: kit.SizeSM}).Size,
			Draw: func(g *layout.Context) {
				if clicked, err := v.backBtn.Layout(env, g, kit.ButtonProps{Text: "Back", Icon: kit.IconArrowLeft, Variant: kit.Ghost, Size: kit.SizeSM}); err != nil {
					sc.fail(err)
				} else if clicked {
					v.mode = memModeList
				}
			},
		},
	}
	if v.selected != nil {
		e := v.selected
		valid := e.Validity == client.MemoryValidityValid
		items = append(items, kit.FlowItem{
			Size: v.editBtn.Measure(env, gtx, kit.ButtonProps{Text: "Edit", Icon: kit.IconPencil, Variant: kit.Secondary, Size: kit.SizeSM, Disabled: v.detailBusy || !valid}).Size,
			Draw: func(g *layout.Context) {
				if clicked, err := v.editBtn.Layout(env, g, kit.ButtonProps{Text: "Edit", Icon: kit.IconPencil, Variant: kit.Secondary, Size: kit.SizeSM, Disabled: v.detailBusy || !valid}); err != nil {
					sc.fail(err)
				} else if clicked {
					v.startEdit()
				}
			},
		})
		if valid && !e.Verified {
			items = append(items, kit.FlowItem{
				Size: v.verifyBtn.Measure(env, gtx, kit.ButtonProps{Text: "Verify", Icon: kit.IconCheck, Variant: kit.Primary, Size: kit.SizeSM, Busy: v.detailBusy}).Size,
				Draw: func(g *layout.Context) {
					if clicked, err := v.verifyBtn.Layout(env, g, kit.ButtonProps{Text: "Verify", Icon: kit.IconCheck, Variant: kit.Primary, Size: kit.SizeSM, Busy: v.detailBusy}); err != nil {
						sc.fail(err)
					} else if clicked {
						v.verifyCurrent(sc)
					}
				},
			})
		}
		if valid {
			items = append(items, kit.FlowItem{
				Size: v.invBtn.Measure(env, gtx, kit.ButtonProps{Text: "Invalidate…", Variant: kit.Danger, Size: kit.SizeSM, Disabled: v.detailBusy}).Size,
				Draw: func(g *layout.Context) {
					if clicked, err := v.invBtn.Layout(env, g, kit.ButtonProps{Text: "Invalidate…", Variant: kit.Danger, Size: kit.SizeSM, Disabled: v.detailBusy}); err != nil {
						sc.fail(err)
					} else if clicked {
						v.invDlg.open(v.wsID, e)
					}
				},
			})
		}
	}
	return kit.FlowRow(gtx, gtx.Dp(theme.SpaceSM), gtx.Dp(theme.SpaceXS), items)
}

func (v *memoryView) layoutDetailTags(gtx *layout.Context, env *kit.Env, sc *screens, e *client.MemoryEntry) layout.Dimensions {
	var tags []kit.FlowItem
	verProps := kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusSuccess, Text: "verified"}
	if !e.Verified {
		verProps = kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusWarning, Text: "unverified"}
	}
	if sz, err := v.verTag.Measure(env, gtx, verProps); err == nil {
		tags = append(tags, kit.FlowItem{Size: sz.Size, Draw: func(g *layout.Context) {
			_, lErr := v.verTag.Layout(env, g, verProps)
			sc.fail(lErr)
		}})
	}
	if e.Freshness == client.MemoryFreshnessStale {
		stProps := kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusDanger, Text: "may be outdated"}
		if sz, err := v.staleTag.Measure(env, gtx, stProps); err == nil {
			tags = append(tags, kit.FlowItem{Size: sz.Size, Draw: func(g *layout.Context) {
				_, lErr := v.staleTag.Layout(env, g, stProps)
				sc.fail(lErr)
			}})
		}
	}
	valProps := kit.TagProps{Variant: kit.TagNeutral, Text: e.Validity}
	if e.Validity != client.MemoryValidityValid {
		valProps = kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusDanger, Text: e.Validity}
	}
	if sz, err := v.valTag.Measure(env, gtx, valProps); err == nil {
		tags = append(tags, kit.FlowItem{Size: sz.Size, Draw: func(g *layout.Context) {
			_, lErr := v.valTag.Layout(env, g, valProps)
			sc.fail(lErr)
		}})
	}
	return kit.FlowRow(gtx, gtx.Dp(theme.SpaceXS), gtx.Dp(theme.SpaceXS), tags)
}

func (v *memoryView) layoutAnchors(gtx *layout.Context, env *kit.Env, sc *screens, e *client.MemoryEntry) layout.Dimensions {
	c := env.Th.Chrome()
	children := []layout.FlexChild{
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return kit.Label(env, gtx, theme.TypeLabel(), c.Fg, "Anchors")
		}),
		spacerH(theme.SpaceXS),
	}
	if len(e.AnchorStatuses) == 0 {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return kit.Label(env, gtx, theme.TypeSmall(), c.FgMuted, "No file anchors attached.")
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
	}
	for len(v.anchorTags) < len(e.AnchorStatuses) {
		v.anchorTags = append(v.anchorTags, kit.NewTagState())
	}
	for i, a := range e.AnchorStatuses {
		idx, anc := i, a
		statusText := "unknown"
		statusKind := theme.StatusWarning
		switch anc.Freshness {
		case client.MemoryFreshnessFresh:
			statusText = "fresh"
			statusKind = theme.StatusSuccess
		case client.MemoryFreshnessStale:
			statusText = "may be outdated"
			statusKind = theme.StatusDanger
		}
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return kit.Label(env, &gtx, theme.TypeCodeSmall(), c.Fg, anc.RootName+":"+anc.RelPath)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					tp := kit.TagProps{Variant: kit.TagStatus, Status: statusKind, Text: statusText}
					d, err := v.anchorTags[idx].Layout(env, &gtx, tp)
					sc.fail(err)
					return d
				}),
			)
		}), spacerH(theme.SpaceXXS))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func (v *memoryView) layoutHistorySection(gtx *layout.Context, env *kit.Env, sc *screens, e *client.MemoryEntry) layout.Dimensions {
	c := env.Th.Chrome()
	rows := make([]RevisionRow, 0, len(v.history))
	for i := len(v.history) - 1; i >= 0; i-- {
		h := v.history[i]
		rows = append(rows, RevisionRow{
			Revision:   h.Revision,
			Time:       h.WrittenAt,
			Author:     h.Author,
			Operation:  h.Op,
			Summary:    h.Summary,
			Current:    h.Revision == e.Revision,
			CanRestore: e.Validity == client.MemoryValidityValid && h.Revision != e.Revision,
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return kit.Label(env, gtx, theme.TypeLabel(), c.Fg, "Revision history")
		}),
		spacerH(theme.SpaceXS),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			if len(rows) == 0 {
				return kit.Label(env, gtx, theme.TypeSmall(), c.FgMuted, "No revision history loaded.")
			}
			rev, ok, dims := v.revHistory.Layout(env, gtx, rows, v.detailBusy, sc.fail)
			if ok {
				v.restoreRevision(sc, rev)
			}
			return dims
		}),
	)
}

func (v *memoryView) layoutEdit(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	return v.scroll.Layout(env, gtx, kit.ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
		canSave := !v.detailBusy && strings.TrimSpace(v.editLabel.Text()) != "" && strings.TrimSpace(v.editSummary.Text()) != ""
		children := make([]layout.FlexChild, 0, 14)
		if v.detailErr != "" {
			children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				d, ev, err := v.errBanner.Layout(env, gtx, kit.BannerProps{
					Kind: kit.BannerDanger, Title: "Could not save entry", Message: v.detailErr, Dismissible: true, Visible: true,
				})
				sc.fail(err)
				if ev.Dismissed {
					v.detailErr = ""
				}
				return d
			}), spacerH(theme.SpaceSM))
		}
		children = append(children,
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				res, err := v.editKindSel.Layout(env, gtx, &kit.SelectProps{
					Label: "Kind", Block: true, Value: v.editKind, Disabled: v.detailBusy,
					Options: memoryKindFilterOptions()[1:],
				})
				sc.fail(err)
				if res.Changed {
					v.editKind = res.Value
				}
				return v.editKindSel.Measure(env, gtx, &kit.SelectProps{Label: "Kind", Block: true, Value: v.editKind, Options: memoryKindFilterOptions()[1:]})
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				pr := &kit.TextFieldProps{Label: "Label", Placeholder: "short-unique-label", Disabled: v.detailBusy}
				_, err := v.editLabel.Layout(env, gtx, pr)
				sc.fail(err)
				return v.editLabel.Measure(env, gtx, pr)
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				pr := &kit.TextFieldProps{Label: "Summary", Placeholder: "One-line summary (max 200 chars)", Disabled: v.detailBusy}
				_, err := v.editSummary.Layout(env, gtx, pr)
				sc.fail(err)
				return v.editSummary.Measure(env, gtx, pr)
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				pr := &kit.TextFieldProps{Label: "Keywords (comma-separated)", Placeholder: "auth, vault, tokens", Disabled: v.detailBusy}
				_, err := v.editKeywords.Layout(env, gtx, pr)
				sc.fail(err)
				return v.editKeywords.Measure(env, gtx, pr)
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				pr := &kit.TextAreaProps{Label: "Body (Markdown)", Placeholder: "Detailed explanation…", MaxLines: 12, Disabled: v.detailBusy}
				_ = v.editBody.Layout(env, gtx, pr)
				return v.editBody.Measure(env, gtx, pr)
			}),
			spacerH(theme.SpaceLG),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return kit.FlowRow(gtx, gtx.Dp(theme.SpaceMD), gtx.Dp(theme.SpaceSM), []kit.FlowItem{
					{
						Size: v.saveBtn.Measure(env, gtx, kit.ButtonProps{Text: "Save & verify", Variant: kit.Primary, Size: kit.SizeMD, Busy: v.detailBusy, Disabled: !canSave}).Size,
						Draw: func(g *layout.Context) {
							if clicked, err := v.saveBtn.Layout(env, g, kit.ButtonProps{Text: "Save & verify", Variant: kit.Primary, Size: kit.SizeMD, Busy: v.detailBusy, Disabled: !canSave}); err != nil {
								sc.fail(err)
							} else if clicked {
								v.saveEdit(sc)
							}
						},
					},
					{
						Size: v.cancelEditBtn.Measure(env, gtx, kit.ButtonProps{Text: "Cancel", Variant: kit.Secondary, Size: kit.SizeMD, Disabled: v.detailBusy}).Size,
						Draw: func(g *layout.Context) {
							if clicked, err := v.cancelEditBtn.Layout(env, g, kit.ButtonProps{Text: "Cancel", Variant: kit.Secondary, Size: kit.SizeMD, Disabled: v.detailBusy}); err != nil {
								sc.fail(err)
							} else if clicked {
								if v.selected != nil {
									v.mode = memModeDetail
								} else {
									v.mode = memModeList
								}
							}
						},
					},
				})
			}),
		)
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
	})
}
