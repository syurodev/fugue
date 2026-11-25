package appwin

// The window layout document (desktop-app §5.3 "Lưu bố cục", host-api §5.2):
// the app's own versioned JSON stored per window through fugue.appstate.v1.
// It survives restarts and restores while the vault is still locked, so it
// carries only what the shell needs to rebuild itself: each slot's object
// kind and id, the split weights, and which slot is filled. No transcript,
// title, payload or secret ever enters the document.

import (
	"encoding/json"
	"fmt"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// LayoutSchemaVersion is the document version this build writes and reads. A
// stored document with a higher version falls back to the default layout in
// memory and is not overwritten until the user edits the layout (host-api
// §5.2).
const LayoutSchemaVersion = 1

// SlotKind names the kind of object a slot refers to (host-api §5.2: kind
// and id only). The module id is opaque to the app; the module contract page
// is written before the first module item.
type SlotKind string

// The slot kinds. settings extends the host-api list with a window-level
// kind, which the host never interprets.
const (
	KindSession  SlotKind = "session"
	KindTeam     SlotKind = "team"
	KindAgent    SlotKind = "agent"
	KindDiff     SlotKind = "diff"
	KindInbox    SlotKind = "inbox"
	KindAccounts SlotKind = "accounts"
	KindSettings SlotKind = "settings"
	KindSecrets  SlotKind = "secrets"
	KindModule   SlotKind = "module"
)

// SlotRef is what a slot shows: the object's kind and id. Window-level kinds
// (inbox, accounts, settings, secrets) carry an empty id.
type SlotRef struct {
	Kind SlotKind
	ID   string
}

// slotKey builds a slot's cell key, "col,row"; the document's FilledSlot and
// the view state address slots by it. The main slot is always "0,0".
func slotKey(col, row int) string { return fmt.Sprintf("%d,%d", col, row) }

// parseSlotKey splits a cell key back into its coordinates; ok is false for
// anything the shell never wrote.
func parseSlotKey(key string) (col, row int, ok bool) {
	var c, r int
	if _, err := fmt.Sscanf(key, "%d,%d", &c, &r); err != nil {
		return 0, 0, false
	}
	return c, r, true
}

// Slot is one grid cell: the object it shows and its height weight within
// its column.
type Slot struct {
	Ref          SlotRef
	HeightWeight float32
}

// Column is one grid column: its width weight relative to the other columns
// and its one or two slots. Column 0 always starts with the main slot.
type Column struct {
	WidthWeight float32
	Slots       []Slot
}

// WindowGeometry is the window's normal size in system dp and maximized state.
// The compositor position is not exposed by Gio v0.10.3; the multi-window
// item (F10.21) revisits.
type WindowGeometry struct {
	WidthDp   int
	HeightDp  int
	Maximized bool
}

// LayoutDoc is the persisted layout of one window.
type LayoutDoc struct {
	SchemaVersion    int
	SidebarCollapsed bool
	// FilledSlot is the cell key of the slot filling the whole grid, empty
	// when the grid is ordinary.
	FilledSlot string
	Columns    []Column
	Window     WindowGeometry
	TeamRead   map[string]map[string]int64
}

// DefaultLayout is the layout a window with no stored document opens with:
// the main slot alone in the first column.
func DefaultLayout() LayoutDoc {
	return LayoutDoc{
		SchemaVersion: LayoutSchemaVersion,
		Columns: []Column{{
			WidthWeight: 1,
			Slots: []Slot{
				{HeightWeight: 1, Ref: SlotRef{Kind: KindSession}},
			},
		}},
	}
}

// layoutWire is the JSON shape of LayoutDoc. Weights round-trip as floats.
type layoutWire struct {
	TeamRead         map[string]map[string]int64 `json:"team_read,omitempty"`
	SchemaVersion    int                         `json:"schema_version"`
	SidebarCollapsed bool                        `json:"sidebar_collapsed,omitempty"`
	FilledSlot       string                      `json:"filled_slot,omitempty"`
	Columns          []struct {
		WidthWeight float32 `json:"width_weight"`
		Slots       []struct {
			HeightWeight float32 `json:"height_weight"`
			Kind         string  `json:"kind"`
			ID           string  `json:"id,omitempty"`
		} `json:"slots"`
	} `json:"columns"`
	Window *struct {
		WidthDp   int  `json:"width_dp,omitempty"`
		HeightDp  int  `json:"height_dp,omitempty"`
		Maximized bool `json:"maximized,omitempty"`
	} `json:"window,omitempty"`
}

// EncodeLayout marshals doc into the versioned envelope fugue.appstate.v1
// stores.
func EncodeLayout(doc LayoutDoc) ([]byte, error) {
	wire := layoutWire{
		SchemaVersion:    LayoutSchemaVersion,
		SidebarCollapsed: doc.SidebarCollapsed,
		FilledSlot:       doc.FilledSlot,
		TeamRead:         doc.TeamRead,
	}
	for _, col := range doc.Columns {
		wc := struct {
			WidthWeight float32 `json:"width_weight"`
			Slots       []struct {
				HeightWeight float32 `json:"height_weight"`
				Kind         string  `json:"kind"`
				ID           string  `json:"id,omitempty"`
			} `json:"slots"`
		}{WidthWeight: col.WidthWeight}
		for _, slot := range col.Slots {
			wc.Slots = append(wc.Slots, struct {
				HeightWeight float32 `json:"height_weight"`
				Kind         string  `json:"kind"`
				ID           string  `json:"id,omitempty"`
			}{HeightWeight: slot.HeightWeight, Kind: string(slot.Ref.Kind), ID: slot.Ref.ID})
		}
		wire.Columns = append(wire.Columns, wc)
	}
	if doc.Window.WidthDp > 0 || doc.Window.HeightDp > 0 || doc.Window.Maximized {
		wire.Window = &struct {
			WidthDp   int  `json:"width_dp,omitempty"`
			HeightDp  int  `json:"height_dp,omitempty"`
			Maximized bool `json:"maximized,omitempty"`
		}{WidthDp: doc.Window.WidthDp, HeightDp: doc.Window.HeightDp, Maximized: doc.Window.Maximized}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode the window layout: %w", err)
	}
	return raw, nil
}

// DecodeLayoutResult reports a decoded document plus the higher-schema flag.
type DecodeLayoutResult struct {
	Doc          LayoutDoc
	HigherSchema bool
}

// DecodeLayout reads a stored layout document. A document whose
// schema_version exceeds LayoutSchemaVersion decodes as DefaultLayout with
// HigherSchema set, so the caller runs on the default and leaves the stored
// document alone until the user edits the layout (host-api §5.2). A document
// below the version, or one that fails the shell's own invariants, converts
// forward: unknown kinds and broken cells are dropped, never fatal — the
// stored document is re-saved in the understood shape on the next edit.
func DecodeLayout(raw []byte) (DecodeLayoutResult, error) {
	if len(raw) == 0 {
		return DecodeLayoutResult{Doc: DefaultLayout()}, nil
	}
	var wire layoutWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return DecodeLayoutResult{}, fmt.Errorf("decode the window layout: %w", err)
	}
	if wire.SchemaVersion > LayoutSchemaVersion {
		return DecodeLayoutResult{Doc: DefaultLayout(), HigherSchema: true}, nil
	}
	doc := LayoutDoc{
		SchemaVersion:    LayoutSchemaVersion,
		SidebarCollapsed: wire.SidebarCollapsed,
		FilledSlot:       wire.FilledSlot,
		TeamRead:         wire.TeamRead,
	}
	for _, wc := range wire.Columns {
		col := Column{WidthWeight: wc.WidthWeight}
		for _, ws := range wc.Slots {
			if !isSlotKind(SlotKind(ws.Kind)) {
				continue
			}
			col.Slots = append(col.Slots, Slot{
				HeightWeight: ws.HeightWeight,
				Ref:          SlotRef{Kind: SlotKind(ws.Kind), ID: ws.ID},
			})
		}
		if len(col.Slots) > 0 {
			doc.Columns = append(doc.Columns, col)
		}
	}
	repairLayout(&doc)
	if wire.Window != nil {
		doc.Window = WindowGeometry{
			WidthDp:   wire.Window.WidthDp,
			HeightDp:  wire.Window.HeightDp,
			Maximized: wire.Window.Maximized,
		}
	}
	return DecodeLayoutResult{Doc: doc}, nil
}

// isSlotKind reports whether kind is one the shell renders.
func isSlotKind(kind SlotKind) bool {
	switch kind {
	case KindSession, KindTeam, KindAgent, KindDiff, KindInbox,
		KindAccounts, KindSettings, KindSecrets, KindModule:
		return true
	case SlotKind(""):
		return false
	}
	return false
}

// repairLayout re-establishes the shell's invariants on a decoded document:
// the main slot exists at (0,0), no column exceeds two rows, at most three
// columns survive, every weight is positive, and a filled slot that no
// longer names a cell clears. Decoding never fails the window over a broken
// stored layout; it repairs and carries on.
func repairLayout(doc *LayoutDoc) {
	if len(doc.Columns) == 0 {
		doc.Columns = DefaultLayout().Columns
		return
	}
	for i := range doc.Columns {
		col := &doc.Columns[i]
		if len(col.Slots) > maxRowsPerColumn {
			col.Slots = col.Slots[:maxRowsPerColumn]
		}
		for j := range col.Slots {
			col.Slots[j].HeightWeight = positiveWeight(col.Slots[j].HeightWeight)
		}
		col.WidthWeight = positiveWeight(col.WidthWeight)
	}
	if len(doc.Columns) > maxGridColumns {
		doc.Columns = doc.Columns[:maxGridColumns]
	}
	if len(doc.Columns[0].Slots) == 0 || !isWorkViewKind(doc.Columns[0].Slots[0].Ref.Kind) {
		doc.Columns[0].Slots[0] = Slot{HeightWeight: 1, Ref: SlotRef{Kind: KindSession}}
	}
	doc.Columns[0].Slots[0].HeightWeight = positiveWeight(doc.Columns[0].Slots[0].HeightWeight)
	if _, _, ok := parseSlotKey(doc.FilledSlot); !ok {
		doc.FilledSlot = ""
	}
}

// positiveWeight turns a missing or negative weight into 1; weights are
// relative, so any positive value preserves the stored proportions.
func positiveWeight(w float32) float32 {
	if w > 0 {
		return w
	}
	return 1
}

// isWorkViewKind reports whether kind can sit in the main slot: the rail
// item's work views — a session chat, a team board, a module workbench.
func isWorkViewKind(kind SlotKind) bool {
	switch kind {
	case KindSession, KindTeam, KindModule:
		return true
	case KindAgent, KindDiff, KindInbox, KindAccounts, KindSettings, KindSecrets, SlotKind(""):
		return false
	}
	return false
}

// layoutMinWidthDp is the smallest width the shell needs for cols columns
// (desktop-app §5.3, FUG-94): permanent Left Rail + Right Stripe, optional
// Sidebar Island + IslandGap, main slot minimum, and regular slot minimums +
// IslandGap gutters.
func layoutMinWidthDp(cols int, sidebar bool) int {
	w := int(theme.RailWidth) + int(theme.RightStripeWidth) + int(theme.MainSlotMinWidth)
	if sidebar {
		w += int(theme.SidebarWidth) + int(theme.IslandGap)
	}
	if cols > 1 {
		w += (cols - 1) * (int(theme.SlotMinWidth) + int(theme.IslandGap))
	}
	return w
}
