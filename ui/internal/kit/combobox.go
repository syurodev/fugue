package kit

import (
	"image"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
)

// Combobox, ui-kit §6.14: a TextField search core paired with a SuggestList
// on the window overlay (UK-07, UK-08). The caller owns the items and the
// async loading flag; the default filter folds case and Vietnamese diacritics
// with the Menu's foldTypeahead rule (substring match, prefix matches ranked
// first). Editor text includes IME composing text and filters on it, but
// never chooses while composing. Arrows move the highlight while the caret
// stays in the field; Enter chooses; Tab chooses only when the list is open
// with a highlight; Escape closes the list and a second Escape clears the
// text; focus leaving the field closes the list.

// ComboboxProps are the per-frame, caller-owned properties (UK-02).
type ComboboxProps struct {
	Label       string
	Placeholder string
	Helper      string
	Error       string
	Items       []SuggestItem
	Loading     bool
	EmptyText   string
	NoResText   string
	Filter      func(query string, items []SuggestItem) []SuggestItem
	Disabled    bool
}

// ComboboxResult reports what happened in the frame (UK-06).
type ComboboxResult struct {
	Changed   bool
	Chosen    string
	FocusLost bool
}

// ComboboxState is the long-lived state of one combobox (UK-02).
type ComboboxState struct {
	field *TextFieldState
	list  *SuggestListState
}

// NewComboboxState builds the state for one combobox.
func NewComboboxState() *ComboboxState {
	return &ComboboxState{
		field: NewTextFieldState(),
		list:  NewSuggestListState(),
	}
}

// Text returns the field's current text (including any IME composing text).
func (s *ComboboxState) Text() string { return s.field.Text() }

// SetText replaces the field's text.
func (s *ComboboxState) SetText(v string) { s.field.SetText(v) }

// Tag returns the combobox's focus tag (its underlying editor).
func (s *ComboboxState) Tag() event.Tag { return s.field.Tag() }

// IsOpen reports whether the suggestion list is open.
func (s *ComboboxState) IsOpen() bool { return s.list.IsOpen() }

// Open opens the suggestion list.
func (s *ComboboxState) Open() { s.list.Open() }

// Close closes the suggestion list.
func (s *ComboboxState) Close() { s.list.Close() }

// MarkSubmitted opens the §6.8 error gate on the field.
func (s *ComboboxState) MarkSubmitted() { s.field.MarkSubmitted() }

// Highlight returns the currently highlighted suggestion index, or -1.
func (s *ComboboxState) Highlight() int { return s.list.Highlight() }

// DefaultComboboxFilter folds case and Vietnamese diacritics with
// foldTypeahead, returning items whose folded label contains the folded
// query, with prefix matches ranked first.
func DefaultComboboxFilter(query string, items []SuggestItem) []SuggestItem {
	q := foldTypeahead(strings.TrimSpace(query))
	if q == "" {
		return items
	}
	var prefix, sub []SuggestItem
	for _, it := range items {
		folded := foldTypeahead(it.Label)
		switch {
		case strings.HasPrefix(folded, q):
			prefix = append(prefix, it)
		case strings.Contains(folded, q):
			sub = append(sub, it)
		}
	}
	return append(prefix, sub...)
}

// Measure returns the field's natural box.
func (s *ComboboxState) Measure(env *Env, gtx *layout.Context, p *ComboboxProps) layout.Dimensions {
	return s.field.Measure(env, gtx, p.fieldProps())
}

// Layout draws the search field and, when open, the SuggestList on the overlay.
func (s *ComboboxState) Layout(env *Env, gtx *layout.Context, p *ComboboxProps) (ComboboxResult, error) {
	var res ComboboxResult
	filtered := filterSuggestItems(s.field.Text(), p.Items, p.Filter)
	if !p.Disabled {
		s.handleKeys(gtx, filtered, &res)
	} else {
		s.list.Close()
	}

	tfProps := p.fieldProps()
	fev, err := s.field.Layout(env, gtx, tfProps)
	if err != nil {
		return res, err
	}
	if fev.Changed {
		res.Changed = true
		filtered = filterSuggestItems(s.field.Text(), p.Items, p.Filter)
		if !p.Disabled && s.field.Text() != "" {
			s.list.Open()
		}
	}
	if fev.FocusLost {
		res.FocusLost = true
		s.list.Close()
	}

	pre := fieldMetrics(env, gtx, p.Label)
	w := textFieldWidth(env, gtx, tfProps)
	anchor := image.Rect(0, pre.labelH, w, pre.labelH+pre.boxH)

	lres, err := s.list.Layout(env, gtx, &SuggestProps{
		Items:     filtered,
		Anchor:    anchor,
		Query:     s.field.Text(),
		Loading:   p.Loading,
		EmptyText: p.EmptyText,
		NoResText: p.NoResText,
		MinWidth:  w,
	})
	if err != nil {
		return res, err
	}
	if lres.Chosen != "" {
		res.Chosen = lres.Chosen
	}
	return res, nil
}

func (p *ComboboxProps) fieldProps() *TextFieldProps {
	return &TextFieldProps{
		Label:       p.Label,
		Placeholder: p.Placeholder,
		Helper:      p.Helper,
		Error:       p.Error,
		Variant:     TextFieldSearch,
		Disabled:    p.Disabled,
	}
}

func filterSuggestItems(query string, items []SuggestItem, fn func(string, []SuggestItem) []SuggestItem) []SuggestItem {
	if fn != nil {
		return fn(query, items)
	}
	return DefaultComboboxFilter(query, items)
}

func (s *ComboboxState) handleKeys(gtx *layout.Context, items []SuggestItem, res *ComboboxResult) {
	tag := s.field.Tag()
	if !s.list.IsOpen() {
		s.openOnDown(gtx, tag)
		return
	}
	s.handleOpenKeys(gtx, tag, items, res)
}

func (s *ComboboxState) openOnDown(gtx *layout.Context, tag event.Tag) {
	for {
		ev, ok := gtx.Event(key.Filter{Focus: tag, Name: key.NameDownArrow})
		if !ok {
			return
		}
		if ke, is := ev.(key.Event); is && ke.State == key.Press {
			s.list.Open()
			return
		}
	}
}

func (s *ComboboxState) handleOpenKeys(gtx *layout.Context, tag event.Tag, items []SuggestItem, res *ComboboxResult) {
	filters := []event.Filter{
		key.Filter{Focus: tag, Name: key.NameUpArrow},
		key.Filter{Focus: tag, Name: key.NameDownArrow},
		key.Filter{Focus: tag, Name: key.NameHome},
		key.Filter{Focus: tag, Name: key.NameEnd},
		key.Filter{Focus: tag, Name: key.NamePageUp},
		key.Filter{Focus: tag, Name: key.NamePageDown},
		key.Filter{Focus: tag, Name: key.NameEscape},
	}
	if !s.field.core.composing {
		filters = append(filters,
			key.Filter{Focus: tag, Name: key.NameReturn},
			key.Filter{Focus: tag, Name: key.NameEnter},
			key.Filter{Focus: tag, Name: key.NameTab},
		)
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		ke, is := ev.(key.Event)
		if !is || ke.State != key.Press {
			continue
		}
		chosen, _, handled := s.list.HandleKey(ke, items)
		if chosen != "" {
			res.Chosen = chosen
		}
		if handled && ke.Name == key.NameEscape {
			return
		}
	}
}

func (f *fieldCore) primeSubRouter() {
	f.subOps.Reset()
	area := clip.Rect(image.Rect(0, 0, 1, 1)).Push(&f.subOps)
	event.Op(&f.subOps, &f.ed)
	area.Pop()
	f.subRouter.Frame(&f.subOps)
	src := f.subRouter.Source()
	src.Event(key.FocusFilter{Target: &f.ed})
	f.subRouter.Frame(&f.subOps)
	src = f.subRouter.Source()
	src.Event(key.FocusFilter{Target: &f.ed})
	src.Execute(key.FocusCmd{Tag: &f.ed})
	f.subRouter.Frame(&f.subOps)
	f.subRouter.Source().Event(key.FocusFilter{Target: &f.ed})
}

func (f *fieldCore) stepSubRouter() {
	f.subRouter.Frame(&f.subOps)
}

func (f *fieldCore) drainIME(gtx *layout.Context, o fieldOpts, ev *FieldEvents) {
	if !f.subReady {
		f.subReady = true
		f.primeSubRouter()
	}
	var any bool
	for {
		e, ok := gtx.Event(key.FocusFilter{Target: &f.ed})
		if !ok {
			break
		}
		any = true
		switch ke := e.(type) {
		case key.CompositionEvent:
			f.composing = ke.Start >= 0 && ke.End > ke.Start
		case key.FocusEvent:
			if !ke.Focus {
				f.composing = false
			}
		}
		f.subRouter.Queue(e)
	}
	if !any {
		return
	}
	sub := *gtx
	sub.Source = f.subRouter.Source()
	f.drainEditor(&sub, o, ev)
	f.stepSubRouter()
}
