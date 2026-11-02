package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaintenanceReport summarises what a Maintain run cleaned up.
type MaintenanceReport struct {
	Expired         []string `json:"expired,omitempty"`
	RemovedProjects []string `json:"removedProjects,omitempty"`
	PackedProjects  []string `json:"packedProjects,omitempty"`
}

// Maintain enforces the age, per-session turn/tool quotas, and per-project
// disk-size limits, drops orphaned refs, repacks active shadow repositories,
// and removes shadow directories with no surviving snapshots.
func (e *Engine) Maintain(ctx context.Context, records Records) (MaintenanceReport, error) {
	if !e.git.OK() || records == nil {
		return MaintenanceReport{}, nil
	}
	rows, err := records.ListCheckpoints(ctx)
	if err != nil {
		return MaintenanceReport{}, fmt.Errorf("snapshot: list checkpoints: %w", err)
	}
	now := e.clock.Now()
	expired := expiredSnapshots(rows, now, e.rootDir, e.limits)
	if len(expired) > 0 {
		if delErr := records.DeleteCheckpoints(ctx, expired); delErr != nil {
			return MaintenanceReport{}, fmt.Errorf("snapshot: delete expired checkpoints: %w", delErr)
		}
	}
	liveByProject, liveProjects := groupSurvivingByProject(rows, toSet(expired))
	removed, packed := e.sweepShadowDirectories(ctx, liveByProject, liveProjects)
	return MaintenanceReport{
		Expired:         expired,
		RemovedProjects: removed,
		PackedProjects:  packed,
	}, nil
}

func groupSurvivingByProject(rows []Record, expiredSet map[string]bool) (map[string]map[string]bool, map[string]bool) {
	liveByProject := make(map[string]map[string]bool)
	liveProjects := make(map[string]bool)
	for i := range rows {
		row := &rows[i]
		if expiredSet[row.ID] {
			continue
		}
		liveProjects[row.ProjectKey] = true
		byID := liveByProject[row.ProjectKey]
		if byID == nil {
			byID = make(map[string]bool)
			liveByProject[row.ProjectKey] = byID
		}
		byID[row.ID] = true
	}
	return liveByProject, liveProjects
}

func (e *Engine) sweepShadowDirectories(
	ctx context.Context,
	liveByProject map[string]map[string]bool,
	liveProjects map[string]bool,
) (removed, packed []string) {
	entries, err := os.ReadDir(e.rootDir)
	if err != nil {
		return nil, nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		key := entry.Name()
		gitDir := filepath.Join(e.rootDir, key)
		if !liveProjects[key] {
			e.evictRepoHandle(key)
			if rmErr := os.RemoveAll(gitDir); rmErr == nil {
				removed = append(removed, key)
			}
			continue
		}
		if e.sweepProjectRefs(ctx, key, gitDir, liveByProject[key]) == nil {
			packed = append(packed, key)
		}
	}
	return removed, packed
}

func (e *Engine) evictRepoHandle(key string) {
	e.mu.Lock()
	delete(e.repos, key)
	e.mu.Unlock()
}

func expiredSnapshots(rows []Record, now time.Time, rootDir string, limits Limits) []string {
	limits = limits.withDefaults()
	expired := make(map[string]bool)

	expireByAge(rows, now, limits, expired)
	expireBySessionQuota(rows, limits, expired)
	expireByProjectSize(rows, rootDir, limits, expired)

	if len(expired) == 0 {
		return nil
	}
	out := make([]string, 0, len(expired))
	for id := range expired {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func expireByAge(rows []Record, now time.Time, limits Limits, expired map[string]bool) {
	turnCutoff := now.AddDate(0, 0, -int(limits.TurnRetentionDays))
	toolCutoff := now.AddDate(0, 0, -int(limits.ToolRetentionDays))
	safetyCutoff := now.AddDate(0, 0, -DefaultSafetyRetentionDays)

	for i := range rows {
		row := &rows[i]
		cutoff := turnCutoff
		switch row.Kind {
		case KindTool:
			cutoff = toolCutoff
		case KindRestoreSafety, KindRewindSafety:
			cutoff = safetyCutoff
		case KindTurn:
			cutoff = turnCutoff
		}
		if !row.CreatedAt.IsZero() && row.CreatedAt.Before(cutoff) {
			expired[row.ID] = true
		}
	}
}

func expireBySessionQuota(rows []Record, limits Limits, expired map[string]bool) {
	turnsBySession := make(map[string][]Record)
	toolsBySession := make(map[string][]Record)
	for i := range rows {
		row := &rows[i]
		if expired[row.ID] {
			continue
		}
		switch row.Kind {
		case KindTurn:
			turnsBySession[row.SessionID] = append(turnsBySession[row.SessionID], *row)
		case KindTool:
			toolsBySession[row.SessionID] = append(toolsBySession[row.SessionID], *row)
		case KindRestoreSafety, KindRewindSafety:
			// Safety snapshots are governed by their age window and the project cap.
		}
	}
	markExcessInBuckets(turnsBySession, int(limits.MaxTurnsPerSession), expired)
	markExcessInBuckets(toolsBySession, int(limits.MaxToolsPerSession), expired)
}

func markExcessInBuckets(buckets map[string][]Record, limit int, expired map[string]bool) {
	if limit <= 0 {
		return
	}
	for _, list := range buckets {
		if len(list) <= limit {
			continue
		}
		sortRecordsNewestFirst(list)
		for i := limit; i < len(list); i++ {
			expired[list[i].ID] = true
		}
	}
}

// expireByProjectSize enforces the shared 1 GiB per-project cap, pruning tool
// and safety snapshots before turn snapshots so tool snapshots never displace
// turn history unless no tool snapshots remain.
func expireByProjectSize(rows []Record, rootDir string, limits Limits, expired map[string]bool) {
	if limits.MaxProjectBytes <= 0 || rootDir == "" {
		return
	}
	byProject := make(map[string][]Record)
	for i := range rows {
		row := &rows[i]
		if !expired[row.ID] {
			byProject[row.ProjectKey] = append(byProject[row.ProjectKey], *row)
		}
	}
	for key, list := range byProject {
		if len(list) <= 1 {
			continue
		}
		size := directorySize(filepath.Join(rootDir, key))
		if size <= limits.MaxProjectBytes {
			continue
		}
		sortForDiskEviction(list)
		perCheckpoint := max(size/int64(len(list)), 1)
		remaining := size
		for i := 0; i < len(list)-1 && remaining > limits.MaxProjectBytes; i++ {
			expired[list[i].ID] = true
			remaining -= perCheckpoint
		}
	}
}

// sortForDiskEviction orders non-turn snapshots (tool, safety) before turn
// snapshots, and oldest-first within each group.
func sortForDiskEviction(list []Record) {
	sort.Slice(list, func(i, j int) bool {
		turnI := list[i].Kind == KindTurn
		turnJ := list[j].Kind == KindTurn
		if turnI != turnJ {
			return !turnI
		}
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].ID < list[j].ID
		}
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
}

func (e *Engine) sweepProjectRefs(ctx context.Context, key, gitDir string, liveIDs map[string]bool) error {
	e.mu.Lock()
	repo := e.repos[key]
	e.mu.Unlock()
	if repo == nil {
		workTree, err := readProjectMarker(gitDir)
		if err != nil {
			return err
		}
		var openErr error
		repo, openErr = e.Open(ctx, workTree)
		if openErr != nil {
			return openErr
		}
	}
	return repo.runLocked(ctx, func() error {
		return sweepRepoRefsLocked(ctx, repo, liveIDs)
	})
}

func readProjectMarker(gitDir string) (workTree string, err error) {
	gitRoot, err := os.OpenRoot(gitDir)
	if err != nil {
		return "", fmt.Errorf("snapshot: open shadow root: %w", err)
	}
	defer func() {
		if closeErr := gitRoot.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("snapshot: close shadow root: %w", closeErr)
		}
	}()

	raw, err := gitRoot.ReadFile(projectFileName)
	if err != nil {
		return "", fmt.Errorf("snapshot: read project marker: %w", err)
	}
	workTree = strings.TrimSpace(string(raw))
	if workTree == "" {
		return "", ErrEmptyProjectMarker
	}
	return workTree, nil
}

func sweepRepoRefsLocked(ctx context.Context, repo *Repo, liveIDs map[string]bool) error {
	raw, err := repo.git(ctx, nil, "for-each-ref", "--format=%(refname)", refPrefix)
	if err != nil {
		return err
	}
	var toDelete []string
	for _, line := range strings.Split(string(raw), "\n") {
		ref := strings.TrimSpace(line)
		if ref == "" {
			continue
		}
		rest := strings.TrimPrefix(ref, refPrefix)
		checkpointID, _, ok := strings.Cut(rest, "/")
		if !ok || !liveIDs[checkpointID] {
			toDelete = append(toDelete, ref)
		}
	}
	if len(toDelete) > 0 {
		var spec bytes.Buffer
		for _, ref := range toDelete {
			spec.WriteString("delete ")
			spec.WriteString(ref)
			spec.WriteByte(0)
			spec.WriteByte(0)
		}
		if _, delErr := repo.git(ctx, spec.Bytes(), "update-ref", "--stdin", "-z"); delErr != nil {
			return delErr
		}
	}
	_, err = repo.git(ctx, nil, "repack", "-adq")
	return err
}

func directorySize(root string) int64 {
	var total int64
	if err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, _ error) error {
		if d == nil || d.IsDir() {
			return nil
		}
		if info, infoErr := d.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	}); err != nil {
		return 0
	}
	return total
}
