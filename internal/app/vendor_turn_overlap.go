package app

// The overlap registry of turn_diff (git-guard §3.2, GG-04, VR-14): a turn is
// overlapped when a turn of another session on the same working directory was
// open at some point while it was. Fugue does not guess whose change is whose,
// it only says the diff may hold another session's work. Owner: application
// layer. Allowed callers: the vendor session actors.

import (
	"path/filepath"
	"sync"

	"github.com/syurodev/fugue/internal/snapshot"
)

// overlapTurn identifies one open turn.
type overlapTurn struct {
	session string
	turn    string
}

// turnOverlaps tracks the open turns per working directory. A turn is open
// from the moment its start snapshot is done until its end snapshot is done, so
// two turns overlap exactly when their intervals intersect. Opening a turn
// marks it and every open turn of another session in the same directory; the
// mark is read, and the entry dropped, when the turn ends. Entries live only
// while a turn does: a session that ends drops its own.
//
// The directory is the one the shadow repository is keyed by (the real path),
// so a parent or a nested directory is a different directory: the shadow
// repositories differ and so do the diffs.
type turnOverlaps struct {
	mu   sync.Mutex
	open map[string]map[overlapTurn]bool
}

func newTurnOverlaps() *turnOverlaps {
	return &turnOverlaps{open: make(map[string]map[overlapTurn]bool)}
}

// overlapDir is the registry key of a working directory.
func overlapDir(workDir string) string {
	if _, resolved, err := snapshot.ProjectKey(workDir); err == nil {
		return resolved
	}
	return filepath.Clean(workDir)
}

// begin registers an open turn and marks it, and the turns it meets, as
// overlapped. A session never overlaps itself.
func (o *turnOverlaps) begin(workDir, session, turn string) {
	if workDir == "" {
		return
	}
	dir := overlapDir(workDir)
	self := overlapTurn{session: session, turn: turn}
	o.mu.Lock()
	defer o.mu.Unlock()
	turns := o.open[dir]
	if turns == nil {
		turns = make(map[overlapTurn]bool)
		o.open[dir] = turns
	}
	met := false
	for other := range turns {
		if other.session != session {
			turns[other] = true
			met = true
		}
	}
	turns[self] = met
}

// end drops an open turn and reports whether it was overlapped.
func (o *turnOverlaps) end(workDir, session, turn string) bool {
	if workDir == "" {
		return false
	}
	dir := overlapDir(workDir)
	o.mu.Lock()
	defer o.mu.Unlock()
	turns := o.open[dir]
	self := overlapTurn{session: session, turn: turn}
	overlapped := turns[self]
	delete(turns, self)
	if len(turns) == 0 {
		delete(o.open, dir)
	}
	return overlapped
}

// endSession drops every open turn of a session, for a turn that never ended.
func (o *turnOverlaps) endSession(session string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for dir, turns := range o.open {
		for turn := range turns {
			if turn.session == session {
				delete(turns, turn)
			}
		}
		if len(turns) == 0 {
			delete(o.open, dir)
		}
	}
}

// openCount is the number of open turns, for tests that check nothing leaks.
func (o *turnOverlaps) openCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, turns := range o.open {
		n += len(turns)
	}
	return n
}
