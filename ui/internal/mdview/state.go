package mdview

import (
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// OpenLink is the popover's request that the caller open URL. The renderer
// never opens anything itself.
type OpenLink struct {
	// URL is the sanitized destination of an openable link.
	URL string
}

// FileClick reports a click or activation on an inline file reference chip.
type FileClick struct {
	Ref FileRef
}

// IDClick reports a click or activation on an inline commit-hash or ID chip.
type IDClick struct {
	ID string
}

// GitRefClick reports a click or activation on an inline git reference chip.
type GitRefClick struct {
	Ref GitRef
}

type chipState struct {
	hover bool
}

// State is the per-document widget state: the laid-out text, the popover
// state of each link, the scroll/wrap state of each code block and the queue
// of open/chip requests. The zero value is ready; use one State per rendered
// document and call Layout with the same State every frame. It belongs to the
// window goroutine.
type State struct {
	doc          *markdown.Doc
	shaper       *text.Shaper
	metric       unit.Metric
	chrome       theme.Chrome
	flows        map[flowKey]*flow
	links        map[*markdown.Link]*linkState
	codeBlocks   map[*markdown.Block]*CodeBlockState
	chips        map[string]*chipState
	marks        map[*markdown.ListItem][]markdown.Span
	pinned       *markdown.Link
	opens        []OpenLink
	fileClicks   []FileClick
	idClicks     []IDClick
	gitRefClicks []GitRefClick
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

// PollFileClick returns the next queued file chip click, oldest first.
func (s *State) PollFileClick() (FileClick, bool) {
	if len(s.fileClicks) == 0 {
		return FileClick{}, false
	}
	next := s.fileClicks[0]
	s.fileClicks = s.fileClicks[1:]
	return next, true
}

// PollIDClick returns the next queued hash/ID chip click, oldest first.
func (s *State) PollIDClick() (IDClick, bool) {
	if len(s.idClicks) == 0 {
		return IDClick{}, false
	}
	next := s.idClicks[0]
	s.idClicks = s.idClicks[1:]
	return next, true
}

// PollGitRefClick returns the next queued git reference click, oldest first.
func (s *State) PollGitRefClick() (GitRefClick, bool) {
	if len(s.gitRefClicks) == 0 {
		return GitRefClick{}, false
	}
	next := s.gitRefClicks[0]
	s.gitRefClicks = s.gitRefClicks[1:]
	return next, true
}

// sync keeps the state valid for this frame: another document starts with
// fresh link and scroll state, and a changed shaper, scale or theme mode
// drops the laid-out text, which depends on them.
func (s *State) sync(doc *markdown.Doc, shaper *text.Shaper, m unit.Metric, chrome *theme.Chrome) {
	if s.doc != doc {
		s.doc = doc
		s.links = map[*markdown.Link]*linkState{}
		s.codeBlocks = map[*markdown.Block]*CodeBlockState{}
		s.chips = map[string]*chipState{}
		s.marks = map[*markdown.ListItem][]markdown.Span{}
		s.pinned = nil
		s.flows = nil
	}
	if s.shaper != shaper || s.metric != m || s.chrome != *chrome || len(s.flows) > maxCachedFlows {
		s.shaper, s.metric, s.chrome = shaper, m, *chrome
		s.flows = nil
	}
	if s.flows == nil {
		s.flows = map[flowKey]*flow{}
	}
}

// codeBlock returns the v0.9 CodeBlockState of one markdown code block.
func (s *State) codeBlock(b *markdown.Block) *CodeBlockState {
	if s.codeBlocks == nil {
		s.codeBlocks = map[*markdown.Block]*CodeBlockState{}
	}
	cb, ok := s.codeBlocks[b]
	if !ok {
		cb = NewCodeBlockState()
		s.codeBlocks[b] = cb
	}
	return cb
}

// chip returns the interaction state of one inline chip key.
func (s *State) chip(key string) *chipState {
	if s.chips == nil {
		s.chips = map[string]*chipState{}
	}
	cs, ok := s.chips[key]
	if !ok {
		cs = &chipState{}
		s.chips[key] = cs
	}
	return cs
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
