package mdview

import (
	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// OpenLink is the popover's request that the caller open URL. The renderer
// never opens anything itself.
type OpenLink struct {
	// URL is the sanitized destination of an openable link.
	URL string
}

// State is the per-document widget state: the laid-out text, the popover
// state of each link, the scroll position of each code block and the queue of
// open requests. The zero value is ready; use one State per rendered
// document and call Layout with the same State every frame. It belongs to the
// window goroutine.
type State struct {
	doc    *markdown.Doc
	shaper *text.Shaper
	metric unit.Metric
	chrome theme.Chrome
	flows  map[flowKey]*flow
	links  map[*markdown.Link]*linkState
	codes  map[*markdown.Block]*widget.List
	marks  map[*markdown.ListItem][]markdown.Span
	pinned *markdown.Link
	opens  []OpenLink
}

// PollOpen returns the next queued open request, oldest first.
func (s *State) PollOpen() (OpenLink, bool) {
	if len(s.opens) == 0 {
		return OpenLink{}, false
	}
	next := s.opens[0]
	s.opens = s.opens[1:]
	return next, true
}

// sync keeps the state valid for this frame: another document starts with
// fresh link and scroll state, and a changed shaper, scale or theme mode
// drops the laid-out text, which depends on them.
func (s *State) sync(doc *markdown.Doc, shaper *text.Shaper, m unit.Metric, chrome theme.Chrome) {
	if s.doc != doc {
		s.doc = doc
		s.links = map[*markdown.Link]*linkState{}
		s.codes = map[*markdown.Block]*widget.List{}
		s.marks = map[*markdown.ListItem][]markdown.Span{}
		s.pinned = nil
		s.flows = nil
	}
	if s.shaper != shaper || s.metric != m || s.chrome != chrome || len(s.flows) > maxCachedFlows {
		s.shaper, s.metric, s.chrome = shaper, m, chrome
		s.flows = nil
	}
	if s.flows == nil {
		s.flows = map[flowKey]*flow{}
	}
}

// link returns the popover state of one link, created on first use.
func (s *State) link(l *markdown.Link) *linkState {
	ls, ok := s.links[l]
	if !ok {
		ls = &linkState{}
		s.links[l] = ls
	}
	return ls
}

// codeList returns the horizontal scroll state of one code block.
func (s *State) codeList(b *markdown.Block) *widget.List {
	l, ok := s.codes[b]
	if !ok {
		l = &widget.List{List: layout.List{Axis: layout.Horizontal}}
		s.codes[b] = l
	}
	return l
}

// marker returns the one-span run of an item's marker, kept so its laid-out
// text is cached under a stable address.
func (s *State) marker(item *markdown.ListItem, label string) []markdown.Span {
	m, ok := s.marks[item]
	if !ok {
		m = []markdown.Span{{Text: label}}
		s.marks[item] = m
	}
	return m
}
