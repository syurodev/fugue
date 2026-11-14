//go:build unix

package process

import (
	"fmt"
	"slices"
)

// OwnsPID reports whether pid runs inside this process's containment: a
// member of its cgroup scope, or a process the descendant tracker reaches
// from the leader. The Host API uses it to keep a vendor process from acting
// as a local client (roadmap Q-41 A). An unreadable scope or process table,
// and a pid the table does not list, are errors, so the caller fails closed.
func (p *Process) OwnsPID(pid int) (bool, error) {
	if pid <= 1 {
		return false, fmt.Errorf("%w: invalid pid %d", ErrIdentityMismatch, pid)
	}
	if p.scope != nil {
		pids, err := p.scope.readProcs()
		if err != nil {
			return false, err
		}
		return slices.Contains(pids, pid), nil
	}
	if p.tracker == nil {
		return false, fmt.Errorf("%w: the process has no containment record", ErrIdentityMismatch)
	}
	return p.tracker.owns(pid)
}

// owns takes a fresh snapshot, so a child forked since the last tick is still
// reached through its parent chain, and reports whether pid is tracked under
// the start token it has now. A recorded pid whose token changed was reused
// by an unrelated process and is not owned.
func (d *descendantTracker) owns(pid int) (bool, error) {
	entries, err := d.source.scan()
	if err != nil {
		return false, fmt.Errorf("%w: scan the process table: %w", ErrIdentityMismatch, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	mergeDescendants(d.tracked, d.leaderPID, entries)
	for _, e := range entries {
		if e.pid == pid {
			return e.startToken != "" && d.tracked[pid] == e.startToken, nil
		}
	}
	return false, fmt.Errorf("%w: pid %d is not in the process table", ErrIdentityMismatch, pid)
}
