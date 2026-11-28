package workspacememory

import (
	"log/slog"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Kind classifies a workspace memory entry (§2).
type Kind string

// Valid workspace memory entry kinds (§2).
const (
	KindPinned     Kind = "pinned"
	KindFact       Kind = "fact"
	KindDecision   Kind = "decision"
	KindConvention Kind = "convention"
	KindHowto      Kind = "howto"
	KindNote       Kind = "note"
)

// Author identifies whether a write came from a user or an agent (§2, WM-03).
type Author string

// Valid workspace memory entry authors.
const (
	AuthorUser  Author = "user"
	AuthorAgent Author = "agent"
)

// Validity identifies the lifecycle status of a workspace memory entry (§2).
type Validity string

// Valid workspace memory entry validity states.
const (
	ValidityValid      Validity = "valid"
	ValiditySuperseded Validity = "superseded"
	ValidityInvalid    Validity = "invalid"
)

// Freshness reports anchor verification state computed on read (§2).
type Freshness string

// Possible anchor freshness states evaluated on read.
const (
	FreshnessFresh   Freshness = "fresh"
	FreshnessStale   Freshness = "stale"
	FreshnessUnknown Freshness = "unknown"
)

// Operation names the write operation recorded in history and events (§3).
type Operation string

// Write operations recorded in workspace_memory_history and MemoryEntryWritten events.
const (
	OpAdd        Operation = "add"
	OpUpdate     Operation = "update"
	OpInvalidate Operation = "invalidate"
	OpRestore    Operation = "restore"
	OpVerify     Operation = "verify"
)

// Field and rate limits (§2, §3).
const (
	MaxLabelRunes             = 128
	MaxSummaryRunes           = 200
	MaxBodyBytes              = 8 * 1024
	MaxPinnedBodyBytes        = 2 * 1024
	MaxKeywords               = 20
	MaxKeywordRunes           = 40
	DefaultWritesPerSessionHr = 60
	DefaultMaxValidEntries    = 2000
	DefaultLanguageMinWords   = 6
	DefaultLanguageMaxNonEn   = 0.60
	EventMemoryEntryWritten   = "MemoryEntryWritten"
)

// Config configures rate, capacity, and language check thresholds.
type Config struct {
	MaxWritesPerSessionHour     int
	MaxValidEntriesPerWorkspace int
	LanguageMinWords            int
	LanguageMaxNonEnglishRatio  float64
	Logger                      *slog.Logger
}

// AnchorInput identifies a workspace-relative file supplied by a caller.
type AnchorInput struct {
	RootName string `json:"root_name"`
	RelPath  string `json:"rel_path"`
}

// Anchor is a persisted file anchor with its SHA-256 content hash at write time.
type Anchor struct {
	RootName    string `json:"root_name"`
	RelPath     string `json:"rel_path"`
	ContentHash string `json:"content_hash"`
}

// AnchorStatus reports the persisted anchor and its freshness at read time.
type AnchorStatus struct {
	RootName    string    `json:"root_name"`
	RelPath     string    `json:"rel_path"`
	ContentHash string    `json:"content_hash"`
	Freshness   Freshness `json:"freshness"`
}

// Provenance records the origin of the latest write (§2).
type Provenance struct {
	Principal  string `json:"principal,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Role       string `json:"role,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	ImportFile string `json:"import_file,omitempty"`
}

// Entry is a workspace memory entry returned on read and conflict responses.
type Entry struct {
	ID             string         `json:"id"`
	WorkspaceID    string         `json:"workspace_id"`
	Kind           Kind           `json:"kind"`
	Label          string         `json:"label"`
	Summary        string         `json:"summary"`
	Body           string         `json:"body"`
	Keywords       []string       `json:"keywords"`
	Anchors        []Anchor       `json:"anchors"`
	AnchorStatuses []AnchorStatus `json:"anchor_statuses"`
	Author         Author         `json:"author"`
	Verified       bool           `json:"verified"`
	Validity       Validity       `json:"validity"`
	InvalidReason  string         `json:"invalid_reason"`
	SupersededBy   string         `json:"superseded_by"`
	Provenance     Provenance     `json:"provenance"`
	Revision       uint64         `json:"revision"`
	UpdatedAt      time.Time      `json:"updated_at"`
	Freshness      Freshness      `json:"freshness"`
}

// HistoryEntry is one immutable revision snapshot from workspace_memory_history.
type HistoryEntry struct {
	HistoryID     int64      `json:"history_id"`
	EntryID       string     `json:"entry_id"`
	WorkspaceID   string     `json:"workspace_id"`
	Revision      uint64     `json:"revision"`
	Op            Operation  `json:"op"`
	Kind          Kind       `json:"kind"`
	Label         string     `json:"label"`
	Summary       string     `json:"summary"`
	Body          string     `json:"body"`
	Keywords      []string   `json:"keywords"`
	Anchors       []Anchor   `json:"anchors"`
	Author        Author     `json:"author"`
	Verified      bool       `json:"verified"`
	Validity      Validity   `json:"validity"`
	InvalidReason string     `json:"invalid_reason"`
	SupersededBy  string     `json:"superseded_by"`
	Provenance    Provenance `json:"provenance"`
	SessionID     string     `json:"session_id"`
	WrittenAt     time.Time  `json:"written_at"`
}

// AddInput holds fields for creating a new workspace memory entry.
type AddInput struct {
	WorkspaceID string
	Kind        Kind
	Label       string
	Summary     string
	Body        string
	Keywords    []string
	Anchors     []AnchorInput
	Author      Author
	Provenance  Provenance
}

// UpdateInput holds fields for updating an existing workspace memory entry under CAS.
type UpdateInput struct {
	WorkspaceID      string
	ID               string
	ExpectedRevision uint64
	Kind             Kind
	Label            string
	Summary          string
	Body             string
	Keywords         []string
	Anchors          []AnchorInput
	Author           Author
	Provenance       Provenance
}

// InvalidateInput marks an entry as invalid or superseded under CAS.
type InvalidateInput struct {
	WorkspaceID      string
	ID               string
	ExpectedRevision uint64
	Reason           string
	SupersededBy     string
	Author           Author
	Provenance       Provenance
}

// RestoreInput restores an entry's fields from a prior history revision as a user write.
type RestoreInput struct {
	WorkspaceID      string
	ID               string
	RestoreRevision  uint64
	ExpectedRevision uint64
	Provenance       Provenance
}

// VerifyInput sets verified=true on an entry under CAS.
type VerifyInput struct {
	WorkspaceID      string
	ID               string
	ExpectedRevision uint64
	Provenance       Provenance
}

// ListFilter filters workspace memory entries returned by List.
type ListFilter struct {
	WorkspaceID string
	Kind        Kind
	Verified    *bool
	Author      Author
	Validity    Validity
}

// WrittenEvent is emitted after every committed write without any entry content (§3).
type WrittenEvent struct {
	EntryID         string    `json:"entry_id"`
	WorkspaceID     string    `json:"workspace_id"`
	Revision        uint64    `json:"revision"`
	Author          Author    `json:"author"`
	Op              Operation `json:"op"`
	UnverifiedCount int       `json:"unverified_count"`
}

func validKind(k Kind) bool {
	switch k {
	case KindPinned, KindFact, KindDecision, KindConvention, KindHowto, KindNote:
		return true
	default:
		return false
	}
}

func validAuthor(a Author) bool {
	return a == AuthorUser || a == AuthorAgent
}

func validValidity(v Validity) bool {
	return v == ValidityValid || v == ValiditySuperseded || v == ValidityInvalid
}

func normalizeConfig(cfg Config) (Config, error) {
	if cfg.MaxWritesPerSessionHour < 0 || cfg.MaxValidEntriesPerWorkspace < 0 || cfg.LanguageMinWords < 0 || cfg.LanguageMaxNonEnglishRatio < 0 || cfg.LanguageMaxNonEnglishRatio > 1 {
		return Config{}, errcode.New(errcode.InvalidArgument, "workspace memory config values must be non-negative and ratio in [0, 1]")
	}
	if cfg.MaxWritesPerSessionHour == 0 {
		cfg.MaxWritesPerSessionHour = DefaultWritesPerSessionHr
	}
	if cfg.MaxValidEntriesPerWorkspace == 0 {
		cfg.MaxValidEntriesPerWorkspace = DefaultMaxValidEntries
	}
	if cfg.LanguageMinWords == 0 {
		cfg.LanguageMinWords = DefaultLanguageMinWords
	}
	if cfg.LanguageMaxNonEnglishRatio == 0 {
		cfg.LanguageMaxNonEnglishRatio = DefaultLanguageMaxNonEn
	}
	return cfg, nil
}
