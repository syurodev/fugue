package snapshot

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// ActivityKind names the kind of live workspace operation recorded in Activity.
type ActivityKind string

const (
	// ActivityTurn marks a turn running in a workspace.
	ActivityTurn ActivityKind = "turn"
	// ActivityTool marks a side-effecting tool running in a workspace.
	ActivityTool ActivityKind = "tool"
	// ActivityRestore marks a snapshot restore in progress.
	ActivityRestore ActivityKind = "restore"
	// ActivityRewind marks a rewind in progress.
	ActivityRewind ActivityKind = "rewind"

	spanHistory = 6 * time.Hour
)

// Span records one period during which a session was active in a project. A
// zero End means the span is still running.
type Span struct {
	Project string       `json:"project"`
	Session string       `json:"session"`
	Kind    ActivityKind `json:"kind"`
	Start   time.Time    `json:"start"`
	End     time.Time    `json:"end,omitempty"`
}

// Running reports whether the span has not yet ended.
func (s *Span) Running() bool {
	return s.End.IsZero()
}

// Activity is the in-memory registry of running and recently finished spans.
type Activity struct {
	mu    sync.Mutex
	spans []*Span
	clock clock.Clock
}

// NewActivity creates a new Activity registry reading time from clk.
func NewActivity(clk clock.Clock) *Activity {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Activity{clock: clk}
}

// Handle closes the span that Begin opened. Calling End more than once, or on a
// nil Handle, is a no-op.
type Handle struct {
	activity *Activity
	span     *Span
}

// Begin records that session has started an operation of kind in project and
// returns a Handle whose End marks the finish.
func (a *Activity) Begin(project, session string, kind ActivityKind) *Handle {
	norm := normaliseProject(project)
	now := a.clock.Now()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.trimLocked(now)

	span := &Span{
		Project: norm,
		Session: session,
		Kind:    kind,
		Start:   now,
	}
	a.spans = append(a.spans, span)
	return &Handle{activity: a, span: span}
}

// End marks the span finished. Safe on a nil Handle and idempotent.
func (h *Handle) End() {
	if h == nil || h.activity == nil || h.span == nil {
		return
	}
	now := h.activity.clock.Now()
	h.activity.mu.Lock()
	defer h.activity.mu.Unlock()
	if h.span.End.IsZero() {
		h.span.End = now
	}
}

// Span returns a copy of the underlying span.
func (h *Handle) Span() Span {
	if h == nil || h.span == nil {
		return Span{}
	}
	h.activity.mu.Lock()
	defer h.activity.mu.Unlock()
	return *h.span
}

// RunningIn returns the spans currently open in project, excluding any session
// listed in excludeSessions.
func (a *Activity) RunningIn(project string, excludeSessions ...string) []Span {
	norm := normaliseProject(project)
	excluded := toSet(excludeSessions)

	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Span
	for _, span := range a.spans {
		if span.Project == norm && span.Running() && !excluded[span.Session] {
			out = append(out, *span)
		}
	}
	return out
}

// Overlap returns the spans in project whose [Start, End] window intersects
// [from, to], excluding any session listed in excludeSessions.
func (a *Activity) Overlap(project string, from, to time.Time, excludeSessions ...string) []Span {
	norm := normaliseProject(project)
	excluded := toSet(excludeSessions)
	now := a.clock.Now()
	if to.IsZero() {
		to = now
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Span
	for _, span := range a.spans {
		if span.Project != norm || excluded[span.Session] {
			continue
		}
		end := span.End
		if end.IsZero() {
			end = now
		}
		if !span.Start.After(to) && !end.Before(from) {
			out = append(out, *span)
		}
	}
	return out
}

func (a *Activity) trimLocked(now time.Time) {
	cutoff := now.Add(-spanHistory)
	kept := a.spans[:0]
	for _, span := range a.spans {
		if span.Running() || span.End.After(cutoff) {
			kept = append(kept, span)
		}
	}
	a.spans = kept
}

func normaliseProject(project string) string {
	trimmed := strings.TrimSpace(project)
	if trimmed == "" {
		return ""
	}
	if _, resolved, err := ProjectKey(trimmed); err == nil {
		return resolved
	}
	if abs, err := filepath.Abs(trimmed); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(trimmed)
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]bool, len(items))
	for _, item := range items {
		if item != "" {
			out[item] = true
		}
	}
	return out
}
