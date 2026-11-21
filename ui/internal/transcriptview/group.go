package transcriptview

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
)

// ToolCategory classifies a tool_call for its row icon and sub-group summary.
type ToolCategory uint8

const (
	// CatOther is the fallback tool category.
	CatOther ToolCategory = iota
	// CatTerminal is shell/command execution.
	CatTerminal
	// CatEdit is file creation or editing.
	CatEdit
	// CatRead is file reading.
	CatRead
	// CatSearch is grep/glob/search.
	CatSearch
	// CatNetwork is web fetch or search.
	CatNetwork
)

const (
	statusPending = "pending"
	statusRunning = "running"
	statusDone    = "done"
	statusFailed  = "failed"
	statusDenied  = "denied"
	formatDiff    = "diff"
)

// displayRow is one VirtualList entry: either a standalone transcript item or a
// folded work group of consecutive work items in one turn.
type displayRow struct {
	key   string
	rev   uint64
	item  *session.Item
	group *workGroup
}

type subGroup struct {
	key      string
	category ToolCategory
	kind     string
	title    string
	items    []*session.Item
}

type workGroup struct {
	key          string
	turnID       string
	items        []*session.Item
	subGroups    []subGroup
	running      bool
	hasFailure   bool
	durationMs   int64
	currentTitle string
}

type commitEffect struct {
	Hash    string
	Message string
}

// buildDisplayRows folds consecutive work items (tool_call, file_change,
// subagent, command) belonging to the same turn into one WorkGroup entry
// (desktop-app v0.6 §5.4). Singletons stay standalone rows so single items
// keep their direct item_id key.
func buildDisplayRows(items []*session.Item) []displayRow {
	out := make([]displayRow, 0, len(items))
	i := 0
	for i < len(items) {
		it := items[i]
		if !isWorkKind(it.Kind) {
			out = append(out, displayRow{key: it.ItemID, rev: it.Revision, item: it})
			i++
			continue
		}
		j := i + 1
		for j < len(items) && isWorkKind(items[j].Kind) && items[j].TurnID == it.TurnID {
			// Group consecutive work items when they share a non-empty TurnID,
			// or when consecutive work items appear together in a turn.
			if it.TurnID == "" && items[j].TurnID == "" {
				break
			}
			j++
		}
		if j-i < 2 {
			out = append(out, displayRow{key: it.ItemID, rev: it.Revision, item: it})
			i++
			continue
		}
		grp := makeWorkGroup(items[i:j])
		var rev uint64
		for _, member := range grp.items {
			rev += member.Revision
		}
		out = append(out, displayRow{key: grp.key, rev: rev, group: grp})
		i = j
	}
	return out
}

func isWorkKind(kind string) bool {
	switch kind {
	case kindToolCall, kindFileChange, kindSubagent, kindCommand:
		return true
	default:
		return false
	}
}

func makeWorkGroup(items []*session.Item) *workGroup {
	g := &workGroup{
		key:    "wg:" + items[0].ItemID,
		turnID: items[0].TurnID,
		items:  items,
	}
	accumulateWorkGroup(g, items)
	if g.currentTitle == "" {
		g.currentTitle = workItemStepTitle(items[len(items)-1])
	}
	g.subGroups = buildSubGroups(items)
	return g
}

func accumulateWorkGroup(g *workGroup, items []*session.Item) {
	var firstCreated, lastUpdated time.Time
	for _, it := range items {
		if isItemRunning(it) {
			g.running = true
			g.currentTitle = workItemStepTitle(it)
		}
		if isToolFailed(it) {
			g.hasFailure = true
		}
		g.durationMs += it.Metadata.DurationMillis
		firstCreated, lastUpdated = expandTSBounds(firstCreated, lastUpdated, it)
	}
	if g.durationMs == 0 && !firstCreated.IsZero() && lastUpdated.After(firstCreated) {
		g.durationMs = lastUpdated.Sub(firstCreated).Milliseconds()
	}
}

func expandTSBounds(firstCreated, lastUpdated time.Time, it *session.Item) (time.Time, time.Time) {
	if ts, ok := parseTS(it.CreatedAt); ok && (firstCreated.IsZero() || ts.Before(firstCreated)) {
		firstCreated = ts
	}
	if ts, ok := parseTS(it.UpdatedAt); ok && ts.After(lastUpdated) {
		lastUpdated = ts
	}
	return firstCreated, lastUpdated
}

// minFoldedRun is the shortest run of same-kind items folded under one
// summary row.
const minFoldedRun = 4

func buildSubGroups(items []*session.Item) []subGroup {
	var out []subGroup
	i := 0
	for i < len(items) {
		cat := subGroupCategory(items[i])
		j := i + 1
		for j < len(items) && subGroupCategory(items[j]) == cat {
			j++
		}
		slice := items[i:j]
		if len(slice) < minFoldedRun {
			// Short runs stay as individual rows; folding three edits into
			// "Edited 3 files ›" hides more than it saves.
			for _, it := range slice {
				out = append(out, subGroup{
					key: "sg:" + it.ItemID, category: cat, kind: it.Kind,
					title: subGroupSummary(cat, []*session.Item{it}), items: []*session.Item{it},
				})
			}
			i = j
			continue
		}
		out = append(out, subGroup{
			key:      "sg:" + slice[0].ItemID,
			category: cat,
			kind:     slice[0].Kind,
			title:    subGroupSummary(cat, slice),
			items:    slice,
		})
		i = j
	}
	return out
}

func subGroupCategory(it *session.Item) ToolCategory {
	switch it.Kind {
	case kindFileChange:
		return CatEdit
	case kindCommand:
		return CatTerminal
	case kindToolCall:
		return toolCategory(it.Metadata.Tool)
	default:
		return CatOther
	}
}

func subGroupSummary(cat ToolCategory, items []*session.Item) string {
	n := len(items)
	if items[0].Kind == kindSubagent {
		return fmt.Sprintf("Ran %d subagents", n)
	}
	switch cat {
	case CatRead:
		return fmt.Sprintf("Read %d files", n)
	case CatEdit:
		return fmt.Sprintf("Edited %d files", n)
	case CatTerminal:
		return fmt.Sprintf("Ran %d commands", n)
	case CatSearch:
		return fmt.Sprintf("Ran %d searches", n)
	case CatNetwork:
		return fmt.Sprintf("Fetched %d resources", n)
	case CatOther:
		return fmt.Sprintf("Used %d tools", n)
	}
	return fmt.Sprintf("Used %d tools", n)
}

func workItemStepTitle(it *session.Item) string {
	switch it.Kind {
	case kindFileChange:
		return "Edited " + fileChangePath(it)
	case kindSubagent:
		return subagentTitle(it)
	case kindCommand:
		return commandTitle(it)
	default:
		return toolShortTitle(it)
	}
}

func toolCategory(toolName string) ToolCategory {
	low := strings.ToLower(toolName)
	switch {
	case strings.Contains(low, "bash"), strings.Contains(low, "shell"),
		strings.Contains(low, "command"), low == "exec", strings.Contains(low, "terminal"):
		return CatTerminal
	case strings.Contains(low, "edit"), strings.Contains(low, "write"), strings.Contains(low, "patch"):
		return CatEdit
	case strings.Contains(low, "read"), low == "view", low == "cat":
		return CatRead
	case strings.Contains(low, "grep"), strings.Contains(low, "glob"),
		strings.Contains(low, "search"), strings.Contains(low, "find"):
		return CatSearch
	case strings.Contains(low, "web"), strings.Contains(low, "fetch"), strings.Contains(low, "http"):
		return CatNetwork
	default:
		return CatOther
	}
}

func iconForCategory(cat ToolCategory) kit.IconName {
	switch cat {
	case CatTerminal:
		return kit.IconTerminal
	case CatEdit:
		return kit.IconPencil
	case CatRead:
		return kit.IconFileText
	case CatSearch:
		return kit.IconSearch
	case CatNetwork:
		return kit.IconGlobe
	case CatOther:
		return kit.IconWrench
	}
	return kit.IconWrench
}

func toolShortTitle(it *session.Item) string {
	view := it.Display()
	cat := toolCategory(it.Metadata.Tool)
	fields := parseToolInput(view.Input)
	if fields.Description != "" {
		return fields.Description
	}
	if title := toolTitleFromFields(cat, &fields, view.Title, it.Metadata.Tool); title != "" {
		return title
	}
	if view.Title != "" {
		return view.Title
	}
	if it.Metadata.Tool != "" {
		return it.Metadata.Tool
	}
	return "Tool call"
}

func toolTitleFromFields(cat ToolCategory, fields *toolInputFields, viewTitle, metaTool string) string {
	switch cat {
	case CatTerminal:
		return terminalToolTitle(fields, viewTitle, metaTool)
	case CatRead, CatEdit:
		return fileToolTitle(cat, fields.FilePath)
	case CatSearch:
		return prefixIfNonEmpty("Search ", fields.Pattern)
	case CatNetwork:
		return prefixIfNonEmpty("Fetch ", fields.URL)
	case CatOther:
		return ""
	}
	return ""
}

func terminalToolTitle(fields *toolInputFields, viewTitle, metaTool string) string {
	if fields.Command != "" {
		return fields.Command
	}
	if viewTitle != "" && viewTitle != metaTool {
		return viewTitle
	}
	return ""
}

func fileToolTitle(cat ToolCategory, path string) string {
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	if cat == CatRead {
		return "Read " + base
	}
	return "Edit " + base
}

func prefixIfNonEmpty(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

type toolInputFields struct {
	Description string `json:"description"`
	Command     string `json:"command"`
	FilePath    string `json:"file_path"`
	Path        string `json:"path"`
	Directory   string `json:"directory"`
	SearchPath  string `json:"search_path"`
	Pattern     string `json:"pattern"`
	Query       string `json:"query"`
	URL         string `json:"url"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	Offset      int    `json:"offset"`
	Limit       int    `json:"limit"`
}

func parseToolInput(raw string) toolInputFields {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return toolInputFields{}
	}
	var f toolInputFields
	if json.Unmarshal([]byte(raw), &f) != nil {
		return toolInputFields{}
	}
	if f.FilePath == "" {
		f.FilePath = f.Path
	}
	if f.Pattern == "" {
		f.Pattern = f.Query
	}
	return f
}

func fileChangePath(it *session.Item) string {
	view := it.Display()
	if view.Title != "" {
		return view.Title
	}
	if len(view.Files) > 0 && view.Files[0].Header != "" {
		if after, ok := strings.CutPrefix(view.Files[0].Header, "diff --git "); ok {
			parts := strings.Fields(after)
			if len(parts) >= 2 {
				return strings.TrimPrefix(parts[1], "b/")
			}
		}
		return view.Files[0].Header
	}
	for line := range strings.SplitSeq(view.Text, "\n") {
		if p, ok := strings.CutPrefix(line, "+++ "); ok {
			p = strings.TrimSpace(p)
			p = strings.TrimPrefix(p, "b/")
			if p != "/dev/null" && p != "" {
				return p
			}
		}
	}
	return "file"
}

func subagentTitle(it *session.Item) string {
	v := it.Display()
	if v.Input != "" {
		return v.Input
	}
	if v.Title != "" {
		return v.Title
	}
	if v.Text != "" {
		first, _, _ := strings.Cut(v.Text, "\n")
		return first
	}
	return "Subagent"
}

func commandTitle(it *session.Item) string {
	v := it.Display()
	if v.Title != "" {
		if v.Input != "" {
			return v.Title + " " + v.Input
		}
		return v.Title
	}
	if v.Text != "" {
		return v.Text
	}
	return "/" + it.Metadata.Source
}

func isItemRunning(it *session.Item) bool {
	return it.Metadata.Status == statusPending || it.Metadata.Status == statusRunning
}

func isToolFailed(it *session.Item) bool {
	if it.Kind != kindToolCall {
		return false
	}
	if it.Metadata.Status == statusFailed || it.Metadata.Status == statusDenied {
		return true
	}
	return it.Metadata.ExitCode != nil && *it.Metadata.ExitCode != 0
}
