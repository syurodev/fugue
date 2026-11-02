package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

const (
	// DefaultTurnRetentionDays is how long turn snapshots are kept before
	// maintenance sweeps them.
	DefaultTurnRetentionDays = 30
	// DefaultToolRetentionDays is how long tool before/after snapshots are
	// kept before maintenance sweeps them.
	DefaultToolRetentionDays = 7
	// DefaultSafetyRetentionDays is how long safety snapshots taken before
	// a restore or rewind are kept.
	DefaultSafetyRetentionDays = 7
	// DefaultMaxTurnsPerSession caps the number of turn snapshots retained
	// for a single session.
	DefaultMaxTurnsPerSession = 200
	// DefaultMaxToolsPerSession caps the circular buffer of tool snapshots
	// retained for a single session.
	DefaultMaxToolsPerSession = 500
	// DefaultMaxProjectBytes is the shared soft ceiling on the disk a
	// single project's shadow repository may use (1 GiB).
	DefaultMaxProjectBytes int64 = 1 << 30
	// DefaultMaxFileBytes is the largest single file a snapshot will put in
	// its tree (10 MiB).
	DefaultMaxFileBytes int64 = 10 << 20
	// DefaultMaxFiles is the ceiling on the number of eligible files in a
	// project before Snapshot refuses the project as too large.
	DefaultMaxFiles = 200_000
	// DefaultGitTimeout bounds a single git command when the caller's
	// context carries no deadline of its own.
	DefaultGitTimeout = 30 * time.Second

	// SlotBefore names the ref pointing at the tree before an edit.
	SlotBefore = "before"
	// SlotAfter names the ref pointing at the tree after an edit.
	SlotAfter = "after"

	refPrefix       = "refs/fugue/cp/"
	projectFileName = "fugue-project"
	projectKeyBytes = 12
	goosDarwin      = "darwin"
	gitTrue         = "true"
	gitFalse        = "false"
)

// Sentinel errors returned by snapshot operations.
var (
	// ErrGitUnavailable reports that no usable git binary was found.
	ErrGitUnavailable = errors.New("snapshot: git is unavailable")
	// ErrProjectTooLarge reports that a workspace exceeds MaxFiles.
	ErrProjectTooLarge = errors.New("snapshot: project exceeds the file ceiling")
	// ErrPlanStale reports that the workspace changed between PlanRestore
	// and ApplyRestore.
	ErrPlanStale = errors.New("snapshot: files changed since the restore plan was built")
	// ErrTreeMissing reports an empty or unknown git tree hash.
	ErrTreeMissing = errors.New("snapshot: tree hash is missing")
	// ErrSnapshotNotFound reports an unknown snapshot ID in the store.
	ErrSnapshotNotFound = errors.New("snapshot: snapshot ID not found")
	// ErrDiffParse reports malformed git diff-tree output.
	ErrDiffParse = errors.New("snapshot: unreadable git diff output")
	// ErrGitProbe reports a candidate git binary that failed version probing.
	ErrGitProbe = errors.New("snapshot: git binary probe failed")
	// ErrEmptyProjectMarker reports a shadow repository with an empty marker file.
	ErrEmptyProjectMarker = errors.New("snapshot: project marker is empty")
	// ErrRestoreConflict reports an attempt to force a non-overridable conflict.
	ErrRestoreConflict = errors.New("snapshot: refusing to restore non-overridable conflict")
	// ErrTreeParse reports malformed git ls-tree output.
	ErrTreeParse = errors.New("snapshot: unreadable ls-tree output")
)

// Limits holds the tunables for the snapshot engine. Zero values fall back to
// the package defaults.
type Limits struct {
	TurnRetentionDays  int32
	ToolRetentionDays  int32
	MaxTurnsPerSession int32
	MaxToolsPerSession int32
	MaxFiles           int32
	MaxProjectBytes    int64
	MaxFileBytes       int64
	GitTimeout         time.Duration
	ExtraExcludes      []string
}

// DefaultLimits returns a Limits struct populated with the package defaults.
func DefaultLimits() Limits {
	return Limits{}.withDefaults()
}

func (l Limits) withDefaults() Limits {
	if l.TurnRetentionDays <= 0 {
		l.TurnRetentionDays = DefaultTurnRetentionDays
	}
	if l.ToolRetentionDays <= 0 {
		l.ToolRetentionDays = DefaultToolRetentionDays
	}
	if l.MaxTurnsPerSession <= 0 {
		l.MaxTurnsPerSession = DefaultMaxTurnsPerSession
	}
	if l.MaxToolsPerSession <= 0 {
		l.MaxToolsPerSession = DefaultMaxToolsPerSession
	}
	if l.MaxProjectBytes <= 0 {
		l.MaxProjectBytes = DefaultMaxProjectBytes
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = DefaultMaxFileBytes
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultMaxFiles
	}
	if l.GitTimeout <= 0 {
		l.GitTimeout = DefaultGitTimeout
	}
	return l
}

// SkipReason explains why a path was left out of a snapshot tree.
type SkipReason string

const (
	// SkipTooLarge means the file exceeded MaxFileBytes.
	SkipTooLarge SkipReason = "too_large"
	// SkipNestedRepo means the directory has its own .git entry.
	SkipNestedRepo SkipReason = "nested_repo"
	// SkipExcluded means the path matched the built-in or configured deny list.
	SkipExcluded SkipReason = "excluded"
	// SkipUnreadable means the path disappeared or could not be stat'd.
	SkipUnreadable SkipReason = "unreadable"
)

// Skip records one path that a snapshot intentionally omitted.
type Skip struct {
	Path   string     `json:"path"`
	Reason SkipReason `json:"reason"`
	Bytes  int64      `json:"bytes,omitempty"`
}

// Snapshot is the outcome of a single Snapshot call.
type Snapshot struct {
	Tree    string        `json:"tree"`
	Skipped []Skip        `json:"skipped,omitempty"`
	Elapsed time.Duration `json:"elapsed"`
}

func defaultExcludedDirs() []string {
	return []string{
		".git",
		"node_modules",
		"dist",
		"build",
		"target",
		"out",
		".next",
		".nuxt",
		".svelte-kit",
		".turbo",
		".angular",
		".parcel-cache",
		".cache",
		"coverage",
		".nyc_output",
		".venv",
		"venv",
		"__pycache__",
		".pytest_cache",
		".mypy_cache",
		".ruff_cache",
		".tox",
		".gradle",
		".idea",
		"Pods",
		"DerivedData",
		".terraform",
		".direnv",
	}
}

func defaultExcludedFiles() []string {
	return []string{".DS_Store", "Thumbs.db"}
}

// denyPatterns builds the gitignore-syntax patterns handed to git's -x flag.
func denyPatterns(extra []string) []string {
	dirs := defaultExcludedDirs()
	files := defaultExcludedFiles()
	out := make([]string, 0, len(dirs)+len(files)+len(extra))
	for _, dir := range dirs {
		out = append(out, dir+"/")
	}
	out = append(out, files...)
	for _, raw := range extra {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// Engine manages the per-project shadow repositories under a single root
// directory.
type Engine struct {
	rootDir  string
	git      Availability
	limits   Limits
	clock    clock.Clock
	ids      ids.Generator
	activity *Activity

	mu    sync.Mutex
	repos map[string]*Repo
}

// New creates an Engine rooted at rootDir using the real clock and UUIDv7 ID
// generator.
func New(rootDir string, git Availability, limits Limits) *Engine {
	clk := clock.Real{}
	return NewWithDeps(rootDir, git, limits, clk, ids.NewUUIDv7(clk, ids.CryptoRandom{}))
}

// NewWithDeps creates an Engine with an explicit clock and ID generator.
func NewWithDeps(rootDir string, git Availability, limits Limits, clk clock.Clock, idGen ids.Generator) *Engine {
	if clk == nil {
		clk = clock.Real{}
	}
	if idGen == nil {
		idGen = ids.NewUUIDv7(clk, ids.CryptoRandom{})
	}
	return &Engine{
		rootDir:  rootDir,
		git:      git,
		limits:   limits.withDefaults(),
		clock:    clk,
		ids:      idGen,
		activity: NewActivity(clk),
		repos:    make(map[string]*Repo),
	}
}

// Activity returns the engine's live-activity registry.
func (e *Engine) Activity() *Activity { return e.activity }

// Availability returns the git availability the engine was constructed with.
func (e *Engine) Availability() Availability { return e.git }

// Limits returns the effective limits in use by the engine.
func (e *Engine) Limits() Limits { return e.limits }

// Open returns the shadow repository for the project at workTree, creating and
// configuring it on disk if needed.
func (e *Engine) Open(ctx context.Context, workTree string) (*Repo, error) {
	if !e.git.OK() {
		return nil, fmt.Errorf("%w: %s", ErrGitUnavailable, e.git.Reason)
	}
	key, resolved, err := ProjectKey(workTree)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	repo, ok := e.repos[key]
	if !ok {
		repo = &Repo{
			key:      key,
			workTree: resolved,
			gitDir:   filepath.Join(e.rootDir, key),
			gitPath:  e.git.Path,
			limits:   e.limits,
			clock:    e.clock,
			ids:      e.ids,
			gate:     make(chan struct{}, 1),
		}
		repo.gate <- struct{}{}
		e.repos[key] = repo
	}
	e.mu.Unlock()
	if err := repo.ensureInitialised(ctx); err != nil {
		return nil, err
	}
	return repo, nil
}

// ProjectKey returns the stable directory key and canonical path for workTree.
func ProjectKey(workTree string) (key, resolved string, err error) {
	trimmed := strings.TrimSpace(workTree)
	if trimmed == "" {
		return "", "", errcode.New(errcode.InvalidArgument, "snapshot: project path is empty")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("snapshot: resolve project path: %w", err)
	}
	realPath, evalErr := filepath.EvalSymlinks(abs)
	if evalErr != nil {
		realPath = filepath.Clean(abs)
	}
	info, statErr := os.Stat(realPath)
	if statErr != nil {
		return "", "", fmt.Errorf("snapshot: stat project path: %w", statErr)
	}
	if !info.IsDir() {
		return "", "", errcode.New(errcode.InvalidArgument, "snapshot: project path is not a directory")
	}
	sum := sha256.Sum256([]byte(realPath))
	return hex.EncodeToString(sum[:projectKeyBytes]), realPath, nil
}

// RefName returns the full git ref name that pins one slot of a snapshot.
func RefName(checkpointID, slot string) string {
	return refPrefix + checkpointID + "/" + slot
}
