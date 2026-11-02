package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ConflictReason explains why a path could not be restored automatically.
type ConflictReason string

const (
	// ConflictChangedSince means the file on disk no longer matches the
	// snapshot that followed the edit.
	ConflictChangedSince ConflictReason = "changed_since"
	// ConflictAppearedSince means the file was absent in the snapshot that
	// followed the edit, and now exists on disk with different content.
	ConflictAppearedSince ConflictReason = "appeared_since"
	// ConflictUnsafePath means the path or one of its parent directories is
	// a symlink or escapes the project root. Never overridable.
	ConflictUnsafePath ConflictReason = "unsafe_path"
	// ConflictProtected means the path is excluded or exceeded the size
	// ceiling when snapshotted. Never overridable.
	ConflictProtected ConflictReason = "protected"
)

// Overridable reports whether a caller may force the restore of a path that
// conflicted for this reason.
func (r ConflictReason) Overridable() bool {
	return r == ConflictChangedSince || r == ConflictAppearedSince
}

// Conflict describes one path that PlanRestore refused to overwrite without
// confirmation.
type Conflict struct {
	Path   string         `json:"path"`
	Reason ConflictReason `json:"reason"`
}

type entry struct {
	mode    string
	blob    string
	present bool
}

// Action is one file operation that ApplyRestore will perform.
type Action struct {
	Path   string     `json:"path"`
	Kind   ChangeKind `json:"kind"`
	Target entry
	Expect entry
}

// RestorePlan is the dry-run result of PlanRestore.
type RestorePlan struct {
	TargetTree string     `json:"targetTree"`
	ExpectTree string     `json:"expectTree"`
	Actions    []Action   `json:"actions"`
	Conflicts  []Conflict `json:"conflicts,omitempty"`
}

// Empty reports whether the plan has no file actions to apply.
func (p *RestorePlan) Empty() bool {
	return p == nil || len(p.Actions) == 0
}

// PlanRestore compares targetTree with expectTree and the current working tree,
// returning the actions ApplyRestore will take and the conflicts it found.
func (r *Repo) PlanRestore(ctx context.Context, targetTree, expectTree string, only, override []string) (*RestorePlan, error) {
	if targetTree == "" {
		return nil, ErrTreeMissing
	}
	var plan *RestorePlan
	err := r.runLocked(ctx, func() error {
		var planErr error
		plan, planErr = r.planRestoreLocked(ctx, targetTree, expectTree, only, override)
		return planErr
	})
	return plan, err
}

func (r *Repo) planRestoreLocked(ctx context.Context, targetTree, expectTree string, only, override []string) (*RestorePlan, error) {
	if only != nil && len(only) == 0 {
		return &RestorePlan{TargetTree: targetTree, ExpectTree: expectTree}, nil
	}
	targetEntries, err := r.readTree(ctx, targetTree)
	if err != nil {
		return nil, err
	}
	var expectEntries map[string]entry
	if expectTree != "" {
		expectEntries, err = r.readTree(ctx, expectTree)
		if err != nil {
			return nil, err
		}
	}
	candidates := collectPlanCandidates(only, targetEntries, expectEntries)
	forced := toSet(override)
	plan := &RestorePlan{TargetTree: targetTree, ExpectTree: expectTree}

	for _, rel := range candidates {
		action, conflict := r.evaluateCandidate(ctx, rel, targetEntries[rel], expectEntries[rel], expectTree != "", forced)
		if conflict != nil {
			plan.Conflicts = append(plan.Conflicts, *conflict)
		}
		if action != nil {
			plan.Actions = append(plan.Actions, *action)
		}
	}
	return plan, nil
}

func collectPlanCandidates(only []string, targetEntries, expectEntries map[string]entry) []string {
	if only != nil {
		seen := make(map[string]bool, len(only))
		out := make([]string, 0, len(only))
		for _, p := range only {
			clean := filepath.ToSlash(filepath.Clean(p))
			if clean != "." && clean != "" && !seen[clean] {
				seen[clean] = true
				out = append(out, clean)
			}
		}
		sort.Strings(out)
		return out
	}
	union := make(map[string]bool, len(targetEntries)+len(expectEntries))
	for p := range targetEntries {
		union[p] = true
	}
	for p := range expectEntries {
		union[p] = true
	}
	out := make([]string, 0, len(union))
	for p := range union {
		if targetEntries[p] != expectEntries[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func (r *Repo) evaluateCandidate(
	ctx context.Context,
	rel string,
	target, expect entry,
	hasExpect bool,
	forced map[string]bool,
) (*Action, *Conflict) {
	if r.isDenied(rel) {
		return nil, &Conflict{Path: rel, Reason: ConflictProtected}
	}
	if _, err := checkSafeRelative(r.workTree, rel); err != nil {
		return nil, &Conflict{Path: rel, Reason: ConflictUnsafePath}
	}
	disk, skipped, err := r.currentEntry(ctx, rel)
	if err != nil {
		return nil, &Conflict{Path: rel, Reason: ConflictUnsafePath}
	}
	if skipped {
		return nil, &Conflict{Path: rel, Reason: ConflictProtected}
	}
	if disk == target {
		return nil, nil
	}
	if hasExpect && disk != expect && !forced[rel] {
		reason := ConflictChangedSince
		if !expect.present && disk.present {
			reason = ConflictAppearedSince
		}
		return nil, &Conflict{Path: rel, Reason: reason}
	}
	return &Action{
		Path:   rel,
		Kind:   classifyRestoreAction(target, disk),
		Target: target,
		Expect: disk,
	}, nil
}

func classifyRestoreAction(target, disk entry) ChangeKind {
	switch {
	case !target.present:
		return ChangeDeleted
	case !disk.present:
		return ChangeAdded
	default:
		return ChangeModified
	}
}

// ApplyRestore carries out a RestorePlan. It re-verifies every planned action
// under the project lock and returns ErrPlanStale if any file changed since the
// plan was computed.
func (r *Repo) ApplyRestore(ctx context.Context, plan *RestorePlan, override []string) ([]string, error) {
	if plan == nil {
		return nil, nil
	}
	var touched []string
	err := r.runLocked(ctx, func() error {
		var applyErr error
		touched, applyErr = r.applyRestoreLocked(ctx, plan, override)
		return applyErr
	})
	return touched, err
}

func (r *Repo) applyRestoreLocked(ctx context.Context, plan *RestorePlan, override []string) ([]string, error) {
	actions, err := r.resolvePlanActions(ctx, plan, override)
	if err != nil || len(actions) == 0 {
		return nil, err
	}
	if verifyErr := r.verifyActionsUnchanged(ctx, actions); verifyErr != nil {
		return nil, verifyErr
	}
	touched, indexUpdate, indexRemove, err := r.executeActions(ctx, actions)
	if err != nil {
		return touched, err
	}
	if syncErr := r.syncIndexAfterRestore(ctx, indexRemove, indexUpdate); syncErr != nil {
		return touched, syncErr
	}
	return touched, nil
}

func (r *Repo) resolvePlanActions(ctx context.Context, plan *RestorePlan, override []string) ([]Action, error) {
	if len(override) == 0 {
		out := make([]Action, len(plan.Actions))
		copy(out, plan.Actions)
		return out, nil
	}
	var extraPaths []string
	forced := toSet(override)
	for _, conflict := range plan.Conflicts {
		if forced[conflict.Path] && conflict.Reason.Overridable() {
			extraPaths = append(extraPaths, conflict.Path)
		}
	}
	if len(extraPaths) == 0 {
		out := make([]Action, len(plan.Actions))
		copy(out, plan.Actions)
		return out, nil
	}
	extra, err := r.planRestoreLocked(ctx, plan.TargetTree, plan.ExpectTree, extraPaths, extraPaths)
	if err != nil {
		return nil, err
	}
	for _, c := range extra.Conflicts {
		if !c.Reason.Overridable() {
			return nil, fmt.Errorf("%w: %s (%s)", ErrRestoreConflict, c.Path, c.Reason)
		}
	}
	out := make([]Action, 0, len(plan.Actions)+len(extra.Actions))
	out = append(out, plan.Actions...)
	return append(out, extra.Actions...), nil
}

func (r *Repo) verifyActionsUnchanged(ctx context.Context, actions []Action) error {
	for i := range actions {
		action := &actions[i]
		if _, err := checkSafeRelative(r.workTree, action.Path); err != nil {
			return fmt.Errorf("snapshot: %s: %w", action.Path, err)
		}
		now, skipped, err := r.currentEntry(ctx, action.Path)
		if err != nil {
			return err
		}
		if skipped || now != action.Expect {
			return fmt.Errorf("%w: %s", ErrPlanStale, action.Path)
		}
	}
	return nil
}

func (r *Repo) executeActions(ctx context.Context, actions []Action) (touched, indexUpdate, indexRemove []string, err error) {
	wsRoot, err := os.OpenRoot(r.workTree)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("snapshot: open workspace root: %w", err)
	}
	defer func() {
		if closeErr := wsRoot.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("snapshot: close workspace root: %w", closeErr)
		}
	}()

	for i := range actions {
		action := &actions[i]
		if !action.Target.present {
			if delErr := r.deleteRestoredFile(wsRoot, action.Path); delErr != nil {
				return touched, nil, nil, delErr
			}
			indexRemove = append(indexRemove, action.Path)
			touched = append(touched, action.Path)
			continue
		}
		if writeErr := r.writeFileFromBlob(ctx, wsRoot, action.Path, action.Target); writeErr != nil {
			return touched, nil, nil, writeErr
		}
		indexUpdate = append(indexUpdate, action.Path)
		touched = append(touched, action.Path)
	}
	return touched, indexUpdate, indexRemove, nil
}

func (r *Repo) syncIndexAfterRestore(ctx context.Context, indexRemove, indexUpdate []string) error {
	if len(indexRemove) > 0 {
		if _, err := r.git(ctx, joinNUL(indexRemove), "update-index", "-z", "--force-remove", "--stdin"); err != nil {
			return err
		}
	}
	if len(indexUpdate) > 0 {
		if _, err := r.git(ctx, joinNUL(indexUpdate), "update-index", "-z", "--add", "--remove", "--stdin"); err != nil {
			return err
		}
	}
	return nil
}
