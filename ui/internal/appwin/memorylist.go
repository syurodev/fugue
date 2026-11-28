package appwin

import (
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/memory"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func (v *memoryView) requestQuery(sc *screens) {
	if sc.memoryWorker == nil || v.wsID == "" {
		return
	}
	v.loading = true
	var verPtr *bool
	switch v.verifiedFilter {
	case "true":
		t := true
		verPtr = &t
	case "false":
		f := false
		verPtr = &f
	}
	if strings.TrimSpace(v.query) != "" {
		sc.memoryWorker.Do(&memory.Request{
			Kind:        memory.OpSearchEntries,
			WorkspaceID: v.wsID,
			Search: client.SearchMemoryInput{
				WorkspaceID:    v.wsID,
				Query:          v.query,
				Kind:           v.kindFilter,
				IncludeInvalid: v.validityFilter == client.MemoryValidityInvalid || v.validityFilter == client.MemoryValiditySuperseded,
				Limit:          30,
				Verified:       verPtr,
				Author:         v.authorFilter,
				Validity:       v.validityFilter,
			},
		})
		return
	}
	sc.memoryWorker.Do(&memory.Request{
		Kind:        memory.OpListEntries,
		WorkspaceID: v.wsID,
		List: client.ListMemoryInput{
			WorkspaceID: v.wsID,
			Kind:        v.kindFilter,
			Verified:    verPtr,
			Author:      v.authorFilter,
			Validity:    v.validityFilter,
		},
	})
}

func (v *memoryView) widgetsFor(id string) *memoryRowWidgets {
	if w, ok := v.rowWidgets[id]; ok {
		return w
	}
	w := &memoryRowWidgets{
		row:           kit.NewListRowState(),
		unverifiedTag: kit.NewTagState(),
		staleTag:      kit.NewTagState(),
	}
	v.rowWidgets[id] = w
	return w
}

type memoryListItem struct {
	id        string
	label     string
	kind      string
	summary   string
	verified  bool
	freshness string
}

func (v *memoryView) visibleItems() []memoryListItem {
	var out []memoryListItem
	if v.inSearch {
		for i := range v.results {
			r := &v.results[i]
			if v.freshnessFilter != "" && r.Freshness != v.freshnessFilter {
				continue
			}
			sec := r.Summary
			if r.Snippet != "" {
				sec = r.Snippet
			}
			out = append(out, memoryListItem{
				id: r.ID, label: r.Label, kind: r.Kind, summary: sec,
				verified: r.Verified, freshness: r.Freshness,
			})
		}
		return out
	}
	for i := range v.entries {
		e := &v.entries[i]
		if v.freshnessFilter != "" && e.Freshness != v.freshnessFilter {
			continue
		}
		out = append(out, memoryListItem{
			id: e.ID, label: e.Label, kind: e.Kind, summary: e.Summary,
			verified: e.Verified, freshness: e.Freshness,
		})
	}
	return out
}

func (v *memoryView) layoutList(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	children := make([]layout.FlexChild, 0, 8)
	if v.listErr != "" {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			d, ev, err := v.errBanner.Layout(env, gtx, kit.BannerProps{
				Kind: kit.BannerDanger, Title: "Memory error", Message: v.listErr, Dismissible: true, Visible: true,
			})
			sc.fail(err)
			if ev.Dismissed {
				v.listErr = ""
			}
			return d
		}), spacerH(theme.SpaceSM))
	}
	if v.infoMsg != "" {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			d, ev, err := v.infoBanner.Layout(env, gtx, kit.BannerProps{
				Kind: kit.BannerInfo, Title: "Workspace memory", Message: v.infoMsg, Dismissible: true, Visible: true,
			})
			sc.fail(err)
			if ev.Dismissed {
				v.infoMsg = ""
			}
			return d
		}), spacerH(theme.SpaceSM))
	}

	children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return v.layoutFilterBar(gtx, env, sc)
	}), spacerH(theme.SpaceMD))

	if v.inSearch && v.approx {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			d, _, err := v.approxBanner.Layout(env, gtx, kit.BannerProps{
				Kind:    kit.BannerInfo,
				Title:   "Approximate matches",
				Message: "No exact match was found; showing closest trigram matches. Try another keyword.",
				Visible: true,
			})
			sc.fail(err)
			return d
		}), spacerH(theme.SpaceSM))
	}

	items := v.visibleItems()
	if len(items) == 0 {
		children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return v.layoutEmpty(&local, env, sc)
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
	}

	children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		return v.scroll.Layout(env, &gtx, kit.ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
			rows := make([]layout.FlexChild, 0, len(items))
			for i := range items {
				it := items[i]
				rows = append(rows, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
					return v.layoutEntryRow(gtx, env, sc, &it)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, rows...)
		})
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func (v *memoryView) layoutFilterBar(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	props := kit.FilterBarProps{
		SearchPlaceholder: "Search workspace memory…",
		Filters: []kit.FilterSelect{
			{ID: "kind", Placeholder: "Kind", Selected: v.kindFilter, Options: memoryKindFilterOptions()},
			{ID: "verified", Placeholder: "Verification", Selected: v.verifiedFilter, Options: []kit.SelectOption{
				{ID: "", Label: "All verification"},
				{ID: "true", Label: "Verified"},
				{ID: "false", Label: "Unverified"},
			}},
			{ID: "author", Placeholder: "Author", Selected: v.authorFilter, Options: []kit.SelectOption{
				{ID: "", Label: "All authors"},
				{ID: client.MemoryAuthorUser, Label: "User"},
				{ID: client.MemoryAuthorAgent, Label: "Agent"},
			}},
			{ID: "freshness", Placeholder: "Freshness", Selected: v.freshnessFilter, Options: []kit.SelectOption{
				{ID: "", Label: "All freshness"},
				{ID: client.MemoryFreshnessFresh, Label: "Fresh"},
				{ID: client.MemoryFreshnessStale, Label: "May be outdated"},
				{ID: client.MemoryFreshnessUnknown, Label: "No anchors"},
			}},
			{ID: "validity", Placeholder: "Validity", Selected: v.validityFilter, Options: []kit.SelectOption{
				{ID: "", Label: "Valid"},
				{ID: client.MemoryValiditySuperseded, Label: "Superseded"},
				{ID: client.MemoryValidityInvalid, Label: "Invalid"},
			}},
		},
	}
	dims, res, err := v.filterBar.Layout(env, gtx, props)
	sc.fail(err)
	if res.Changed {
		v.query = res.State.Query
		v.kindFilter = res.State.Selected["kind"]
		v.verifiedFilter = res.State.Selected["verified"]
		v.authorFilter = res.State.Selected["author"]
		v.freshnessFilter = res.State.Selected["freshness"]
		v.validityFilter = res.State.Selected["validity"]
		v.scroll.SetOffset(0)
		v.requestQuery(sc)
	}
	return dims
}

func (v *memoryView) layoutEntryRow(gtx *layout.Context, env *kit.Env, sc *screens, it *memoryListItem) layout.Dimensions {
	w := v.widgetsFor(it.id)
	sec := it.kind
	if it.summary != "" {
		sec = it.kind + " · " + it.summary
	}
	props := kit.ListRowProps{
		Icon:      kit.IconFileText,
		Label:     it.label,
		Secondary: sec,
	}
	if !it.verified {
		props.StateLabel = "unverified"
		props.StateTone = kit.ToneWarning
	}
	drawRow := func(gtx *layout.Context) layout.Dimensions {
		d, ev := w.row.Layout(env, gtx, &props)
		sc.fail(ev.Err)
		if ev.Clicked || ev.Opened {
			v.openEntry(sc, it.id)
		}
		return d
	}
	if it.verified && it.freshness != client.MemoryFreshnessStale {
		return drawRow(gtx)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(drawRow),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceXXL, Bottom: theme.SpaceXS}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
				var tags []kit.FlowItem
				if !it.verified {
					tp := kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusWarning, Text: "unverified"}
					if sz, err := w.unverifiedTag.Measure(env, &gtx, tp); err == nil {
						tags = append(tags, kit.FlowItem{Size: sz.Size, Draw: func(g *layout.Context) {
							_, lErr := w.unverifiedTag.Layout(env, g, tp)
							sc.fail(lErr)
						}})
					}
				}
				if it.freshness == client.MemoryFreshnessStale {
					tp := kit.TagProps{Variant: kit.TagStatus, Status: theme.StatusDanger, Text: "may be outdated"}
					if sz, err := w.staleTag.Measure(env, &gtx, tp); err == nil {
						tags = append(tags, kit.FlowItem{Size: sz.Size, Draw: func(g *layout.Context) {
							_, lErr := w.staleTag.Layout(env, g, tp)
							sc.fail(lErr)
						}})
					}
				}
				return kit.FlowRow(&gtx, gtx.Dp(theme.SpaceXS), gtx.Dp(theme.SpaceXS), tags)
			})
		}),
	)
}

func (v *memoryView) layoutEmpty(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	if v.loading && !v.loaded {
		d, _, err := v.empty.Layout(env, gtx, &kit.EmptyStateProps{
			Icon:        kit.IconFileText,
			Title:       "Loading memory…",
			Description: "Fetching entries for this workspace.",
		})
		sc.fail(err)
		return d
	}
	hasFilter := v.query != "" || v.kindFilter != "" || v.verifiedFilter != "" ||
		v.authorFilter != "" || v.freshnessFilter != "" || v.validityFilter != ""
	if hasFilter {
		d, ev, err := v.empty.Layout(env, gtx, &kit.EmptyStateProps{
			Icon:              kit.IconSearch,
			Title:             "No matching memory entries",
			Description:       "Keep your filters or clear them to see all entries.",
			NoResults:         true,
			ClearFilterAction: "Clear filters",
		})
		sc.fail(err)
		if ev.ClearFilterClicked {
			v.filterBar.SetQuery("")
			v.query, v.kindFilter, v.verifiedFilter, v.authorFilter, v.freshnessFilter, v.validityFilter = "", "", "", "", "", ""
			v.requestQuery(sc)
		}
		return d
	}
	d, ev, err := v.empty.Layout(env, gtx, &kit.EmptyStateProps{
		Icon:          kit.IconFileText,
		Title:         "No workspace memory yet",
		Description:   "Record durable facts, conventions, decisions, and how-tos for this workspace.",
		PrimaryAction: "New entry",
	})
	sc.fail(err)
	if ev.PrimaryClicked {
		v.startCreate()
	}
	return d
}

func memoryKindFilterOptions() []kit.SelectOption {
	return []kit.SelectOption{
		{ID: "", Label: "All kinds"},
		{ID: client.MemoryKindPinned, Label: "Pinned"},
		{ID: client.MemoryKindFact, Label: "Fact"},
		{ID: client.MemoryKindDecision, Label: "Decision"},
		{ID: client.MemoryKindConvention, Label: "Convention"},
		{ID: client.MemoryKindHowto, Label: "How-to"},
		{ID: client.MemoryKindNote, Label: "Note"},
	}
}
