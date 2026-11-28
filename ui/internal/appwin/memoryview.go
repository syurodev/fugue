package appwin

import (
	"fmt"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/folderpicker"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/memory"
)

const (
	memoryPickerExportPrefix = "memory-export:"
	memoryPickerImportPrefix = "memory-import:"
)

type memoryViewMode uint8

const (
	memModeList memoryViewMode = iota
	memModeDetail
	memModeEdit
	memModeImport
)

type memoryRowWidgets struct {
	row           *kit.ListRowState
	unverifiedTag *kit.TagState
	staleTag      *kit.TagState
}

// memoryView owns the per-workspace Memory slot UI (workspace-memory §10):
// list with FilterBar (SearchEntries / ListEntries), entry detail & edit/verify/
// invalidate + history restore, and export / import diff.
type memoryView struct {
	wsID string
	mode memoryViewMode

	loaded   bool
	loading  bool
	listErr  string
	infoMsg  string
	entries  []client.MemoryEntry
	results  []client.MemorySearchResult
	inSearch bool
	approx   bool

	query           string
	kindFilter      string
	verifiedFilter  string
	authorFilter    string
	freshnessFilter string
	validityFilter  string

	filterBar    *kit.FilterBarState
	newBtn       *kit.ButtonState
	clearBtn     *kit.ButtonState
	empty        *kit.EmptyStateState
	errBanner    *kit.BannerState
	infoBanner   *kit.BannerState
	approxBanner *kit.BannerState
	scroll       *kit.ScrollAreaState
	rowWidgets   map[string]*memoryRowWidgets

	// Detail / edit state (memorydetail.go & memorydlg.go).
	selected      *client.MemoryEntry
	history       []client.MemoryHistoryEntry
	detailLoading bool
	detailBusy    bool
	detailErr     string
	backBtn       *kit.ButtonState
	editBtn       *kit.ButtonState
	verifyBtn     *kit.ButtonState
	invBtn        *kit.ButtonState
	saveBtn       *kit.ButtonState
	cancelEditBtn *kit.ButtonState
	verTag        *kit.TagState
	staleTag      *kit.TagState
	valTag        *kit.TagState
	anchorTags    []*kit.TagState
	revHistory    *RevisionHistoryState
	creating      bool
	editKind      string
	editKindSel   *kit.SelectState
	editLabel     *kit.TextFieldState
	editSummary   *kit.TextFieldState
	editKeywords  *kit.TextFieldState
	editBody      *kit.TextAreaState
	invDlg        *memoryInvalidateDialog

	// Import diff state (memoryimport.go).
	importPreview *client.MemoryImportPreview
	importBytes   []byte
	importFile    string
	importRows    []*memoryImportRowState
	importBusy    bool
	importStale   bool
	importErr     string
	importResult  *client.MemoryImportResult
	importScroll  *kit.ScrollAreaState
	importApply   *kit.ButtonState
	importCancel  *kit.ButtonState
	importBack    *kit.ButtonState

	lastRenderedLocked bool
}

func newMemoryView(wsID string) *memoryView {
	return &memoryView{
		wsID:          wsID,
		filterBar:     kit.NewFilterBarState(),
		newBtn:        kit.NewButtonState(),
		clearBtn:      kit.NewButtonState(),
		empty:         kit.NewEmptyStateState(),
		errBanner:     kit.NewBannerState(),
		infoBanner:    kit.NewBannerState(),
		approxBanner:  kit.NewBannerState(),
		scroll:        kit.NewScrollAreaState(),
		rowWidgets:    map[string]*memoryRowWidgets{},
		backBtn:       kit.NewButtonState(),
		editBtn:       kit.NewButtonState(),
		verifyBtn:     kit.NewButtonState(),
		invBtn:        kit.NewButtonState(),
		saveBtn:       kit.NewButtonState(),
		cancelEditBtn: kit.NewButtonState(),
		verTag:        kit.NewTagState(),
		staleTag:      kit.NewTagState(),
		valTag:        kit.NewTagState(),
		revHistory:    NewRevisionHistoryState(),
		editKind:      client.MemoryKindFact,
		editKindSel:   kit.NewSelectState(),
		editLabel:     kit.NewTextFieldState(),
		editSummary:   kit.NewTextFieldState(),
		editKeywords:  kit.NewTextFieldState(),
		editBody:      kit.NewTextAreaState(),
		invDlg:        newMemoryInvalidateDialog(),
		importScroll:  kit.NewScrollAreaState(),
		importApply:   kit.NewButtonState(),
		importCancel:  kit.NewButtonState(),
		importBack:    kit.NewButtonState(),
	}
}

func (s *screens) memoryViewFor(wsID string) *memoryView {
	if s.memoryViews == nil {
		s.memoryViews = map[string]*memoryView{}
	}
	if v, ok := s.memoryViews[wsID]; ok {
		return v
	}
	v := newMemoryView(wsID)
	s.memoryViews[wsID] = v
	return v
}

func (s *screens) applyMemoryDelivery(u hostconn.Update) {
	if s.memory == nil {
		return
	}
	if u.Snapshot != nil {
		s.fail(s.memory.ApplySnapshot(u.Snapshot.State))
		return
	}
	if u.Event != nil {
		ev, err := s.memory.ApplyEvent(u.Event.Type, u.Event.Payload)
		if err != nil {
			s.fail(err)
			return
		}
		if ev.WorkspaceID != "" && s.memoryWorker != nil {
			if v, ok := s.memoryViews[ev.WorkspaceID]; ok && v.loaded {
				v.requestQuery(s)
				if v.selected != nil && v.selected.ID == ev.EntryID && v.mode == memModeDetail {
					v.requestEntryDetail(s, ev.EntryID)
				}
			}
		}
	}
}

func (s *screens) refreshOpenMemoryViews() {
	if s.memoryWorker == nil {
		return
	}
	for _, v := range s.memoryViews {
		if v.loaded {
			v.requestQuery(s)
		}
	}
}

func drainMemoryResults(w *memory.Worker) []memory.Result {
	var out []memory.Result
	for {
		select {
		case r := <-w.Results():
			out = append(out, r)
		default:
			return out
		}
	}
}

func (s *screens) applyMemoryResults(results []memory.Result) {
	for i := range results {
		res := &results[i]
		v := s.memoryViewFor(res.WorkspaceID)
		v.applyResult(s, res)
	}
}

func (v *memoryView) applyResult(sc *screens, res *memory.Result) {
	switch res.Kind {
	case memory.OpListEntries:
		v.loading = false
		v.loaded = true
		if res.Err != nil {
			v.listErr = hostErrorMessage(res.Err)
			return
		}
		v.listErr = ""
		v.inSearch = false
		v.approx = false
		v.entries = res.Entries
	case memory.OpSearchEntries:
		v.loading = false
		v.loaded = true
		if res.Err != nil {
			v.listErr = hostErrorMessage(res.Err)
			return
		}
		v.listErr = ""
		v.inSearch = true
		v.approx = res.Search.Approximate
		v.results = res.Search.Results
	case memory.OpGetEntry:
		v.detailLoading = false
		if res.Err != nil {
			v.detailErr = hostErrorMessage(res.Err)
			return
		}
		e := res.Entry
		v.selected = &e
	case memory.OpGetEntryHistory:
		if res.Err != nil {
			v.detailErr = hostErrorMessage(res.Err)
			return
		}
		v.history = res.History
	case memory.OpWriteEntry, memory.OpVerifyEntry, memory.OpRestoreEntry:
		v.applyWriteResult(sc, res)
	case memory.OpExportToFile:
		if res.Err != nil {
			v.listErr = hostErrorMessage(res.Err)
			return
		}
		v.listErr = ""
		v.infoMsg = fmt.Sprintf("Exported memory to %s", res.FilePath)
	case memory.OpPreviewImportFile, memory.OpPreviewImportBytes:
		v.importBusy = false
		if res.Err != nil {
			if v.mode == memModeImport {
				v.importErr = hostErrorMessage(res.Err)
			} else {
				v.listErr = hostErrorMessage(res.Err)
			}
			return
		}
		prev := res.Preview
		v.loadPreview(&prev, res.ImportBytes, res.ImportFile)
	case memory.OpApplyImport:
		v.applyImportResult(sc, res)
	}
}

func (s *screens) handleMemoryPickerResult(res folderpicker.Result) {
	if res.Cancelled {
		return
	}
	var wsID string
	var isExport bool
	if id, ok := strings.CutPrefix(res.Tag, memoryPickerExportPrefix); ok {
		wsID = id
		isExport = true
	} else if id, ok := strings.CutPrefix(res.Tag, memoryPickerImportPrefix); ok {
		wsID = id
	} else {
		return
	}
	v := s.memoryViewFor(wsID)
	if res.Err != nil {
		v.listErr = hostErrorMessage(res.Err)
		return
	}
	if res.Path == "" || s.memoryWorker == nil {
		return
	}
	v.listErr = ""
	if isExport {
		s.memoryWorker.Do(&memory.Request{Kind: memory.OpExportToFile, WorkspaceID: wsID, FilePath: res.Path})
		return
	}
	s.memoryWorker.Do(&memory.Request{Kind: memory.OpPreviewImportFile, WorkspaceID: wsID, FilePath: res.Path})
}

func memorySlotHeaderActions(sc *screens, wsID string) []slotAction {
	disabled := sc.vault.Locked() || sc.memoryWorker == nil || wsID == ""
	wsName := wsID
	if ws, ok := sc.workspaces.Lookup(wsID); ok && ws.Name != "" {
		wsName = ws.Name
	}
	return []slotAction{
		{
			props: kit.IconButtonProps{
				Icon: kit.IconPlus, Label: "New memory entry",
				Variant: kit.IconButtonGhost, Size: kit.SizeXS, Disabled: disabled,
			},
			onClick: func() { sc.memoryViewFor(wsID).startCreate() },
		},
		{
			props: kit.IconButtonProps{
				Icon: kit.IconArrowUp, Label: "Export memory",
				Variant: kit.IconButtonGhost, Size: kit.SizeXS, Disabled: disabled,
			},
			onClick: func() {
				if sc.folderPicker != nil {
					sc.folderPicker.PickSaveFile(memoryPickerExportPrefix+wsID, wsName+"-memory.md")
				}
			},
		},
		{
			props: kit.IconButtonProps{
				Icon: kit.IconArrowDown, Label: "Import memory",
				Variant: kit.IconButtonGhost, Size: kit.SizeXS, Disabled: disabled,
			},
			onClick: func() {
				if sc.folderPicker != nil {
					sc.folderPicker.PickOpenFile(memoryPickerImportPrefix+wsID, ".md")
				}
			},
		},
	}
}

func (s *screens) layoutMemoryDialogs(env *kit.Env, gtx *layout.Context) {
	for _, v := range s.memoryViews {
		v.invDlg.layout(env, gtx, s, v)
	}
}

func (v *memoryView) layout(gtx *layout.Context, env *kit.Env, sc *screens) {
	if sc.vault.Locked() {
		v.lastRenderedLocked = true
		return
	}
	v.lastRenderedLocked = false
	if !v.loaded && !v.loading && sc.memoryWorker != nil && v.wsID != "" {
		v.requestQuery(sc)
	}
	layoutSlotContent(gtx, env, func(gtx *layout.Context) layout.Dimensions {
		switch v.mode {
		case memModeDetail:
			return v.layoutDetail(gtx, env, sc)
		case memModeEdit:
			return v.layoutEdit(gtx, env, sc)
		case memModeImport:
			return v.layoutImportDiff(gtx, env, sc)
		default:
			return v.layoutList(gtx, env, sc)
		}
	})
}
