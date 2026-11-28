package kit

import (
	"image"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// SuggestList is the floating suggestion list used by Combobox (§6.14) and
// the composer's `/` and `@` menus (F10.4b). It draws on the window overlay
// (UK-07) with the Menu panel look (§6.15), anchors to any caller-supplied
// rect and flips at the window edge, never takes keyboard focus (the caller
// keeps focus and forwards navigation/choice keys), and renders loading
// (Spinner after 150 ms, §6.5), empty, and no-results rows.

const (
	suggestSpinnerDelay = 150 * time.Millisecond
	suggestPageStep     = 5
)

// SuggestItem is one row in a SuggestList.
type SuggestItem struct {
	ID        string
	Label     string
	Secondary string
	Icon      IconName
	Shortcut  Shortcut
	Disabled  bool
}

// SuggestProps are the per-frame, caller-owned properties (UK-02).
type SuggestProps struct {
	Items     []SuggestItem
	Anchor    image.Rectangle
	Query     string
	Loading   bool
	EmptyText string
	NoResText string
	MinWidth  int
	// FieldAlign aligns a Combobox's list (§6.14) flush with the field box and
	// its item text with the field's value text.
	FieldAlign bool
	Inline     bool
}

// SuggestResult reports what happened in the frame (UK-06).
type SuggestResult struct {
	Chosen string
	Closed bool
	Size   image.Point
}

type suggestStatus int

const (
	suggestItems suggestStatus = iota
	suggestLoading
	suggestEmpty
	suggestNoResults
)

// SuggestListState is the long-lived state of one suggestion list (UK-02).
type SuggestListState struct {
	open         motion.Presence
	hiY, hiA     motion.Follower
	hi           int
	hiYv, hiAv   float32
	rect         image.Rectangle
	from, dir    image.Point
	pad          int
	itemInset    int
	rows         []menuRow
	cols         menuCols
	items        []SuggestItem
	status       suggestStatus
	spinner      Spinner
	loadingSince time.Time
	origin       image.Point
	userMoved    bool
}

// NewSuggestListState builds the state of one suggestion list.
func NewSuggestListState() *SuggestListState {
	return &SuggestListState{
		open: motion.NewPresence(motion.Snappy(), motion.Exit()),
		hiY:  motion.NewFollower(motion.Snappy()),
		hiA:  motion.NewFollower(motion.Snappy()),
		hi:   -1,
	}
}

// Open opens the suggestion list on Snappy and highlights the first enabled
// item (or -1 if none) when transitioning from closed.
func (s *SuggestListState) Open() {
	if !s.open.Shown() {
		s.hi = 0
		s.userMoved = false
	}
	s.open.Show(true)
}

// Close closes the suggestion list on Exit.
func (s *SuggestListState) Close() {
	s.open.Show(false)
	s.loadingSince = time.Time{}
	s.userMoved = false
}

// IsOpen reports whether the suggestion list was last asked to be open.
func (s *SuggestListState) IsOpen() bool { return s.open.Shown() }

// Highlight returns the currently highlighted item index, or -1 if none.
func (s *SuggestListState) Highlight() int { return s.hi }

// SetHighlight sets the highlighted item index (-1 clears it).
func (s *SuggestListState) SetHighlight(idx int) { s.hi = idx }

// Rect returns the panel's last laid-out bounds in window coordinates.
func (s *SuggestListState) Rect() image.Rectangle {
	if !s.open.Visible() {
		return image.Rectangle{}
	}
	return s.rect.Add(s.origin)
}

// HandleKey processes a key event forwarded by the focused caller. It never
// takes focus itself.
func (s *SuggestListState) HandleKey(ke key.Event, items []SuggestItem) (chosen string, closed, handled bool) {
	if !s.open.Shown() || ke.State != key.Press {
		return "", false, false
	}
	if s.handleNavKey(ke.Name, items) {
		return "", false, true
	}
	return s.handleActionKey(ke.Name, items)
}

func (s *SuggestListState) handleNavKey(name key.Name, items []SuggestItem) bool {
	switch string(name) {
	case string(key.NameUpArrow):
		s.stepHighlight(items, -1)
	case string(key.NameDownArrow):
		s.stepHighlight(items, 1)
	case string(key.NameHome):
		s.hi = suggestEdgeEnabled(items, true)
	case string(key.NameEnd):
		s.hi = suggestEdgeEnabled(items, false)
	case string(key.NamePageUp):
		s.pageHighlight(items, -suggestPageStep)
	case string(key.NamePageDown):
		s.pageHighlight(items, suggestPageStep)
	default:
		return false
	}
	s.userMoved = true
	return true
}

func (s *SuggestListState) handleActionKey(name key.Name, items []SuggestItem) (chosen string, closed, handled bool) {
	switch string(name) {
	case string(key.NameReturn), string(key.NameEnter):
		if s.hi >= 0 && s.hi < len(items) && !items[s.hi].Disabled {
			id := items[s.hi].ID
			s.Close()
			return id, true, true
		}
		return "", false, false
	case string(key.NameTab):
		if s.userMoved && s.hi >= 0 && s.hi < len(items) && !items[s.hi].Disabled {
			id := items[s.hi].ID
			s.Close()
			return id, true, true
		}
		return "", false, false
	case string(key.NameEscape):
		s.Close()
		return "", true, true
	}
	return "", false, false
}

// Layout runs one frame of the suggestion list: pointer input, loading timer,
// motion, placement and drawing.
func (s *SuggestListState) Layout(env *Env, gtx *layout.Context, p *SuggestProps) (SuggestResult, error) {
	var res SuggestResult
	if !p.Inline && !s.open.Visible() {
		return res, nil
	}
	if !p.Inline {
		s.pointerInput(gtx, &res)
	}
	s.resolve(env, gtx, p)
	s.advance(env, p.Inline)
	if !p.Inline && !s.open.Visible() {
		return res, nil
	}
	if err := s.place(env, gtx, p); err != nil {
		return res, err
	}
	res.Size = s.rect.Size()
	if err := s.draw(env, gtx, p); err != nil {
		return res, err
	}
	return res, nil
}

func (s *SuggestListState) pointerInput(gtx *layout.Context, res *SuggestResult) {
	if !s.open.Shown() {
		return
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target: s,
			Kinds:  pointer.Move | pointer.Enter | pointer.Leave | pointer.Press,
		})
		if !ok {
			return
		}
		pe, is := ev.(pointer.Event)
		if !is {
			continue
		}
		s.handlePointerEvent(pe, res)
	}
}

func (s *SuggestListState) handlePointerEvent(pe pointer.Event, res *SuggestResult) {
	at := image.Pt(int(pe.Position.X), int(pe.Position.Y))
	switch pe.Kind {
	case pointer.Move, pointer.Enter:
		s.hoverAt(at)
	case pointer.Leave:
		s.hi = -1
	case pointer.Press:
		s.pressAt(at, res)
	case pointer.Release, pointer.Cancel, pointer.Drag, pointer.Scroll:
	}
}

func (s *SuggestListState) hoverAt(at image.Point) {
	if s.status != suggestItems {
		s.hi = -1
		return
	}
	idx := s.itemAt(at)
	if idx >= 0 && idx < len(s.items) && !s.items[idx].Disabled {
		s.hi = idx
		return
	}
	s.hi = -1
}

func (s *SuggestListState) pressAt(at image.Point, res *SuggestResult) {
	if s.status != suggestItems {
		return
	}
	idx := s.itemAt(at)
	if idx >= 0 && idx < len(s.items) && !s.items[idx].Disabled {
		res.Chosen = s.items[idx].ID
		res.Closed = true
		s.Close()
	}
}

func (s *SuggestListState) resolve(env *Env, gtx *layout.Context, p *SuggestProps) {
	if !p.Inline && !s.open.Shown() {
		return
	}
	s.items = p.Items
	if len(p.Items) > 0 {
		s.status = suggestItems
		s.loadingSince = time.Time{}
		if s.hi >= len(p.Items) || (s.hi >= 0 && p.Items[s.hi].Disabled) {
			s.hi = suggestEdgeEnabled(p.Items, true)
		}
		return
	}
	s.hi = -1
	s.resolveEmptyStatus(env, gtx, p)
}

func (s *SuggestListState) resolveEmptyStatus(env *Env, gtx *layout.Context, p *SuggestProps) {
	if !p.Loading {
		s.loadingSince = time.Time{}
		if p.Query != "" {
			s.status = suggestNoResults
			return
		}
		s.status = suggestEmpty
		return
	}
	s.status = suggestLoading
	if p.Inline {
		return
	}
	if s.loadingSince.IsZero() {
		s.loadingSince = gtx.Now
	}
	if wake := s.loadingSince.Add(suggestSpinnerDelay); gtx.Now.Before(wake) {
		env.Driver.WakeAt(wake)
	}
}

func (s *SuggestListState) advance(env *Env, inline bool) {
	if inline {
		return
	}
	if env.ReducedMotion {
		s.open.Snap(s.open.Shown())
	} else if s.open.Advance(env.DT()) {
		env.Keep()
	}
}

func (s *SuggestListState) spinnerVisible(gtx *layout.Context, inline bool) bool {
	if s.status != suggestLoading {
		return false
	}
	if inline {
		return true
	}
	return !s.loadingSince.IsZero() && !gtx.Now.Before(s.loadingSince.Add(suggestSpinnerDelay))
}

func (s *SuggestListState) place(env *Env, gtx *layout.Context, p *SuggestProps) error {
	s.origin = env.origin
	if p.Inline || s.open.Shown() {
		size, err := s.layoutRows(env, gtx, p)
		if err != nil {
			return err
		}
		if p.Inline {
			s.setRect(image.Point{}, size, -1)
		} else {
			win := env.windowRect()
			pos, up := placeBelow(p.Anchor, size, win, gtx.Dp(theme.SpaceXS))
			s.setRect(pos, size, dirOf(up))
		}
	}
	target := s.hiY.Value()
	if s.status == suggestItems && s.hi >= 0 && s.hi < len(s.rows) {
		target = float32(s.rows[s.hi].y)
	}
	s.hiYv = followTo(env, &s.hiY, target)
	s.hiAv = followState(env, &s.hiA, s.status == suggestItems && s.hi >= 0)
	return nil
}

func (s *SuggestListState) setRect(pos, size image.Point, dirY int) {
	s.rect = image.Rectangle{Min: pos, Max: pos.Add(size)}
	s.dir = image.Pt(0, dirY)
	s.from = image.Pt(pos.X+size.X/2, pos.Y+(dirY+1)/2*size.Y)
}

func (s *SuggestListState) layoutRows(env *Env, gtx *layout.Context, p *SuggestProps) (image.Point, error) {
	s.pad = gtx.Dp(theme.SpaceSM)
	s.rows = s.rows[:0]
	minW, insetX := suggestSizing(gtx, p)
	s.itemInset = insetX
	w := max(minW-2*s.pad, 0)
	rowH := gtx.Dp(theme.Row)

	if s.status != suggestItems {
		s.cols = menuCols{}
		txt := s.statusLabel(p)
		tw := 2*insetX + measureLabel(env, gtx, theme.TypeSmall(), txt).X
		if s.status == suggestLoading || p.FieldAlign {
			tw += gtx.Dp(theme.IconMD) + gtx.Dp(theme.SpaceSM)
		}
		w = max(w, tw)
		s.rows = append(s.rows, menuRow{y: 0, h: rowH})
		return image.Pt(w+2*s.pad, rowH+2*s.pad), nil
	}

	cols, err := suggestColumns(env, gtx, s.items)
	if err != nil {
		return image.Point{}, err
	}
	if p.FieldAlign {
		// Reserve the leading icon column so item text aligns with TextFieldSearch value text.
		cols.icons = true
	}
	s.cols = cols
	y := 0
	for i := range s.items {
		w = max(w, suggestItemWidth(env, gtx, &s.items[i], cols, insetX))
		s.rows = append(s.rows, menuRow{y: y, h: rowH})
		y += rowH
	}
	return image.Pt(w+2*s.pad, y+2*s.pad), nil
}

func suggestSizing(gtx *layout.Context, p *SuggestProps) (minW, insetX int) {
	if p.FieldAlign {
		minW = max(p.MinWidth, p.Anchor.Dx())
		if minW <= 0 {
			minW = gtx.Dp(theme.MenuMinWidth)
		}
		return minW, gtx.Dp(theme.SpaceSM)
	}
	return max(p.MinWidth, max(p.Anchor.Dx(), gtx.Dp(theme.MenuMinWidth))), gtx.Dp(theme.SpaceMD)
}

func (s *SuggestListState) statusLabel(p *SuggestProps) string {
	switch s.status {
	case suggestLoading:
		return "Loading…"
	case suggestNoResults:
		if p.NoResText != "" {
			return p.NoResText
		}
		return "No results"
	case suggestEmpty:
		if p.EmptyText != "" {
			return p.EmptyText
		}
		return "No items"
	case suggestItems:
		return ""
	}
	return ""
}

func (s *SuggestListState) draw(env *Env, gtx *layout.Context, p *SuggestProps) error {
	inline := p.Inline
	macro := op.Record(gtx.Ops)
	err := s.drawBody(env, gtx, p)
	call := macro.Stop()
	if inline {
		call.Add(gtx.Ops)
	} else {
		op.Defer(gtx.Ops, call)
	}
	return err
}

func (s *SuggestListState) drawBody(env *Env, gtx *layout.Context, p *SuggestProps) error {
	inline := p.Inline
	if !inline && s.open.Shown() {
		area := clip.Rect(s.rect).Push(gtx.Ops)
		event.Op(gtx.Ops, s)
		area.Pop()
	}
	prog := float32(1)
	if !inline {
		prog = s.open.Progress()
	}
	shift := s.dir.Mul(int(float32(gtx.Dp(theme.SpaceXS)) * (1 - prog)))
	defer op.Offset(shift).Push(gtx.Ops).Pop()
	defer scaleAbout(gtx, f32.Pt(float32(s.from.X), float32(s.from.Y)), 0.96+0.04*prog).Pop()
	defer paint.PushOpacity(gtx.Ops, unit01(prog)).Pop()

	inner := drawMenuPanelSurface(env, gtx, s.rect, s.pad, s.hiYv, s.hiAv)
	if s.status != suggestItems {
		s.drawStatusRow(env, gtx, inner, p)
		return nil
	}
	for j := range s.items {
		row := image.Rect(inner.Min.X, inner.Min.Y+s.rows[j].y, inner.Max.X, inner.Min.Y+s.rows[j].y+s.rows[j].h)
		if err := drawSuggestItem(env, gtx, &s.items[j], row, s.cols, s.itemInset); err != nil {
			return err
		}
	}
	return nil
}
