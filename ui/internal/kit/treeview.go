package kit

import (
	"image"
	"sync"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	treeSpinnerDelay  = 150 * time.Millisecond
	treeDoubleClick   = 400 * time.Millisecond
	treeLoadingPrefix = "__tree_loading__:"
	treeErrorPrefix   = "__tree_error__:"
)

// TreeNode describes one node in a TreeView (§6.21).
type TreeNode struct {
	Key         string
	Label       string
	Icon        IconName
	Trailing    string
	StateLabel  string
	StateTone   ListRowTone
	HasChildren bool
	Children    []TreeNode
	Disabled    bool
}

// TreeLoader loads children for an opened branch asynchronously and invokes
// done with the loaded children or an error (§6.21).
type TreeLoader func(nodeKey string, done func(children []TreeNode, err error))

// TreeViewProps configures a TreeView for the current frame (§6.21).
type TreeViewProps struct {
	Roots        []TreeNode
	Loader       TreeLoader
	FollowBottom bool
}

// TreeViewEvents reports TreeView selection, expansion, and open events in the
// current frame.
type TreeViewEvents struct {
	Selected string
	Opened   string
	Toggled  string
	Expanded bool
	Changed  bool
	Err      error
}

type treeLoadResult struct {
	key      string
	children []TreeNode
	err      error
}

type treeNodeState struct {
	tag       bool
	chevTag   bool
	hovered   bool
	chevHover bool
	pressed   bool
	hoverF    motion.Follower
	focusF    motion.Follower
	chevF     motion.Follower
	presence  motion.Presence
	expanded  bool
	loading   bool
	loadSince time.Time
	loaded    bool
	children  []TreeNode
	loadErr   error
	spinner   Spinner
}

func newTreeNodeState() *treeNodeState {
	return &treeNodeState{
		hoverF:   motion.NewFollower(motion.Snappy()),
		focusF:   motion.NewFollower(motion.Snappy()),
		chevF:    motion.NewFollower(motion.Snappy()),
		presence: motion.NewPresence(motion.Smooth(), motion.Exit()),
	}
}

type flatTreeRow struct {
	key         string
	parentKey   string
	firstChild  string
	depth       int
	node        TreeNode
	hasChildren bool
	isLoading   bool
	isError     bool
	errText     string
	alpha       float32
}

// TreeViewState holds the virtualized list state, keyed selection, keyed
// branch expansion/loading state, and sliding selection pill of a TreeView
// (§6.21).
type TreeViewState struct {
	list       *VirtualListState
	nodes      map[string]*treeNodeState
	pill       slidingPill
	selected   string
	lastClickK string
	lastClickT time.Time

	mu      sync.Mutex
	pending []treeLoadResult
}

// NewTreeViewState builds a TreeViewState with no node selected.
func NewTreeViewState() *TreeViewState {
	return &TreeViewState{
		list:  NewVirtualListState(),
		nodes: map[string]*treeNodeState{},
		pill:  newSlidingPill(),
	}
}

// List returns the underlying VirtualListState.
func (s *TreeViewState) List() *VirtualListState { return s.list }

// Selected returns the currently selected node key, or "".
func (s *TreeViewState) Selected() string { return s.selected }

// SetSelected updates the selected node key.
func (s *TreeViewState) SetSelected(nodeKey string) { s.selected = nodeKey }

// Expanded reports whether nodeKey is currently expanded.
func (s *TreeViewState) Expanded(nodeKey string) bool {
	if ns, ok := s.nodes[nodeKey]; ok {
		return ns.expanded
	}
	return false
}

// SetExpanded sets the expansion state of nodeKey.
func (s *TreeViewState) SetExpanded(nodeKey string, open bool) {
	ns := s.node(nodeKey)
	ns.expanded = open
	ns.presence.Show(open)
}

// NodeTag returns the row event tag for nodeKey.
func (s *TreeViewState) NodeTag(nodeKey string) event.Tag { return &s.node(nodeKey).tag }

// ChevronTag returns the chevron event tag for nodeKey.
func (s *TreeViewState) ChevronTag(nodeKey string) event.Tag { return &s.node(nodeKey).chevTag }

func (s *TreeViewState) node(nodeKey string) *treeNodeState {
	ns, ok := s.nodes[nodeKey]
	if !ok {
		ns = newTreeNodeState()
		s.nodes[nodeKey] = ns
	}
	return ns
}

func (s *TreeViewState) drainPending() {
	s.mu.Lock()
	res := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, r := range res {
		ns := s.node(r.key)
		ns.loading = false
		if r.err != nil {
			ns.loadErr = r.err
			ns.loaded = false
			continue
		}
		ns.loadErr = nil
		ns.loaded = true
		ns.children = r.children
	}
}

// Layout lays out the virtualized TreeView inside gtx (§6.21).
func (s *TreeViewState) Layout(env *Env, gtx *layout.Context, p TreeViewProps) (layout.Dimensions, TreeViewEvents) {
	s.drainPending()
	ev := TreeViewEvents{Selected: s.selected}

	flat := s.flatten(env, p.Roots, p.Loader)
	s.pollTreeKeys(env, gtx, flat, p.Loader, &ev)
	if ev.Toggled != "" {
		flat = s.flatten(env, p.Roots, p.Loader)
	}

	items := make([]VirtualItem, len(flat))
	for i := range flat {
		r := &flat[i]
		est := theme.RowDense
		if r.alpha < 1 {
			est = theme.RowDense * unit.Dp(r.alpha)
			if est < 1 {
				est = 1
			}
		}
		items[i] = VirtualItem{Key: r.key, Estimate: est}
	}

	var selW, selH int
	var hasSel bool
	m := op.Record(gtx.Ops)
	dims := s.list.Layout(env, gtx, VirtualListProps{FollowBottom: p.FollowBottom}, items, func(env *Env, gtx *layout.Context, idx int, _ VirtualItem, _ *VirtualItemState) layout.Dimensions {
		d, rowSel := s.layoutTreeRow(env, gtx, &flat[idx], p.Loader, &ev)
		if rowSel {
			selW, selH = d.Size.X, d.Size.Y
			hasSel = true
		}
		return d
	})
	call := m.Stop()

	var selRect image.Rectangle
	if hasSel {
		selRect = s.selectedRect(gtx, flat, selW, selH)
	}
	s.pill.step(env, selRect, hasSel)
	s.pill.draw(gtx, gtx.Dp(theme.RadiusMD), env.Th.Chrome().SelectedWash)
	call.Add(gtx.Ops)
	return dims, ev
}

func (s *TreeViewState) selectedRect(gtx *layout.Context, flat []flatTreeRow, w, h int) image.Rectangle {
	y := -s.list.Offset()
	for i := range flat {
		r := &flat[i]
		if r.key == s.selected {
			return pillRect(gtx, image.Rect(0, y, w, y+h))
		}
		y += rowHeight(gtx.Dp(theme.RowDense), r.alpha)
	}
	return image.Rectangle{}
}

func (s *TreeViewState) flatten(env *Env, roots []TreeNode, loader TreeLoader) []flatTreeRow {
	var out []flatTreeRow
	s.flattenBranch(env, roots, "", 0, 1, loader, &out)
	return out
}

func (s *TreeViewState) flattenBranch(env *Env, nodes []TreeNode, parentKey string, depth int, parentAlpha float32, loader TreeLoader, out *[]flatTreeRow) {
	for i := range nodes {
		s.flattenNode(env, &nodes[i], parentKey, depth, parentAlpha, loader, out)
	}
}

func (s *TreeViewState) flattenNode(env *Env, n *TreeNode, parentKey string, depth int, parentAlpha float32, loader TreeLoader, out *[]flatTreeRow) {
	ns := s.node(n.Key)
	hasKids := n.HasChildren || len(n.Children) > 0
	if ns.expanded && !ns.presence.Visible() && !ns.presence.Shown() {
		ns.presence.Show(true)
	}
	pres := stepPresence(env, &ns.presence)
	children, firstChild := s.resolveNodeChildren(env, n, ns, loader)
	*out = append(*out, flatTreeRow{
		key:         n.Key,
		parentKey:   parentKey,
		firstChild:  firstChild,
		depth:       depth,
		node:        *n,
		hasChildren: hasKids,
		alpha:       parentAlpha,
	})
	if hasKids && pres > 0 {
		s.appendSubBranch(env, n, ns, children, depth, parentAlpha*pres, loader, out)
	}
}

func (s *TreeViewState) resolveNodeChildren(env *Env, n *TreeNode, ns *treeNodeState, loader TreeLoader) (children []TreeNode, firstChild string) {
	if ns.expanded && n.HasChildren && len(n.Children) == 0 && !ns.loaded && !ns.loading && ns.loadErr == nil && loader != nil {
		s.startBranchLoad(env, n.Key, ns, loader)
		s.drainPending()
	}
	children = n.Children
	if len(children) == 0 && ns.loaded {
		children = ns.children
	}
	if len(children) > 0 && ns.expanded {
		firstChild = children[0].Key
	}
	return children, firstChild
}

func (s *TreeViewState) appendSubBranch(env *Env, n *TreeNode, ns *treeNodeState, children []TreeNode, depth int, subAlpha float32, loader TreeLoader, out *[]flatTreeRow) {
	if ns.loading {
		if env.Now().Before(ns.loadSince.Add(treeSpinnerDelay)) {
			env.Driver.WakeAt(ns.loadSince.Add(treeSpinnerDelay))
			return
		}
		*out = append(*out, flatTreeRow{
			key:       treeLoadingPrefix + n.Key,
			parentKey: n.Key,
			depth:     depth + 1,
			isLoading: true,
			alpha:     subAlpha,
		})
		return
	}
	if ns.loadErr != nil {
		*out = append(*out, flatTreeRow{
			key:       treeErrorPrefix + n.Key,
			parentKey: n.Key,
			depth:     depth + 1,
			isError:   true,
			errText:   ns.loadErr.Error(),
			alpha:     subAlpha,
		})
		return
	}
	if len(children) > 0 {
		s.flattenBranch(env, children, n.Key, depth+1, subAlpha, loader, out)
	}
}

func (s *TreeViewState) startBranchLoad(env *Env, nodeKey string, ns *treeNodeState, loader TreeLoader) {
	ns.loading = true
	ns.loadErr = nil
	ns.loadSince = env.Now()
	env.Driver.WakeAt(ns.loadSince.Add(treeSpinnerDelay))
	invalidate := env.Invalidate
	loader(nodeKey, func(children []TreeNode, err error) {
		s.mu.Lock()
		s.pending = append(s.pending, treeLoadResult{key: nodeKey, children: children, err: err})
		s.mu.Unlock()
		if invalidate != nil {
			invalidate()
		}
	})
}

func (s *TreeViewState) toggleBranch(env *Env, node *TreeNode, loader TreeLoader, open bool, ev *TreeViewEvents) {
	ns := s.node(node.Key)
	if ns.expanded == open {
		return
	}
	ns.expanded = open
	ns.presence.Show(open)
	ev.Toggled = node.Key
	ev.Expanded = open
	if open && node.HasChildren && len(node.Children) == 0 && !ns.loaded && !ns.loading && loader != nil {
		s.startBranchLoad(env, node.Key, ns, loader)
	}
}

func (s *TreeViewState) pollTreeKeys(env *Env, gtx *layout.Context, flat []flatTreeRow, loader TreeLoader, ev *TreeViewEvents) {
	for i := range flat {
		r := &flat[i]
		if r.isLoading || r.isError || r.node.Disabled {
			continue
		}
		ns := s.node(r.key)
		if !gtx.Focused(&ns.tag) {
			continue
		}
		s.stepTreeKeys(env, gtx, flat, i, &ns.tag, loader, ev)
		return
	}
}

func (s *TreeViewState) stepTreeKeys(env *Env, gtx *layout.Context, flat []flatTreeRow, idx int, tag *bool, loader TreeLoader, ev *TreeViewEvents) {
	for {
		ke, ok := gtx.Event(
			key.Filter{Focus: tag, Name: key.NameUpArrow},
			key.Filter{Focus: tag, Name: key.NameDownArrow},
			key.Filter{Focus: tag, Name: key.NameRightArrow},
			key.Filter{Focus: tag, Name: key.NameLeftArrow},
			key.Filter{Focus: tag, Name: key.NameReturn},
			key.Filter{Focus: tag, Name: key.NameEnter},
		)
		if !ok {
			return
		}
		k, isKey := ke.(key.Event)
		if !isKey || k.State != key.Press {
			continue
		}
		s.handleTreeKey(env, gtx, flat, idx, k.Name, loader, ev)
	}
}

func (s *TreeViewState) handleTreeKey(env *Env, gtx *layout.Context, flat []flatTreeRow, idx int, name key.Name, loader TreeLoader, ev *TreeViewEvents) {
	r := &flat[idx]
	if name == key.NameReturn || name == key.NameEnter {
		ev.Opened = r.key
		return
	}
	if name == key.NameUpArrow {
		if next := nextSelectableTreeRow(flat, idx, -1); next != idx {
			s.selectAndFocus(gtx, flat[next].key, ev)
		}
		return
	}
	if name == key.NameDownArrow {
		if next := nextSelectableTreeRow(flat, idx, 1); next != idx {
			s.selectAndFocus(gtx, flat[next].key, ev)
		}
		return
	}
	if name == key.NameRightArrow {
		s.handleRightKey(env, gtx, r, loader, ev)
		return
	}
	if name == key.NameLeftArrow {
		s.handleLeftKey(env, gtx, r, loader, ev)
	}
}

func (s *TreeViewState) handleRightKey(env *Env, gtx *layout.Context, r *flatTreeRow, loader TreeLoader, ev *TreeViewEvents) {
	if !r.hasChildren {
		return
	}
	ns := s.node(r.key)
	if !ns.expanded {
		s.toggleBranch(env, &r.node, loader, true, ev)
		return
	}
	if r.firstChild != "" {
		s.selectAndFocus(gtx, r.firstChild, ev)
	}
}

func (s *TreeViewState) handleLeftKey(env *Env, gtx *layout.Context, r *flatTreeRow, loader TreeLoader, ev *TreeViewEvents) {
	ns := s.node(r.key)
	if r.hasChildren && ns.expanded {
		s.toggleBranch(env, &r.node, loader, false, ev)
		return
	}
	if r.parentKey != "" {
		s.selectAndFocus(gtx, r.parentKey, ev)
	}
}

func (s *TreeViewState) selectAndFocus(gtx *layout.Context, nodeKey string, ev *TreeViewEvents) {
	if nodeKey != s.selected {
		s.selected = nodeKey
		ev.Selected = nodeKey
		ev.Changed = true
	}
	s.list.EnsureVisible(nodeKey)
	gtx.Execute(key.FocusCmd{Tag: &s.node(nodeKey).tag})
}

func nextSelectableTreeRow(flat []flatTreeRow, from, dir int) int {
	for i := from + dir; i >= 0 && i < len(flat); i += dir {
		r := flat[i]
		if !r.isLoading && !r.isError && !r.node.Disabled {
			return i
		}
	}
	return from
}
