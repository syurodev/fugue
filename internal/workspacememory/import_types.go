package workspacememory

import "time"

// ImportClass classifies a parsed block relative to the target workspace (§9).
type ImportClass string

// Import classification values (§9).
const (
	ImportClassAdd           ImportClass = "add"
	ImportClassSkip          ImportClass = "skip"
	ImportClassConflict      ImportClass = "conflict"
	ImportClassLabelConflict ImportClass = "label_conflict"
	ImportClassRejected      ImportClass = "rejected"
)

// ImportChoice selects how ApplyImport resolves a preview item (§9).
type ImportChoice string

// Import resolution choices (§9).
const (
	ImportChoiceKeepLocal    ImportChoice = "keep_local"
	ImportChoiceTakeImported ImportChoice = "take_imported"
	ImportChoiceKeepBoth     ImportChoice = "keep_both"
	ImportChoiceSkip         ImportChoice = "skip"
	ImportChoiceRename       ImportChoice = "rename"
)

// ImportPreviewItem describes one diff row produced by PreviewImport.
type ImportPreviewItem struct {
	ItemID        string
	EntryID       string
	Label         string
	Class         ImportClass
	DefaultChoice ImportChoice
	RejectReason  string
	Local         *Entry
	Imported      *Entry
}

// ImportPreview captures the parsed diff and target revision snapshot for ApplyImport.
type ImportPreview struct {
	WorkspaceName     string
	ExportedAt        time.Time
	Items             []ImportPreviewItem
	SnapshotRevisions map[string]uint64
	SnapshotValidIDs  []string
}

// ImportItemChoice specifies the user's resolution choice for one preview item.
type ImportItemChoice struct {
	ItemID   string
	Choice   ImportChoice
	NewLabel string
}

// ApplyImportInput holds parameters for ApplyImport.
type ApplyImportInput struct {
	WorkspaceID string
	Principal   string
	ImportFile  string
	Preview     ImportPreview
	Choices     []ImportItemChoice
}

// ApplyImportResult summarizes the entries committed by ApplyImport.
type ApplyImportResult struct {
	Written []Entry
	Added   int
	Updated int
	Skipped int
}
