package appwin

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/ui/internal/folderpicker"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The shared source-folder list (FUG-115): the rows the new-workspace and
// add-folders dialogs turn into roots. Add runs the desktop's native chooser
// through the window's folder picker and appends the chosen folder; the
// typed path field is the fallback for a desktop without a picker or a user
// who prefers typing. A path already in the list, or already a root of the
// workspace, is refused inline. Every picked path is data: the host still
// enforces every root rule when the rows are added.

// sourceFolders is the list's long-lived state, one instance per dialog. It
// changes only on the window goroutine.
type sourceFolders struct {
	rows []*sourceRow
	// add is the picker's Add button (the folder-plus icon); reveal is the
	// "Type a path instead" link; typedAdd adds the typed path's row.
	add, reveal, typedAdd *kit.ButtonState
	typed                 *kit.TextFieldState
	card                  *kit.CardState
	// typedErr is the typed field's refusal, shown only after an add
	// attempt on it; refused is the list-level refusal (a repeated path, a
	// failed pick); a host error lives on the row it belongs to.
	typedErr, refused string
	// noPicker marks a desktop whose probe found no chooser: the path field
	// opens with a short note. typedOpen marks the revealed field.
	noPicker, typedOpen bool
	// picking marks a pick in flight; picker is the window's worker.
	picking bool
	picker  *folderpicker.Picker
	// onRemove is the dialog's remove step: it keeps the add chain pointing
	// at the row that took the removed one's place.
	onRemove func(i int)
	// roots are the workspace's existing roots: duplicates against them are
	// refused and their names count for the uniqueness suffix. Empty in the
	// create dialog, whose workspace does not exist yet.
	roots []workspace.Root
}

// sourceRow is one row's long-lived state: the folder's path, the
// inline-editable root name, the access pick and the remove button.
type sourceRow struct {
	path      string
	name      *kit.TextFieldState
	access    *kit.SelectState
	accessSel string
	rm        *kit.IconButtonState
	// err is the host's typed answer when this row's add failed: the repair
	// prompt rides the row it belongs to.
	err string
}

func newSourceFolders() sourceFolders {
	return sourceFolders{
		add:      kit.NewButtonState(),
		reveal:   kit.NewButtonState(),
		typedAdd: kit.NewButtonState(),
		typed:    kit.NewTextFieldState(),
		card:     kit.NewCardState(),
	}
}

// setPicker arms the list with the window's worker and the desktop's answer:
// without a picker — or on a desktop whose probe found no chooser — the
// typed field opens with the fallback note.
func (f *sourceFolders) setPicker(p *folderpicker.Picker) {
	f.picker = p
	f.noPicker = p == nil || p.First() == folderpicker.HelperNone
}

// reset clears the list for a dialog's fresh open, keeping the workspace's
// roots as the duplicate and uniqueness context.
func (f *sourceFolders) reset(roots []workspace.Root) {
	f.rows = nil
	f.roots = roots
	f.refused, f.typedErr = "", ""
	f.typed.SetText("")
	f.typedOpen = f.noPicker
	f.picking = false
}

// addPath appends the folder as a row named after it, refusing a path the
// list or the workspace already holds.
func (f *sourceFolders) addPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	for _, row := range f.rows {
		if row.path == path {
			f.refused = "That folder is already in the list."
			return
		}
	}
	for _, root := range f.roots {
		if root.Path == path {
			f.refused = "That folder is already a root of this workspace."
			return
		}
	}
	f.rows = append(f.rows, &sourceRow{
		path:      path,
		name:      kit.NewTextFieldState(),
		access:    kit.NewSelectState(),
		accessSel: defaultAccess,
		rm:        kit.NewIconButtonState(),
	})
	f.rows[len(f.rows)-1].name.SetText(f.uniqueName(baseName(path)))
	f.refused = ""
}

// baseName is the folder's own name: the default root name's base.
func baseName(path string) string {
	return filepath.Base(path)
}

// defaultAccess is the access a fresh row starts at.
const defaultAccess = "rw"

// uniqueName suffixes the base with the smallest number that frees it within
// the workspace: the list's names plus its existing roots'.
func (f *sourceFolders) uniqueName(base string) string {
	taken := make(map[string]bool, len(f.rows)+len(f.roots))
	for _, row := range f.rows {
		taken[row.name.Text()] = true
	}
	for _, root := range f.roots {
		taken[root.Name] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		name := fmt.Sprintf("%s-%d", base, n)
		if !taken[name] {
			return name
		}
	}
}

// remove drops one row; a chain waiting on it continues from the row that
// takes its place, which the dialog's onRemove keeps its index honest about.
func (f *sourceFolders) remove(i int) {
	if i < 0 || i >= len(f.rows) {
		return
	}
	f.rows = append(f.rows[:i], f.rows[i+1:]...)
}

// pick runs the native chooser off the dialog; the result lands on the frame
// loop in picked.
func (f *sourceFolders) pick() {
	if f.picking || f.picker == nil {
		return
	}
	if f.picker.Pick() {
		f.picking = true
	}
}

// picked folds one pick result in: a cancel changes nothing, a failure is
// refused inline, and a chosen folder becomes a row.
func (f *sourceFolders) picked(res folderpicker.Result) {
	f.picking = false
	switch {
	case res.Cancelled:
		return
	case res.Err != nil:
		f.refused = res.Err.Error()
	default:
		f.addPath(res.Path)
	}
}

// revealTyped opens the fallback path field.
func (f *sourceFolders) revealTyped() {
	f.typedOpen = true
}

// addTyped adds the typed path's row. An empty field refuses only after the
// attempt: the dialog opens silent.
func (f *sourceFolders) addTyped() {
	if !f.typedOpen {
		return
	}
	path := strings.TrimSpace(f.typed.Text())
	if path == "" {
		f.typedErr = "Type the folder's absolute path"
		return
	}
	f.typedErr = ""
	f.addPath(path)
	if f.refused == "" {
		f.typed.SetText("")
	}
}

// dirty reports input the dialog would lose on an Escape: rows, or a typed
// path not yet added.
func (f *sourceFolders) dirty() bool {
	if len(f.rows) > 0 {
		return true
	}
	return f.typedOpen && f.typed.Text() != ""
}
