package kit

import (
	"image"
	"maps"
	"time"

	"gioui.org/io/event"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// FilterBar exists so list views (such as Workspace Memory, sessions, or tasks)
// share one domain-free search + N Select filter strip with a Clear action
// rather than building ad-hoc toolbars per screen. It owns only the query and
// selected filter values it emits, debouncing typed search queries through the
// injected frame clock (Env.Now) without timers or goroutines (UK-02, UK-06).
//
//	dims, res, err := bar.Layout(env, gtx, kit.FilterBarProps{
//	    SearchPlaceholder: "Search…",
//	    Filters: []kit.FilterSelect{{ID: "kind", Placeholder: "All kinds", Options: opts}},
//	})

const defaultFilterDebounce = 200 * time.Millisecond

// FilterSelect describes one Select filter in a FilterBar.
type FilterSelect struct {
	ID          string
	Placeholder string
	Options     []SelectOption
	Selected    string
}

// FilterBarProps configures a FilterBar for one frame (UK-02).
type FilterBarProps struct {
	SearchPlaceholder string
	Filters           []FilterSelect
	ClearLabel        string
	Debounce          time.Duration
	Disabled          bool
}

// FilterState is the emitted query and per-filter selections.
type FilterState struct {
	Query    string
	Selected map[string]string
}

// FilterBarResult reports what changed in the frame (UK-06).
type FilterBarResult struct {
	Changed bool
	State   FilterState
}

// FilterBarState holds the long-lived state of one FilterBar (UK-02).
type FilterBarState struct {
	search       *TextFieldState
	selects      []*SelectState
	clearBtn     *ButtonState
	lastQuery    string
	emittedQuery string
	pending      bool
	emitAt       time.Time
}

// NewFilterBarState builds a FilterBarState.
func NewFilterBarState() *FilterBarState {
	return &FilterBarState{
		search:   NewTextFieldState(),
		clearBtn: NewButtonState(),
	}
}

// SearchTag returns the search TextField's input tag.
func (s *FilterBarState) SearchTag() event.Tag { return s.search.Tag() }

// ClearTag returns the Clear button's input tag.
func (s *FilterBarState) ClearTag() event.Tag { return s.clearBtn.Tag() }

// SelectTag returns the i-th Select's input tag if allocated.
func (s *FilterBarState) SelectTag(i int) event.Tag {
	if i < 0 || i >= len(s.selects) {
		return nil
	}
	return s.selects[i].Tag()
}

// Query returns the current search input text.
func (s *FilterBarState) Query() string { return s.search.Text() }

// SetQuery sets the search input text and synchronizes the emitted query.
func (s *FilterBarState) SetQuery(q string) {
	s.search.SetText(q)
	s.lastQuery = q
	s.emittedQuery = q
	s.pending = false
}

func (s *FilterBarState) ensureSelects(n int) {
	for len(s.selects) < n {
		s.selects = append(s.selects, NewSelectState())
	}
}

// Layout draws the search field, filter Selects, and Clear button in a wrapping
// FlowRow and reports debounced query or immediate filter changes.
func (s *FilterBarState) Layout(env *Env, gtx *layout.Context, p FilterBarProps) (layout.Dimensions, FilterBarResult, error) {
	s.ensureSelects(len(p.Filters))
	debounce := p.Debounce
	if debounce <= 0 {
		debounce = defaultFilterDebounce
	}
	clearLabel := p.ClearLabel
	if clearLabel == "" {
		clearLabel = "Clear filters"
	}

	selected := make(map[string]string, len(p.Filters))
	anySelected := false
	for _, f := range p.Filters {
		selected[f.ID] = f.Selected
		if f.Selected != "" {
			anySelected = true
		}
	}

	var res FilterBarResult
	var firstErr error
	noteErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	searchProps := TextFieldProps{
		Placeholder: p.SearchPlaceholder,
		Variant:     TextFieldSearch,
		Disabled:    p.Disabled,
	}
	if searchProps.Placeholder == "" {
		searchProps.Placeholder = "Search…"
	}
	searchGtx := *gtx
	searchGtx.Constraints.Min.X = min(gtx.Dp(theme.SidebarWidth), gtx.Constraints.Max.X)
	searchSize := s.search.Measure(env, &searchGtx, &searchProps).Size

	items := make([]FlowItem, 0, len(p.Filters)+2)
	items = append(items, FlowItem{
		Size: searchSize,
		Draw: func(g *layout.Context) {
			g.Constraints.Min.X = g.Constraints.Max.X
			ev, err := s.search.Layout(env, g, &searchProps)
			noteErr(err)
			cur := s.search.Text()
			switch {
			case ev.Submitted:
				s.lastQuery = cur
				if s.emittedQuery != cur || s.pending {
					s.emittedQuery = cur
					s.pending = false
					res.Changed = true
				}
			case ev.Changed || cur != s.lastQuery:
				s.lastQuery = cur
				if cur == "" && s.emittedQuery != "" {
					s.emittedQuery = ""
					s.pending = false
					res.Changed = true
				} else if cur != s.emittedQuery {
					s.pending = true
					s.emitAt = env.Now().Add(debounce)
					env.Keep()
				}
			}
		},
	})

	for i := range p.Filters {
		f := p.Filters[i]
		sel := s.selects[i]
		selProps := SelectProps{
			Placeholder: f.Placeholder,
			Options:     f.Options,
			Value:       f.Selected,
			Disabled:    p.Disabled,
		}
		selSize := sel.Measure(env, gtx, &selProps).Size
		items = append(items, FlowItem{
			Size: selSize,
			Draw: func(g *layout.Context) {
				selRes, err := sel.Layout(env, g, &selProps)
				noteErr(err)
				if selRes.Changed {
					selected[f.ID] = selRes.Value
					res.Changed = true
				}
			},
		})
	}

	hasActive := s.search.Text() != "" || s.emittedQuery != "" || anySelected
	clearProps := ButtonProps{
		Text:     clearLabel,
		Variant:  Ghost,
		Size:     SizeMD,
		Disabled: p.Disabled || !hasActive,
	}
	clearSize := s.clearBtn.Measure(env, gtx, clearProps).Size
	items = append(items, FlowItem{
		Size: clearSize,
		Draw: func(g *layout.Context) {
			clicked, err := s.clearBtn.Layout(env, g, clearProps)
			noteErr(err)
			if clicked {
				s.search.SetText("")
				s.lastQuery = ""
				s.emittedQuery = ""
				s.pending = false
				for k := range selected {
					selected[k] = ""
				}
				res.Changed = true
			}
		},
	})

	dims := FlowRow(gtx, gtx.Dp(theme.SpaceMD), gtx.Dp(theme.SpaceSM), items)

	if s.pending {
		if !env.Now().Before(s.emitAt) {
			s.pending = false
			if s.emittedQuery != s.lastQuery {
				s.emittedQuery = s.lastQuery
				res.Changed = true
			}
		} else {
			env.Keep()
		}
	}

	res.State = FilterState{
		Query:    s.emittedQuery,
		Selected: maps.Clone(selected),
	}
	return dims, res, firstErr
}

// Measure returns the FilterBar's bounding box for a single unclipped layout pass.
func (s *FilterBarState) Measure(env *Env, gtx *layout.Context, p FilterBarProps) image.Point {
	s.ensureSelects(len(p.Filters))
	clearLabel := p.ClearLabel
	if clearLabel == "" {
		clearLabel = "Clear filters"
	}
	searchProps := TextFieldProps{Placeholder: p.SearchPlaceholder, Variant: TextFieldSearch}
	if searchProps.Placeholder == "" {
		searchProps.Placeholder = "Search…"
	}
	searchGtx := *gtx
	searchGtx.Constraints.Min.X = min(gtx.Dp(theme.SidebarWidth), gtx.Constraints.Max.X)
	items := make([]FlowItem, 0, len(p.Filters)+2)
	items = append(items, FlowItem{Size: s.search.Measure(env, &searchGtx, &searchProps).Size})
	for i := range p.Filters {
		selProps := SelectProps{Placeholder: p.Filters[i].Placeholder, Options: p.Filters[i].Options, Value: p.Filters[i].Selected}
		items = append(items, FlowItem{Size: s.selects[i].Measure(env, gtx, &selProps).Size})
	}
	items = append(items, FlowItem{Size: s.clearBtn.Measure(env, gtx, ButtonProps{Text: clearLabel, Variant: Ghost, Size: SizeMD}).Size})
	_, size := FlowPlace(gtx.Constraints.Max.X, gtx.Dp(theme.SpaceMD), gtx.Dp(theme.SpaceSM), items)
	return size
}
