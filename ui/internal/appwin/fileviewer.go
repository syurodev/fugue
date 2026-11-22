package appwin

import (
	"errors"
	"fmt"
	"image"
	"io"
	"path"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/kit/icondata"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The read-only file, diff and folder viewer (F10.4e, desktop-app v0.10
// §5.4, Q-179 A). Until the slot grid (F10.20b) it occupies the right pane
// like the inbox and accounts. Every read goes through the host's confined
// ReadWorkspaceFile and ListWorkspaceDir: transcript paths are untrusted
// data, the host decides, the viewer only asks.

// diffLabel names the diff view in its header when the item carries no
// file path, and dirKindFile/dirKindDir are the host's entry kinds.
const (
	diffLabel   = "diff"
	dirKindFile = "file"
	dirKindDir  = "dir"
)

type fileViewMode uint8

const (
	fileViewFile fileViewMode = iota
	fileViewDiff
	fileViewFolder
)

type fileViewer struct {
	open    bool
	mode    fileViewMode
	loading bool
	// target names the open file or folder and the line or range to land on.
	wsID, rootName, path string
	// rawPath is the untrusted transcript path before the workspace's roots
	// resolve it; path is what the host is asked for.
	rawPath       string
	line, endLine int
	// pendingRoots holds the workspace roots still to try when a path is not
	// relative to the first one; the host refuses a wrong root typed, the
	// viewer moves to the next candidate.
	pendingRoots []string
	errMsg       string

	file               *client.WorkspaceFile
	code               *mdview.CodeBlockState
	diff               *mdview.CodeBlockState
	diffRaw, diffTitle string
	entries            []client.WorkspaceDirEntry
	entryClicks        map[string]*widget.Clickable

	backBtn, copyBtn *kit.ButtonState
	card             *kit.CardState
	// scrollPending arms the one-time jump to the requested line once the
	// content's line height is known.
	scrollPending bool
}

func newFileViewer() *fileViewer {
	return &fileViewer{
		code:    mdview.NewCodeBlockState(),
		diff:    mdview.NewCodeBlockState(),
		backBtn: kit.NewButtonState(), copyBtn: kit.NewButtonState(),
		card: kit.NewCardState(),
	}
}

func (v *fileViewer) setOpen(open bool) {
	if v.open == open {
		return
	}
	v.open = open
	if !open {
		v.reset()
	}
}

func (v *fileViewer) reset() {
	v.mode, v.loading, v.errMsg = fileViewFile, false, ""
	v.wsID, v.rootName, v.path, v.rawPath = "", "", "", ""
	v.line, v.endLine = 0, 0
	v.pendingRoots = nil
	v.file, v.diffRaw, v.diffTitle = nil, "", ""
	v.entries = nil
	v.entryClicks = nil
	v.scrollPending = false
	v.code = mdview.NewCodeBlockState()
	v.diff = mdview.NewCodeBlockState()
}

// openFile targets a file or folder badge click. The workspace's roots may
// not have arrived yet; applyRoots resolves the target when they do.
func (v *fileViewer) openFile(ref mdview.FileRef, wsID string) {
	v.reset()
	v.open = true
	v.mode = fileViewFolder
	if !ref.Folder {
		v.mode = fileViewFile
	}
	v.wsID, v.line, v.endLine = wsID, ref.Line, ref.EndLine
	v.rawPath, v.loading = ref.Path, true
}

// applyRoots resolves the target against the workspace's registered roots,
// in try order; the first one that answers carries the read. Empty roots
// keep the viewer waiting for the workspace detail read.
func (v *fileViewer) applyRoots(roots []workspace.Root) {
	if !v.open || v.mode == fileViewDiff {
		return
	}
	v.rootName = ""
	v.path, v.pendingRoots = viewerRelativePath(v.rawPath, roots)
	if v.path == "" {
		v.loading = false
		v.errMsg = "That path is outside this session's workspace roots."
		return
	}
	if len(v.pendingRoots) > 0 {
		v.rootName = v.pendingRoots[0]
	}
	v.loading = true
}

// openUnavailable opens the pane with an inline refusal and no read, for a
// click the session cannot serve (no workspace bound).
func (v *fileViewer) openUnavailable(msg string) {
	v.reset()
	v.open = true
	v.errMsg = msg
}

// openDiff targets an Edited row: the diff comes from the file_change item
// itself, so no file read happens.
func (v *fileViewer) openDiff(item *session.Item, diffPath string) {
	v.reset()
	v.open = true
	v.mode = fileViewDiff
	v.path = diffPath
	v.diffRaw, v.diffTitle = item.Display().Text, path.Base(diffPath)
	if v.diffTitle == "." || v.diffTitle == "/" {
		v.diffTitle = diffLabel
	}
}

// request arms the worker read for the current target; each pending root is
// tried in turn by the result handler.
func (v *fileViewer) request(w *fileViewWorker) {
	if !v.open || !v.loading || len(v.pendingRoots) == 0 || w == nil {
		return
	}
	root := v.pendingRoots[0]
	switch v.mode {
	case fileViewFile:
		w.Do(fileRequest{Op: fileOpRead, WorkspaceID: v.wsID, RootName: root, Path: v.path})
	case fileViewFolder:
		w.Do(fileRequest{Op: fileOpList, WorkspaceID: v.wsID, RootName: root, Path: v.path})
	case fileViewDiff:
	}
}

// apply folds one finished read into the viewer. An outside-root refusal
// moves to the next root candidate before surfacing anything.
func (v *fileViewer) apply(res *fileResult) {
	if !v.open || res.Path != v.path {
		return
	}
	v.loading = false
	if res.Err != nil {
		if errorsIsPathOutsideRoot(res.Err) && v.tryNextRoot() {
			v.loading = true
			return
		}
		v.errMsg = fileViewErrorMessage(res.Err)
		return
	}
	switch res.Op {
	case fileOpRead:
		v.file = &res.File
		// Expanded keeps the block uncapped while still bounding its
		// viewport, so long files scroll inside the pane instead of
		// stretching it (desktop-app v0.9 height rules, F10.4e).
		v.code = mdview.NewCodeBlockState()
		v.code.Expanded = true
		v.scrollPending = v.line > 0
	case fileOpList:
		v.entries = res.Entries
		v.entryClicks = make(map[string]*widget.Clickable, len(res.Entries))
	}
}

// tryNextRoot drops the current root candidate and reports whether another
// remains for a retry.
func (v *fileViewer) tryNextRoot() bool {
	if len(v.pendingRoots) == 0 {
		return false
	}
	v.pendingRoots = v.pendingRoots[1:]
	if len(v.pendingRoots) == 0 {
		return false
	}
	v.errMsg = ""
	return true
}

func errorsIsPathOutsideRoot(err error) bool {
	return errors.Is(err, client.ErrWorkspacePathOutsideRoot)
}

// fileViewErrorMessage turns a refused read into the viewer's inline message.
func fileViewErrorMessage(err error) string {
	switch {
	case errors.Is(err, client.ErrWorkspaceFileNotFound):
		return "This file no longer exists in the workspace."
	case errors.Is(err, client.ErrWorkspaceFileTooLarge):
		return "This file is over the 1 MiB read limit."
	case errors.Is(err, client.ErrWorkspaceNotText):
		return "This file is not UTF-8 text, so it cannot be shown here."
	case errorsIsPathOutsideRoot(err):
		return "That path is outside this session's workspace roots."
	default:
		return err.Error()
	}
}

// viewerRelativePath turns a transcript path into one relative to the first
// root that contains it, returning the remaining roots as fallbacks. An
// absolute path must live under a registered root; a relative path may live
// under any of them.
func viewerRelativePath(refPath string, roots []workspace.Root) (string, []string) {
	if len(roots) == 0 {
		return "", nil
	}
	clean := strings.TrimSuffix(path.Clean(strings.TrimSpace(refPath)), "/")
	if path.IsAbs(clean) {
		for _, r := range roots {
			root := strings.TrimSuffix(r.Path, "/")
			if clean == root {
				return ".", []string{r.Name}
			}
			if rel, ok := strings.CutPrefix(clean, root+"/"); ok && rel != "" {
				return rel, []string{r.Name}
			}
		}
		return "", nil
	}
	names := make([]string, 0, len(roots))
	for _, r := range roots {
		names = append(names, r.Name)
	}
	return clean, names
}

// layout draws the viewer pane: header, then file, diff, folder, or the
// inline error, never a crash.
func (v *fileViewer) layout(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	paint.FillShape(gtx.Ops, env.Th.Chrome().CanvasBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Inset{Top: theme.SpaceMD, Bottom: theme.SpaceMD, Left: theme.SpaceLG, Right: theme.SpaceLG}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutHeader(gtx, env, sc) }),
			spacerH(theme.SpaceMD),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return v.layoutBody(&local, env, sc)
			}),
		)
	})
}

func (v *fileViewer) layoutHeader(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	ch := env.Th.Chrome()
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			name, icon := v.headerSymbol()
			d, err := env.DrawSymbol(gtx, name, &icon, theme.IconSM, env.Th.FileColor(theme.FileColorName(icon.Color)))
			sc.fail(err)
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Dp(theme.SpaceSM), 0)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			local.Constraints.Max.X = max(gtx.Constraints.Max.X-gtx.Dp(220), gtx.Dp(80))
			return kit.MiddleEllipsisLabel(env, &local, theme.TypeLabel(), ch.Fg, v.displayPath())
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
		}),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			if v.mode == fileViewDiff || v.path == "" {
				return layout.Dimensions{}
			}
			bp := kit.ButtonProps{Text: "Copy path", Icon: kit.IconCopy, Variant: kit.Ghost, Size: kit.SizeSM}
			clicked, err := v.copyBtn.Layout(env, gtx, bp)
			sc.fail(err)
			if clicked {
				gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(v.fullPath()))})
			}
			return v.copyBtn.Measure(env, gtx, bp)
		}),
		rigidInboxButton(env, sc.fail, v.backBtn, kit.ButtonProps{Text: labelBackToChat, Variant: kit.Ghost, Size: kit.SizeSM, Disabled: sc.open == ""}, func() {
			v.setOpen(false)
		}),
	)
}

// headerSymbol resolves the header glyph from the pure target path, never
// the display string: the root prefix and the ":96-103" line suffix would
// defeat the extension lookup, and a folder must use the folder glyph.
func (v *fileViewer) headerSymbol() (string, icondata.FileIcon) {
	if v.mode == fileViewFolder {
		return kit.FolderIconData(v.path)
	}
	if v.mode == fileViewDiff {
		// A generic "diff" title degrades to the generic document glyph.
		return kit.FileIconData(v.diffTitle)
	}
	return kit.FileIconData(v.path)
}

func (v *fileViewer) displayPath() string {
	if v.mode == fileViewDiff {
		if v.diffTitle != "" && v.diffTitle != diffLabel {
			return v.diffTitle
		}
		return diffLabel
	}
	name := v.path
	if v.rootName != "" && v.path != "." {
		name = v.rootName + "/" + v.path
	}
	if v.line > 0 && v.mode == fileViewFile {
		if v.endLine > v.line {
			return fmt.Sprintf("%s:%d-%d", name, v.line, v.endLine)
		}
		return fmt.Sprintf("%s:%d", name, v.line)
	}
	return name
}

func (v *fileViewer) fullPath() string {
	if v.rootName == "" || v.path == "." {
		return v.path
	}
	return v.rootName + "/" + v.path
}

func (v *fileViewer) layoutBody(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	switch {
	case v.errMsg != "":
		return v.layoutError(env, gtx, sc)
	case v.loading:
		return kit.Label(env, gtx, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Reading…")
	case v.mode == fileViewDiff:
		return v.layoutDiff(env, gtx)
	case v.mode == fileViewFolder:
		return v.layoutFolder(env, gtx, sc)
	case v.file != nil:
		return v.layoutFile(env, gtx)
	default:
		return kit.Label(env, gtx, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Nothing to show.")
	}
}

func (v *fileViewer) layoutError(env *kit.Env, gtx *layout.Context, _ *screens) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return kit.Label(env, &l, theme.TypeBody(), env.Th.Status().Danger, v.errMsg)
		}),
	)
}

// layoutFile renders the read through the shared code block: line numbers,
// syntax colors from the host's language hint, wrap toggle, horizontal
// scroll, and the requested line or range tinted. The jump lands a few rows
// above the range so it reads with context; the reader scrolls on from there.
func (v *fileViewer) layoutFile(env *kit.Env, gtx *layout.Context) layout.Dimensions {
	jump := 0
	if v.scrollPending {
		jump = max(v.line-viewerScrollContext, 1)
		v.scrollPending = false
	}
	end := max(v.endLine, v.line)
	local := *gtx
	dims, _ := mdview.LayoutCodeBlock(env, &local, v.code, &mdview.CodeBlockProps{
		Title: path.Base(v.path), Format: v.file.Language,
		ShowLineNumbers: true, MaxLines: -1,
		Raw:              v.file.Content,
		HighlightLine:    v.line,
		HighlightEndLine: end,
		ScrollToLine:     jump,
		ViewportLines:    viewerViewportLines(gtx),
	})
	return dims
}

// viewerScrollContext is how many rows above the requested range the jump
// keeps visible.
const viewerScrollContext = 4

// viewerViewportLines sizes the code block's inner viewport to the pane, so
// the requested line lands on screen instead of inside an oversized block.
func viewerViewportLines(gtx *layout.Context) int {
	lineEst := max(gtx.Sp(theme.TypeCode().Line), 1)
	chrome := 6 * gtx.Dp(theme.SpaceMD)
	return max((gtx.Constraints.Max.Y-chrome)/lineEst, 8)
}

// layoutDiff renders the Edited row's full diff with the same three columns
// and concentric hunk fills as the inline diff, uncapped.
func (v *fileViewer) layoutDiff(env *kit.Env, gtx *layout.Context) layout.Dimensions {
	local := *gtx
	dims, _ := mdview.LayoutCodeBlock(env, &local, v.diff, &mdview.CodeBlockProps{
		Title: v.diffTitle, IsDiff: true,
		ShowLineNumbers: true, MaxLines: -1,
		Raw: v.diffRaw,
	})
	return dims
}

// layoutFolder lists the directory's entries as file and folder badges;
// clicking one opens it in this viewer.
func (v *fileViewer) layoutFolder(env *kit.Env, gtx *layout.Context, sc *screens) layout.Dimensions {
	if len(v.entries) == 0 {
		return kit.Label(env, gtx, theme.TypeSmall(), env.Th.Chrome().FgMuted, "This folder is empty.")
	}
	children := make([]layout.FlexChild, 0, len(v.entries))
	for _, e := range v.entries {
		entry := e
		kind := mdview.BadgeFile
		if entry.Kind == dirKindDir {
			kind = mdview.BadgeFolder
		}
		click := ensureEntryClick(v.entryClicks, entry.Name)
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				ref := mdview.FileRef{Path: joinDirPath(v.path, entry.Name), Raw: entry.Name, Folder: entry.Kind == dirKindDir}
				return mdview.DrawBadge(env, &l, click, mdview.BadgeSpec{Kind: kind, File: ref}, func(r mdview.FileRef) {
					sc.openFileRef(r)
				})
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func ensureEntryClick(m map[string]*widget.Clickable, name string) *widget.Clickable {
	if m == nil {
		return nil
	}
	if c, ok := m[name]; ok {
		return c
	}
	c := &widget.Clickable{}
	m[name] = c
	return c
}

func joinDirPath(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return dir + "/" + name
}
