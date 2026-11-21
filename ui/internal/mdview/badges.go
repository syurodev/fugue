package mdview

import (
	"image"
	"image/color"
	"io"
	"strconv"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// BadgeKind classifies a shared file, folder, or deleted-file badge.
type BadgeKind uint8

const (
	// BadgeFile is a clickable source/document file badge.
	BadgeFile BadgeKind = iota
	// BadgeFolder is a clickable directory badge.
	BadgeFolder
	// BadgeDeleted is a non-clickable, muted, struck-through deleted file badge.
	BadgeDeleted
)

// BadgeSpec describes one unified file or folder badge rendered identically in
// prose, tool rows, listed files, and search results.
type BadgeSpec struct {
	Kind BadgeKind
	File FileRef
}

// GitRefKind identifies a git reference chip (commit, PR, or branch).
type GitRefKind uint8

const (
	// GitCommit is a monospace dotted-underlined commit hash with a commit icon.
	GitCommit GitRefKind = iota + 1
	// GitPR is a pill badge with a PR icon (#123, owner/repo#123, or PR URL).
	GitPR
	// GitBranch is a pill badge with a branch icon.
	GitBranch
)

// GitRef carries the known metadata for one commit, PR, or branch chip.
type GitRef struct {
	Kind    GitRefKind
	Text    string
	Hash    string
	Subject string
	URL     string
}

// GitRefAction handles activation of one GitRef kind.
type GitRefAction func(gtx *layout.Context, ref GitRef)

// GitRefRegistry maps each GitRefKind to its click handler.
type GitRefRegistry map[GitRefKind]GitRefAction

// DefaultGitRefRegistry returns stage-A git-ref actions: commit copies full
// hash, PR opens https URLs only, and branch copies its name.
func DefaultGitRefRegistry(openHTTPS func(url string)) GitRefRegistry {
	return GitRefRegistry{
		GitCommit: func(gtx *layout.Context, ref GitRef) {
			copyClipboard(gtx, firstNonEmptyStr(ref.Hash, ref.Text))
		},
		GitPR: func(_ *layout.Context, ref GitRef) {
			u := ref.URL
			if u == "" && strings.HasPrefix(ref.Text, "https://") {
				u = ref.Text
			}
			if openHTTPS != nil && strings.HasPrefix(u, "https://") {
				openHTTPS(u)
			}
		},
		GitBranch: func(gtx *layout.Context, ref GitRef) {
			copyClipboard(gtx, ref.Text)
		},
	}
}

func copyClipboard(gtx *layout.Context, txt string) {
	if gtx != nil && txt != "" {
		gtx.Execute(clipboard.WriteCmd{Type: clipboardMIME, Data: io.NopCloser(strings.NewReader(txt))})
	}
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// RunGitRefAction invokes the registered handler for ref.Kind.
func RunGitRefAction(gtx *layout.Context, reg GitRefRegistry, ref GitRef) bool {
	if reg == nil {
		return false
	}
	if fn := reg[ref.Kind]; fn != nil {
		fn(gtx, ref)
		return true
	}
	return false
}

// PaintBadgeChrome paints the shared pill container (radius.full, SurfaceBase,
// BorderSubtle hairline) for file badges, folder badges, and PR/branch badges.
func PaintBadgeChrome(th *theme.Theme, gtx *layout.Context, box image.Rectangle, deleted bool) {
	ch := th.Chrome()
	fillCol := ch.SurfaceBase
	if deleted {
		fillCol = ch.FillSoft
	}
	rad := box.Dy() / 2
	paint.FillShape(gtx.Ops, fillCol, clip.UniformRRect(box, rad).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, ch.BorderSubtle, clip.Stroke{
		Path: clip.UniformRRect(box, rad).Path(gtx.Ops), Width: float32(max(gtx.Dp(theme.Hairline), 1)),
	}.Op())
}

// DrawBadge renders a file, folder, or deleted badge using one shared spec.
func DrawBadge(env *kit.Env, gtx *layout.Context, click *widget.Clickable, spec BadgeSpec, onOpen func(FileRef)) layout.Dimensions {
	if spec.Kind != BadgeDeleted && click != nil {
		if click.Clicked(*gtx) && onOpen != nil {
			onOpen(spec.File)
		}
		return click.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return drawBadgeContent(env, &l, &spec)
		})
	}
	return drawBadgeContent(env, gtx, &spec)
}

func drawBadgeContent(env *kit.Env, gtx *layout.Context, spec *BadgeSpec) layout.Dimensions {
	ch := env.Th.Chrome()
	deleted := spec.Kind == BadgeDeleted
	pathText, lineSuffix := splitFileAndLine(&spec.File)
	icon := FileIcon(spec.File.Path)
	if spec.Kind == BadgeFolder {
		icon = kit.IconFolder
	}
	iconCol, pathCol := ch.IconMuted, ch.Fg
	if deleted {
		iconCol, pathCol = ch.FgDisabled, ch.FgDisabled
	}
	rec := op.Record(gtx.Ops)
	inner := *gtx
	inner.Constraints.Min = image.Point{}
	pad := layout.Inset{Left: theme.SpaceSM, Right: theme.SpaceSM, Top: theme.SpaceXXS, Bottom: theme.SpaceXXS}
	dims := pad.Layout(inner, func(gtx layout.Context) layout.Dimensions {
		return layoutBadgeRow(env, &gtx, icon, iconCol, pathText, pathCol, lineSuffix, ch.FgSubtle)
	})
	call := rec.Stop()
	PaintBadgeChrome(env.Th, gtx, image.Rectangle{Max: dims.Size}, deleted)
	call.Add(gtx.Ops)
	if deleted {
		thin := max(gtx.Dp(theme.Hairline), 1)
		y := dims.Size.Y / 2
		bar := image.Rect(gtx.Dp(theme.SpaceSM), y, max(dims.Size.X-gtx.Dp(theme.SpaceSM), 0), y+thin)
		paint.FillShape(gtx.Ops, ch.FgDisabled, clip.Rect(bar).Op())
	}
	return dims
}

func splitFileAndLine(ref *FileRef) (pathText, lineSuffix string) {
	pathText = firstNonEmptyStr(ref.Path, ref.Raw)
	if ref.Line <= 0 {
		return pathText, ""
	}
	if ref.EndLine > ref.Line {
		return pathText, ":" + strconv.Itoa(ref.Line) + "-" + strconv.Itoa(ref.EndLine)
	}
	return pathText, ":" + strconv.Itoa(ref.Line)
}

func layoutBadgeRow(
	env *kit.Env, gtx *layout.Context,
	icon kit.IconName, iconCol color.NRGBA,
	label string, labelCol color.NRGBA,
	subtle string, subtleCol color.NRGBA,
) layout.Dimensions {
	suffixW := 0
	if subtle != "" {
		suffixW = kit.MeasureLabel(env, gtx, theme.TypeCodeSmall(), subtle).X
	}
	labelMaxW := max(gtx.Constraints.Max.X-gtx.Dp(theme.IconXS)-gtx.Dp(theme.SpaceXS)-suffixW, 0)
	kids := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			d, err := env.Icon(&l, icon, theme.IconXS, iconCol)
			logMDViewErr(err)
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				l.Constraints.Max.X = min(l.Constraints.Max.X, labelMaxW)
				return kit.MiddleEllipsisLabel(env, &l, theme.TypeCodeSmall(), labelCol, label)
			})
		}),
	}
	if subtle != "" {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return kit.EllipsisLabel(env, &l, theme.TypeCodeSmall(), subtleCol, subtle)
		}))
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, kids...)
}

// DrawGitRefChip draws a commit, PR, or branch chip with hover card + click registry.
func DrawGitRefChip(
	env *kit.Env, gtx *layout.Context, click *widget.Clickable, ref GitRef, reg GitRefRegistry,
) layout.Dimensions {
	if click != nil && click.Clicked(*gtx) {
		RunGitRefAction(gtx, reg, ref)
	}
	draw := func(gtx layout.Context) layout.Dimensions {
		l := gtx
		if ref.Kind == GitCommit {
			return drawCommitRef(env, &l, ref)
		}
		return drawGitPillRef(env, &l, ref)
	}
	if click == nil {
		return draw(*gtx)
	}
	dims := click.Layout(*gtx, draw)
	if click.Hovered() {
		DrawGitRefHoverCard(env, gtx, image.Rectangle{Max: dims.Size}, ref)
	}
	return dims
}

func drawCommitRef(env *kit.Env, gtx *layout.Context, ref GitRef) layout.Dimensions {
	ch := env.Th.Chrome()
	dims := layoutBadgeRow(env, gtx, kit.IconGitCommit, ch.IconMuted, ref.Text, ch.Fg, "", ch.FgSubtle)
	thin := max(gtx.Dp(theme.Hairline), 1)
	y := max(dims.Size.Y-thin, 0)
	for dx := 0; dx < dims.Size.X; dx += 3 * thin {
		paint.FillShape(gtx.Ops, ch.FgMuted, clip.Rect(image.Rect(dx, y, min(dx+thin, dims.Size.X), y+thin)).Op())
	}
	return dims
}

func drawGitPillRef(env *kit.Env, gtx *layout.Context, ref GitRef) layout.Dimensions {
	ch := env.Th.Chrome()
	icon := kit.IconGitBranch
	if ref.Kind == GitPR {
		icon = kit.IconGitPullRequest
	}
	rec := op.Record(gtx.Ops)
	inner := *gtx
	inner.Constraints.Min = image.Point{}
	pad := layout.Inset{Left: theme.SpaceSM, Right: theme.SpaceSM, Top: theme.SpaceXXS, Bottom: theme.SpaceXXS}
	dims := pad.Layout(inner, func(gtx layout.Context) layout.Dimensions {
		return layoutBadgeRow(env, &gtx, icon, ch.IconMuted, ref.Text, ch.Fg, "", ch.FgSubtle)
	})
	call := rec.Stop()
	PaintBadgeChrome(env.Th, gtx, image.Rectangle{Max: dims.Size}, false)
	call.Add(gtx.Ops)
	return dims
}

// DrawGitRefHoverCard defers a floating git-ref summary card showing item data.
func DrawGitRefHoverCard(env *kit.Env, gtx *layout.Context, anchor image.Rectangle, ref GitRef) {
	ch := env.Th.Chrome()
	title := firstNonEmptyStr(ref.Hash, ref.Text)
	detail := firstNonEmptyStr(ref.Subject, ref.URL)
	rec := op.Record(gtx.Ops)
	card := *gtx
	card.Constraints.Min = image.Point{}
	card.Constraints.Max.X = gtx.Dp(popoverWidth)
	body := op.Record(gtx.Ops)
	dims := layout.UniformInset(theme.SpaceMD).Layout(card, func(gtx layout.Context) layout.Dimensions {
		l1 := gtx
		d1 := kit.Label(env, &l1, theme.TypeCodeSmall(), ch.Fg, title)
		if detail == "" {
			return d1
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return d1 }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l2 := gtx
					return kit.Label(env, &l2, theme.TypeSmall(), ch.FgMuted, detail)
				})
			}),
		)
	})
	bodyCall := body.Stop()
	box, rad := image.Rectangle{Max: dims.Size}, gtx.Dp(theme.RadiusMD)
	off := op.Offset(image.Pt(anchor.Min.X, anchor.Max.Y+gtx.Dp(theme.SpaceXS))).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, ch.SurfaceRaised, clip.UniformRRect(box, rad).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, ch.Border, clip.Stroke{
		Path: clip.UniformRRect(box, rad).Path(gtx.Ops), Width: float32(max(gtx.Dp(theme.Hairline), 1)),
	}.Op())
	bodyCall.Add(gtx.Ops)
	off.Pop()
	op.Defer(gtx.Ops, rec.Stop())
}
