package main

import (
	"errors"
	"image"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	tvSelected = "selected"
	tvExpanded = "expanded"
	tvLoading  = "loading"
	tvError    = "error"
	tvLive     = "live"
	tvSlowKey  = "slow"
)

var errTreeSample = errors.New("permission denied")

func treeViewStates() []string {
	return []string{stRest, stHover, tvSelected, stFocus, stDisabled, tvExpanded, tvLoading, tvError}
}

type treeViewPage struct {
	specs [8]*kit.TreeViewState
	live  *kit.TreeViewState
	roots []kit.TreeNode
}

func newTreeViewPage() treeViewPage {
	var specs [8]*kit.TreeViewState
	for i := range specs {
		specs[i] = kit.NewTreeViewState()
	}
	specs[2].SetSelected("node")
	specs[5].SetExpanded("branch", true)
	specs[6].SetExpanded(tvSlowKey, true)
	specs[7].SetExpanded("err", true)

	live := kit.NewTreeViewState()
	live.SetSelected("src")
	live.SetExpanded("src", true)

	roots := []kit.TreeNode{
		{
			Key:         "src",
			Label:       "src",
			Icon:        kit.IconSearch,
			HasChildren: true,
			Children: []kit.TreeNode{
				{Key: "main.go", Label: "main.go", Trailing: "124 lines"},
				{Key: "view.go", Label: "view.go", Trailing: "231 lines"},
			},
		},
		{Key: "lazy", Label: "lazy-branch", HasChildren: true},
		{Key: tvSlowKey, Label: "slow-loader", HasChildren: true},
		{Key: "fail", Label: "error-branch", HasChildren: true},
		{Key: "locked", Label: "locked-node", Disabled: true},
	}
	return treeViewPage{specs: specs, live: live, roots: roots}
}

func (p *pages) layoutTreeViews(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "TreeView §6.21: built on VirtualList and ListRow pieces; dense 24 dp rows, 12 dp indent, BorderSubtle guides, Snappy 90° chevron, Smooth open / Exit close, lazy async loader with 150 ms Spinner and error row.") + gtx.Dp(theme.SpaceXL)
	p.flow(gtx, y, []block{p.treeSpecimensBlock, p.treeLiveBlock})
}

func (p *pages) treeSpecimensBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceMD)
	rowW := gtx.Dp(unit.Dp(260))
	y := line + gap

	states := treeViewStates()
	for i, st := range states {
		tv := p.specsForTreeState(i, st)
		h := gtx.Dp(theme.RowDense)
		if st == tvExpanded || st == tvLoading || st == tvError {
			h = 2 * gtx.Dp(theme.RowDense)
		}
		if !dry {
			p.caption(gtx, image.Pt(0, y), st, c.FgSubtle)
			nodes, loader := treeSpecimenData(st)
			cd := cellDraw{
				key: cell{compTreeView, 0, 0, st},
				tag: tv.NodeTag(nodes[0].Key),
				box: image.Pt(rowW, h),
				lay: func(gtx *layout.Context) {
					g := *gtx
					g.Constraints = layout.Exact(image.Pt(rowW, h))
					tv.Layout(p.env, &g, kit.TreeViewProps{Roots: nodes, Loader: loader})
				},
			}
			p.place(gtx, image.Pt(0, y+line+gtx.Dp(theme.SpaceXS)), &cd)
		}
		y += line + gtx.Dp(theme.SpaceXS) + h + gap
	}
	if !dry {
		p.caption(gtx, image.Point{}, "states", c.FgMuted)
	}
	return image.Pt(rowW, y)
}

func (p *pages) specsForTreeState(i int, _ string) *kit.TreeViewState {
	return p.tree.specs[i]
}

func treeSpecimenData(st string) ([]kit.TreeNode, kit.TreeLoader) {
	switch st {
	case tvExpanded:
		return []kit.TreeNode{{
			Key:         "branch",
			Label:       "branch",
			HasChildren: true,
			Children:    []kit.TreeNode{{Key: "child", Label: "child.go"}},
		}}, nil
	case tvLoading:
		return []kit.TreeNode{{Key: tvSlowKey, Label: "loading-branch", HasChildren: true}}, func(string, func([]kit.TreeNode, error)) {}
	case tvError:
		return []kit.TreeNode{{Key: "err", Label: "error-branch", HasChildren: true}}, func(_ string, done func([]kit.TreeNode, error)) {
			done(nil, errTreeSample)
		}
	case stDisabled:
		return []kit.TreeNode{{Key: "node", Label: "disabled-node", Disabled: true}}, nil
	default:
		return []kit.TreeNode{{Key: "node", Label: "node-" + st, HasChildren: true}}, nil
	}
}

func (p *pages) treeLiveBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceMD)
	rowW := gtx.Dp(unit.Dp(320))
	totalH := gtx.Dp(unit.Dp(240))
	y := line + gap
	if !dry {
		p.caption(gtx, image.Point{}, "interactive tree (lazy, slow loader, error branch)", c.FgMuted)
		cd := cellDraw{
			key: cell{compTreeView, 1, 0, tvLive},
			box: image.Pt(rowW, totalH),
			lay: func(gtx *layout.Context) {
				g := *gtx
				g.Constraints = layout.Exact(image.Pt(rowW, totalH))
				p.tree.live.Layout(p.env, &g, kit.TreeViewProps{
					Roots:  p.tree.roots,
					Loader: galleryTreeLoader,
				})
			},
		}
		p.place(gtx, image.Pt(0, y), &cd)
	}
	return image.Pt(rowW, y+totalH)
}

func galleryTreeLoader(nodeKey string, done func([]kit.TreeNode, error)) {
	switch nodeKey {
	case "lazy":
		done([]kit.TreeNode{
			{Key: "lazy-1", Label: "loaded-a.go", Trailing: "18 lines"},
			{Key: "lazy-2", Label: "loaded-b.go", Trailing: "42 lines"},
		}, nil)
	case tvSlowKey:
		time.AfterFunc(400*time.Millisecond, func() {
			done([]kit.TreeNode{
				{Key: "slow-1", Label: "slow-result.go"},
			}, nil)
		})
	case "fail":
		done(nil, errTreeSample)
	}
}
