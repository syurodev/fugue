package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/secrets"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

const (
	maxGateDiffLines = 5000
	maxGateDiffFiles = 200
)

type checkCmdRunner func(ctx context.Context, dir, command string) (output string, exitCode int, err error)

// hostResultGate implements ResultGate (orchestration §10 steps 1–4):
// write scope, secret scan, size limits, and checks through the policy.
type hostResultGate struct {
	store        *vendorstore.Store
	git          *gitops.Manager
	dataDir      string
	workspaces   *workspace.Service
	interactions *InteractionService
	tags         interaction.TagPolicy
	heavy        *heavySemaphore
	sessions     *VendorSessions
	ids          ids.Generator
	runCheck     checkCmdRunner
}

func newHostResultGate(
	store *vendorstore.Store,
	git *gitops.Manager,
	dataDir string,
	workspaces *workspace.Service,
	interactions *InteractionService,
	tags interaction.TagPolicy,
	heavy *heavySemaphore,
	sessions *VendorSessions,
	gen ids.Generator,
) *hostResultGate {
	return &hostResultGate{
		store:        store,
		git:          git,
		dataDir:      dataDir,
		workspaces:   workspaces,
		interactions: interactions,
		tags:         tags,
		heavy:        heavy,
		sessions:     sessions,
		ids:          gen,
		runCheck:     execCheckCommand,
	}
}

func (g *hostResultGate) Evaluate(ctx context.Context, teamID, taskID string) (ResultGateVerdict, error) {
	team, tasks, err := g.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return ResultGateVerdict{}, fmt.Errorf("read team snapshot: %w", err)
	}
	task, ok := findTask(tasks, taskID)
	if !ok {
		return ResultGateVerdict{}, errcode.New(errcode.NotFound, "team task not found")
	}
	spec, err := g.openSpec(ctx, teamID, taskID)
	if err != nil {
		return ResultGateVerdict{}, err
	}
	repoDir, repoID := g.resolveRepo(ctx, &team)
	workDir := repoDir

	if task.Deliverable != vendorstore.DeliverableReport {
		var verdict ResultGateVerdict
		var done bool
		workDir, verdict, done, err = g.evaluateDiffGates(ctx, &team, tasks, task, repoID, spec.WriteScope)
		if err != nil || done {
			return verdict, err
		}
	}
	if len(spec.Checks) > 0 {
		if verdict, done, checkErr := g.runChecks(ctx, &team, task, spec.Checks, workDir); checkErr != nil || done {
			return verdict, checkErr
		}
	}
	return ResultGateVerdict{Ready: true, Passed: true, Summary: "ok"}, nil
}

func (g *hostResultGate) evaluateDiffGates(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	tasks []vendorstore.TaskRecord,
	task *vendorstore.TaskRecord,
	repoID string,
	writeScope []string,
) (workDir string, verdict ResultGateVerdict, done bool, err error) {
	if g.git == nil {
		return "", ResultGateVerdict{}, true, errcode.New(errcode.FailedPrecondition, "gitops is not configured")
	}
	parentRef, err := parentBranch(task, tasks)
	if err != nil {
		return "", ResultGateVerdict{}, true, err
	}
	workDir, err = gitops.TaskWorktreePath(g.dataDir, repoID, gitTaskID(task.TaskID))
	if err != nil {
		return "", ResultGateVerdict{}, true, fmt.Errorf("resolve task worktree: %w", err)
	}
	if verdict, done, err = g.checkWriteScope(ctx, repoID, task, writeScope, parentRef); err != nil || done {
		return workDir, verdict, done, err
	}
	if verdict, done, err = g.checkSecretScan(ctx, team, repoID, task, parentRef); err != nil || done {
		return workDir, verdict, done, err
	}
	verdict, done, err = g.checkDiffSize(ctx, team, repoID, task, parentRef)
	return workDir, verdict, done, err
}

func (g *hostResultGate) openSpec(ctx context.Context, teamID, taskID string) (teams.ProtectedTaskContent, error) {
	raw, err := g.store.OpenTaskSpec(ctx, teamID, taskID)
	if err != nil {
		return teams.ProtectedTaskContent{}, fmt.Errorf("open task spec: %w", err)
	}
	var spec teams.ProtectedTaskContent
	if err := json.Unmarshal(raw, &spec); err != nil {
		return teams.ProtectedTaskContent{}, errcode.Wrap(errcode.Internal, "decode task spec", err)
	}
	return spec, nil
}

func (g *hostResultGate) resolveRepo(ctx context.Context, team *vendorstore.TeamRecord) (repoDir, repoID string) {
	if g.workspaces != nil && team.WorkspaceID != "" {
		if ws, err := g.workspaces.GetWorkspace(ctx, team.WorkspaceID); err == nil && len(ws.Roots) > 0 {
			repoDir = ws.Roots[0].Path
		}
	}
	if canonical, err := filepath.EvalSymlinks(repoDir); err == nil {
		repoDir = canonical
	}
	return repoDir, repoIDFor(repoDir)
}

func (g *hostResultGate) checkWriteScope(
	ctx context.Context,
	repoID string,
	task *vendorstore.TaskRecord,
	writeScope []string,
	parentRef string,
) (ResultGateVerdict, bool, error) {
	changed, err := g.git.ChangedFiles(ctx, g.dataDir, repoID, gitTaskID(task.TaskID), parentRef)
	if err != nil {
		return ResultGateVerdict{}, true, fmt.Errorf("list changed files: %w", err)
	}
	var outOfScope []string
	for _, f := range changed {
		norm := normalizeScopePath(f)
		if norm == "" || !matchesAnyWriteScope(norm, writeScope) {
			outOfScope = append(outOfScope, f)
		}
	}
	if len(outOfScope) == 0 {
		return ResultGateVerdict{}, false, nil
	}
	if task.ScopeBounces == 0 {
		return ResultGateVerdict{
			Ready:       true,
			Bounce:      true,
			BounceFiles: outOfScope,
			Summary:     "write_scope violated: " + strings.Join(outOfScope, ", "),
		}, true, nil
	}
	return ResultGateVerdict{
		Ready:       true,
		Passed:      false,
		FailureCode: "scope",
		Summary:     "write_scope violated twice: " + strings.Join(outOfScope, ", "),
	}, true, nil
}

func (g *hostResultGate) checkSecretScan(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	repoID string,
	task *vendorstore.TaskRecord,
	parentRef string,
) (ResultGateVerdict, bool, error) {
	diffs, err := g.git.DiffAddedByFile(ctx, g.dataDir, repoID, gitTaskID(task.TaskID), parentRef)
	if err != nil {
		return ResultGateVerdict{}, true, fmt.Errorf("read added diff lines: %w", err)
	}
	det := secrets.New(nil, secrets.Options{})
	var lines []string
	for _, d := range diffs {
		for _, f := range det.FindIn(d.AddedText, d.Path) {
			entry := fmt.Sprintf("%s: %s", d.Path, f.Kind)
			if f.Key != "" {
				entry += " (key: " + f.Key + ")"
			}
			lines = append(lines, entry)
		}
	}
	if len(lines) == 0 {
		return ResultGateVerdict{}, false, nil
	}
	summary := fmt.Sprintf("Task %s diff matched %d potential secret(s): %s", task.TaskID, len(lines), strings.Join(lines, "; "))
	allowed, err := g.askGateDecision(ctx, team, task, "secret_scan", summary)
	if err != nil {
		return ResultGateVerdict{}, true, err
	}
	if !allowed {
		return ResultGateVerdict{
			Ready:       true,
			Passed:      false,
			FailureCode: "secret_detected",
			Summary:     "secret scan rejected by user",
		}, true, nil
	}
	return ResultGateVerdict{}, false, nil
}

func (g *hostResultGate) checkDiffSize(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	repoID string,
	task *vendorstore.TaskRecord,
	parentRef string,
) (ResultGateVerdict, bool, error) {
	stat, err := g.git.DiffNumstat(ctx, g.dataDir, repoID, gitTaskID(task.TaskID), parentRef)
	if err != nil {
		return ResultGateVerdict{}, true, fmt.Errorf("read diff numstat: %w", err)
	}
	if stat.Lines <= maxGateDiffLines && stat.Files <= maxGateDiffFiles {
		return ResultGateVerdict{}, false, nil
	}
	summary := fmt.Sprintf("Task %s diff is large (%d files, %d lines > %d files / %d lines)", task.TaskID, stat.Files, stat.Lines, maxGateDiffFiles, maxGateDiffLines)
	allowed, err := g.askGateDecision(ctx, team, task, "oversize_diff", summary)
	if err != nil {
		return ResultGateVerdict{}, true, err
	}
	if !allowed {
		return ResultGateVerdict{
			Ready:       true,
			Passed:      false,
			FailureCode: "diff_too_large",
			Summary:     "oversize diff rejected by user",
		}, true, nil
	}
	return ResultGateVerdict{}, false, nil
}

func (g *hostResultGate) askGateDecision(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	task *vendorstore.TaskRecord,
	gateStep, summary string,
) (bool, error) {
	if g.interactions == nil {
		return false, errcode.New(errcode.FailedPrecondition, "interactions service is not configured")
	}
	if len(summary) > interaction.MaxSummaryBytes {
		summary = summary[:interaction.MaxSummaryBytes]
	}
	invID, err := g.ids.NewID()
	if err != nil {
		return false, errcode.Wrap(errcode.Internal, "generate gate interaction ID", err)
	}
	opID, err := interaction.NativeOperationKey(invID, invID, 1)
	if err != nil {
		return false, errcode.Wrap(errcode.Internal, "key gate interaction", err)
	}
	digest := sha256.Sum256([]byte(team.TeamID + ":" + task.TaskID + ":" + gateStep))
	spec := &interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Summary:     summary,
		Risk:        string(interaction.RiskWrite),
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
		Binding: interaction.Binding{
			SessionID:       task.SessionID,
			WorkspaceID:     team.WorkspaceID,
			InvocationID:    invID,
			AttemptID:       invID,
			CheckGeneration: 1,
			ToolName:        "result_gate_" + gateStep,
			ArgsDigest:      hex.EncodeToString(digest[:]),
		},
	}
	ticket, err := g.interactions.Ensure(spec)
	if err != nil {
		return false, err
	}
	if !ticket.Resolved {
		if ticket, err = g.interactions.AwaitResolved(ctx, ticket.RequestID); err != nil {
			if _, cancelErr := g.interactions.owner.InvalidateOperation(opID); cancelErr != nil {
				return false, errcode.Wrap(errcode.Internal, "cancel abandoned gate interaction", cancelErr)
			}
			return false, err
		}
	}
	return ticket.Outcome == interaction.OutcomeAllowedOnce, nil
}

func normalizeScopePath(p string) string {
	slash := filepath.ToSlash(strings.TrimSpace(p))
	cleaned := path.Clean(slash)
	cleaned = strings.TrimPrefix(cleaned, "./")
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return ""
	}
	return cleaned
}

func matchesAnyWriteScope(filePath string, scope []string) bool {
	return slices.ContainsFunc(scope, func(raw string) bool {
		return matchWriteScopePattern(filePath, raw)
	})
}

func matchWriteScopePattern(filePath, rawPattern string) bool {
	pat := strings.TrimPrefix(strings.TrimSpace(filepath.ToSlash(rawPattern)), "./")
	switch {
	case pat == "":
		return false
	case pat == ".", pat == "*", pat == "**":
		return true
	case strings.HasSuffix(pat, "/**"):
		prefix := strings.TrimSuffix(pat, "/**")
		return filePath == prefix || strings.HasPrefix(filePath, prefix+"/")
	case strings.HasSuffix(pat, "/"):
		return strings.HasPrefix(filePath, pat)
	case !strings.ContainsAny(pat, "*?["):
		return filePath == pat || strings.HasPrefix(filePath, pat+"/")
	default:
		matched, err := path.Match(pat, filePath)
		return err == nil && matched
	}
}
