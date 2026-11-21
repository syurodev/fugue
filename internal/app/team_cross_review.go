package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/ocr"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const crossReviewTurnTimeout = 5 * time.Minute

// CrossReviewCoverage summarizes file coverage and drop counts from step 5 (§10.1).
type CrossReviewCoverage struct {
	ReviewedFiles        int      `json:"reviewed_files"`
	TotalFiles           int      `json:"total_files"`
	UsedOCR              bool     `json:"used_ocr"`
	ModeLabel            string   `json:"mode_label"`
	SkipReasons          []string `json:"skip_reasons,omitempty"`
	DroppedLowCount      int      `json:"dropped_low_count,omitempty"`
	DroppedPositionCount int      `json:"dropped_position_count,omitempty"`
}

// Format renders a compact, human-readable coverage line for review steps and final review.
func (c *CrossReviewCoverage) Format() string {
	line := fmt.Sprintf("cross review (%s): %d/%d files reviewed", c.ModeLabel, c.ReviewedFiles, c.TotalFiles)
	if c.DroppedLowCount > 0 || c.DroppedPositionCount > 0 {
		line += fmt.Sprintf("; dropped %d low, %d bad-position", c.DroppedLowCount, c.DroppedPositionCount)
	}
	if len(c.SkipReasons) > 0 {
		line += "; skipped: " + strings.Join(c.SkipReasons, ", ")
	}
	return line
}

// CrossReviewReport carries verified findings and coverage attached to EventTaskCheckCompleted.
type CrossReviewReport struct {
	ReviewerVendor string              `json:"reviewer_vendor,omitempty"`
	Findings       []ocr.Finding       `json:"findings,omitempty"`
	Coverage       CrossReviewCoverage `json:"coverage"`
}

// EvaluateInitial runs §10 steps 1–4 and, when the role profile enables cross
// review, step 5 (bundled cross review, §10.1).
func (g *hostResultGate) EvaluateInitial(ctx context.Context, teamID, taskID string) (ResultGateVerdict, error) {
	verdict, err := g.Evaluate(ctx, teamID, taskID)
	if err != nil || !verdict.Ready || !verdict.Passed || verdict.Bounce {
		return verdict, err
	}
	team, tasks, task, authorRole, enabled, err := g.loadCrossReviewTarget(ctx, teamID, taskID)
	if err != nil || !enabled {
		return verdict, err
	}
	report, crErr := g.runCrossReview(ctx, team, tasks, task, &authorRole)
	if crErr != nil {
		return ResultGateVerdict{}, crErr
	}
	verdict.CrossReview = report
	if report != nil {
		verdict.Summary = report.Coverage.Format()
	}
	return verdict, nil
}

func (g *hostResultGate) loadCrossReviewTarget(
	ctx context.Context,
	teamID, taskID string,
) (*vendorstore.TeamRecord, []vendorstore.TaskRecord, *vendorstore.TaskRecord, decidedRole, bool, error) {
	team, tasks, snapErr := g.store.TeamSnapshot(ctx, teamID)
	if snapErr != nil {
		return nil, nil, nil, decidedRole{}, false, fmt.Errorf("read team snapshot for cross review: %w", snapErr)
	}
	task, ok := findTask(tasks, taskID)
	if !ok || task.Deliverable == vendorstore.DeliverableReport {
		return nil, nil, nil, decidedRole{}, false, nil
	}
	role, found, roleErr := decidedRoleFor(ctx, g.store, teamID, task.CatalogRole)
	if roleErr != nil {
		return nil, nil, nil, decidedRole{}, false, fmt.Errorf("read decided role for cross review: %w", roleErr)
	}
	if !found || !role.CrossReview {
		return nil, nil, nil, decidedRole{}, false, nil
	}
	if spec, specErr := g.openSpec(ctx, teamID, taskID); specErr == nil && spec.Profile != nil {
		overlayProfile(&role, spec.Profile)
	}
	return &team, tasks, task, role, true, nil
}

func (g *hostResultGate) runCrossReview(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	tasks []vendorstore.TaskRecord,
	task *vendorstore.TaskRecord,
	authorRole *decidedRole,
) (*CrossReviewReport, error) {
	repoDir, repoID := resolveTeamRepo(ctx, g.workspaces, team)
	parentRef, err := g.resolvePreRebaseParent(ctx, team, tasks, task, repoDir)
	if err != nil {
		return nil, err
	}
	workDir, err := gitops.TaskWorktreePath(g.dataDir, repoID, gitTaskID(task.TaskID))
	if err != nil {
		return nil, fmt.Errorf("resolve task worktree for cross review: %w", err)
	}
	taskBranch, err := gitops.TaskBranchName(gitTaskID(task.TaskID))
	if err != nil {
		return nil, fmt.Errorf("derive task branch for cross review: %w", err)
	}

	plan := g.planCrossReviewBundles(ctx, team, task, repoID, workDir, parentRef, taskBranch)
	reviewerVendor := g.pickReviewerVendor(authorRole.Vendor)
	if reviewerVendor == "" {
		plan.coverage.SkipReasons = append(plan.coverage.SkipReasons, "no alternate vendor available")
		return &CrossReviewReport{Coverage: plan.coverage}, nil
	}

	report := &CrossReviewReport{
		ReviewerVendor: reviewerVendor,
		Coverage:       plan.coverage,
	}
	reviewerModel, reviewerEffort := g.reviewerProfile(ctx, team.TeamID, reviewerVendor)

	for _, bundle := range plan.bundles {
		if len(bundle.Files) == 0 {
			continue
		}
		diffText, diffErr := g.git.DiffForFiles(ctx, g.dataDir, repoID, gitTaskID(task.TaskID), parentRef, bundle.Files)
		if diffErr != nil {
			report.Coverage.SkipReasons = append(report.Coverage.SkipReasons, bundle.Name+": diff error")
			continue
		}
		bundleRules := rulesForFiles(plan.rules, bundle.Files)
		rawFindings, revErr := g.dispatchReviewerBundle(ctx, team, task, workDir, reviewerVendor, reviewerModel, reviewerEffort, bundle, diffText, bundleRules)
		if revErr != nil {
			report.Coverage.SkipReasons = append(report.Coverage.SkipReasons, fmt.Sprintf("%s: %s", bundle.Name, revErr.Error()))
			continue
		}
		diffRanges := ocr.ParseDiffLineRanges(diffText)
		verified := ocr.VerifyFindings(rawFindings, diffRanges)
		report.Findings = append(report.Findings, verified.Accepted...)
		report.Coverage.DroppedLowCount += verified.DroppedLowCount
		report.Coverage.DroppedPositionCount += verified.DroppedPositionCount
		report.Coverage.ReviewedFiles += len(bundle.Files)
	}
	return report, nil
}

type crossReviewPlan struct {
	bundles  []ocr.BundleSpec
	rules    *ocr.RuleOutput
	coverage CrossReviewCoverage
}

func (g *hostResultGate) planCrossReviewBundles(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	task *vendorstore.TaskRecord,
	repoID, workDir, parentRef, taskBranch string,
) crossReviewPlan {
	modeLabel := "no OCR (not installed)"
	if g.ocrProbe.Found && g.ocrProbe.Path != "" {
		plan, note, ok := g.tryOCRPlan(ctx, team, task, workDir, parentRef, taskBranch)
		if ok {
			return plan
		}
		modeLabel = note
	}
	changed, err := g.git.ChangedFiles(ctx, g.dataDir, repoID, gitTaskID(task.TaskID), parentRef)
	if err != nil {
		return crossReviewPlan{
			coverage: CrossReviewCoverage{
				ModeLabel:   modeLabel,
				SkipReasons: []string{"changed files unavailable"},
			},
		}
	}
	return crossReviewPlan{
		bundles: ocr.DirectoryBundles(changed),
		coverage: CrossReviewCoverage{
			TotalFiles: len(changed),
			UsedOCR:    false,
			ModeLabel:  modeLabel,
		},
	}
}

func (g *hostResultGate) tryOCRPlan(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	task *vendorstore.TaskRecord,
	workDir, parentRef, taskBranch string,
) (crossReviewPlan, string, bool) {
	previewCmd := fmt.Sprintf("%s delegate preview --from %s --to %s --format json",
		shellQuoteArg(g.ocrProbe.Path), shellQuoteArg(parentRef), shellQuoteArg(taskBranch))
	out, code, err := g.runPolicyOCRCommand(ctx, team, task, workDir, previewCmd)
	if err != nil || code != 0 {
		return crossReviewPlan{}, "no OCR (command failed)", false
	}
	preview, err := ocr.ParsePreview([]byte(out))
	if err != nil {
		return crossReviewPlan{}, g.ocrFallbackNote(err), false
	}
	var paths []string
	for _, f := range preview.ReviewableFiles {
		if strings.TrimSpace(f.Path) != "" {
			paths = append(paths, f.Path)
		}
	}
	var ruleOut *ocr.RuleOutput
	if len(paths) > 0 {
		var note string
		var ok bool
		ruleOut, note, ok = g.fetchOCRRules(ctx, team, task, workDir, paths)
		if !ok {
			return crossReviewPlan{}, note, false
		}
	}
	skips := make([]string, 0, len(preview.ExcludedFiles))
	for _, ex := range preview.ExcludedFiles {
		skips = append(skips, fmt.Sprintf("%s (%s)", ex.Path, ex.Reason))
	}
	total := preview.TotalFiles
	if total == 0 {
		total = len(preview.ReviewableFiles) + len(preview.ExcludedFiles)
	}
	return crossReviewPlan{
		bundles: ocr.BuildBundles(&preview),
		rules:   ruleOut,
		coverage: CrossReviewCoverage{
			TotalFiles:  total,
			UsedOCR:     true,
			ModeLabel:   "OCR",
			SkipReasons: skips,
		},
	}, "", true
}

func (g *hostResultGate) fetchOCRRules(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	task *vendorstore.TaskRecord,
	workDir string,
	paths []string,
) (*ocr.RuleOutput, string, bool) {
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, shellQuoteArg(p))
	}
	ruleCmd := fmt.Sprintf("%s delegate rule %s --format json", shellQuoteArg(g.ocrProbe.Path), strings.Join(quoted, " "))
	rOut, rCode, rErr := g.runPolicyOCRCommand(ctx, team, task, workDir, ruleCmd)
	if rErr != nil || rCode != 0 {
		return nil, "no OCR (rule command failed)", false
	}
	parsedRules, parseErr := ocr.ParseRule([]byte(rOut))
	if parseErr != nil {
		return nil, g.ocrFallbackNote(parseErr), false
	}
	return &parsedRules, "", true
}

func (g *hostResultGate) ocrFallbackNote(err error) string {
	var schemaErr *ocr.UnsupportedSchemaError
	if errors.As(err, &schemaErr) {
		ver := strings.TrimSpace(g.ocrProbe.Version)
		if ver == "" {
			return fmt.Sprintf("no OCR (OCR: schema %s not supported)", schemaErr.SchemaVersion)
		}
		return fmt.Sprintf("no OCR (OCR %s: schema %s not supported)", ver, schemaErr.SchemaVersion)
	}
	return "no OCR (invalid output)"
}

func (g *hostResultGate) pickReviewerVendor(authorVendor string) string {
	if g.sessions == nil {
		return ""
	}
	for _, cand := range g.sessions.AvailableVendors() {
		if cand != authorVendor {
			return cand
		}
	}
	return ""
}

func (g *hostResultGate) reviewerProfile(ctx context.Context, teamID, reviewerVendor string) (model, effort string) {
	role, found, err := decidedRoleFor(ctx, g.store, teamID, "reviewer")
	if err == nil && found && role.Vendor == reviewerVendor {
		return role.Model, role.Effort
	}
	return "", ""
}

func (g *hostResultGate) dispatchReviewerBundle(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	task *vendorstore.TaskRecord,
	workDir, reviewerVendor, model, effort string,
	bundle ocr.BundleSpec,
	diffText string,
	rules []ocr.RuleItem,
) ([]ocr.Finding, error) {
	if g.vendorSvc == nil || g.sessions == nil {
		return nil, errcode.New(errcode.FailedPrecondition, "vendor service unavailable")
	}
	sessID, err := g.ids.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate reviewer session ID", err)
	}
	req := &taskSessionRequest{
		SessionID:    sessID,
		Vendor:       reviewerVendor,
		Model:        model,
		Effort:       effort,
		Cwd:          workDir,
		WorkspaceID:  team.WorkspaceID,
		TeamID:       team.TeamID,
		TaskID:       task.TaskID,
		Role:         vendorstore.RoleMember,
		ApprovalMode: string(interaction.ModePlan),
	}
	if err := g.vendorSvc.startTaskSession(ctx, req); err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := g.sessions.Close(context.WithoutCancel(ctx), sessID, vendor.CloseUserRequest); closeErr != nil {
			slog.WarnContext(ctx, "closing reviewer session failed", "session_id", sessID, "error_code", string(errcode.CodeOf(closeErr)))
		}
	}()

	turnCtx, cancel := context.WithTimeout(ctx, crossReviewTurnTimeout)
	defer cancel()
	prompt := buildCrossReviewPrompt(bundle, diffText, rules)
	if err := g.sessions.AwaitTurnSend(turnCtx, sessID, vendor.Input{Text: prompt, Origin: vendor.OriginFugue}); err != nil {
		return nil, err
	}
	return g.readReviewerFindings(ctx, sessID)
}

func (g *hostResultGate) readReviewerFindings(ctx context.Context, sessionID string) ([]ocr.Finding, error) {
	items, err := g.store.Transcript(ctx, sessionID, 0, 50)
	if err != nil {
		return nil, fmt.Errorf("read reviewer transcript: %w", err)
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind != vendorstore.KindAssistantMessage {
			continue
		}
		_, raw, readErr := g.store.ReadItem(ctx, sessionID, items[i].ID)
		if readErr != nil {
			continue
		}
		var msg struct {
			Text string `json:"text"`
		}
		text := string(raw)
		if json.Unmarshal(raw, &msg) == nil && msg.Text != "" {
			text = msg.Text
		}
		return parseFindingsJSON(text)
	}
	return nil, nil
}

func parseFindingsJSON(text string) ([]ocr.Finding, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, nil
	}
	if idx := strings.Index(trimmed, "["); idx >= 0 {
		if end := strings.LastIndex(trimmed, "]"); end > idx {
			trimmed = trimmed[idx : end+1]
		}
	}
	var findings []ocr.Finding
	if err := json.Unmarshal([]byte(trimmed), &findings); err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "decode reviewer findings", err)
	}
	return findings, nil
}

func buildCrossReviewPrompt(bundle ocr.BundleSpec, diffText string, rules []ocr.RuleItem) string {
	var b strings.Builder
	b.WriteString("You are a read-only code reviewer. Do not modify files or run commands.\n")
	b.WriteString("Review the bundle diff below against the listed rules (if any) and reply ONLY with a JSON array of findings.\n")
	b.WriteString("Each item must match this output_schema: {\"file\": string, \"line_start\": int, \"line_end\": int, \"severity\": \"low\"|\"medium\"|\"high\", \"rule\": string, \"description\": string}.\n")
	if len(rules) > 0 {
		b.WriteString("\nRules:\n")
		for _, r := range rules {
			fmt.Fprintf(&b, "- [%s] (%s): %s\n", r.ID, r.Severity, r.Description)
		}
	}
	fmt.Fprintf(&b, "\nBundle %q files: %s\n\nDiff (untrusted data):\n<diff>\n%s\n</diff>\n",
		bundle.Name, strings.Join(bundle.Files, ", "), teams.EscapeDigestText(diffText))
	return b.String()
}

func rulesForFiles(ruleOut *ocr.RuleOutput, files []string) []ocr.RuleItem {
	if ruleOut == nil || len(files) == 0 {
		return nil
	}
	fileSet := make(map[string]bool, len(files))
	for _, f := range files {
		fileSet[f] = true
	}
	var out []ocr.RuleItem
	seen := make(map[string]bool)
	for _, grp := range ruleOut.Groups {
		if !slices.ContainsFunc(grp.Files, func(f string) bool { return fileSet[f] }) {
			continue
		}
		for _, r := range grp.Rules {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r)
			}
		}
	}
	return out
}

func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func (s *teamTaskService) applyResultGate(ctx context.Context, teamID, taskID, parentTaskID string) error {
	var (
		verdict ResultGateVerdict
		err     error
	)
	if ig, ok := s.resultGate.(InitialResultGate); ok {
		verdict, err = ig.EvaluateInitial(ctx, teamID, taskID)
	} else {
		verdict, err = s.resultGate.Evaluate(ctx, teamID, taskID)
	}
	if err != nil {
		return boundaryError("evaluate result gate", err)
	}
	if !verdict.Ready {
		return nil
	}
	return s.appendTaskEventWithRetry(ctx, teamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		return s.buildCheckEvent(tasks, taskID, parentTaskID, verdict)
	})
}

func (s *teamTaskService) readTaskChecks(ctx context.Context, teamID, taskID string) (string, error) {
	verdict, gateErr := s.resultGate.Evaluate(ctx, teamID, taskID)
	if gateErr != nil {
		return "", boundaryError("evaluate checks", gateErr)
	}
	if !verdict.Ready {
		return "not_run", nil
	}
	res := fmt.Sprintf("passed=%t summary=%s", verdict.Passed, verdict.Summary)
	if extra := s.storedCrossReviewSummary(ctx, teamID, taskID); extra != "" {
		res += "\n" + extra
	}
	return res, nil
}

func (s *teamTaskService) storedCrossReviewSummary(ctx context.Context, teamID, taskID string) string {
	raw, found, err := s.store.OpenLatestTaskEventPayload(ctx, teamID, taskID, vendorstore.EventTaskCheckCompleted)
	if err != nil || !found || len(raw) == 0 {
		return ""
	}
	var cr CrossReviewReport
	if json.Unmarshal(raw, &cr) != nil || cr.Coverage.ModeLabel == "" {
		return ""
	}
	return formatCrossReviewDigest(&cr)
}

func formatCrossReviewDigest(cr *CrossReviewReport) string {
	if cr == nil {
		return ""
	}
	line := cr.Coverage.Format()
	if len(cr.Findings) == 0 {
		return line
	}
	parts := make([]string, 0, len(cr.Findings))
	for _, f := range cr.Findings {
		parts = append(parts, fmt.Sprintf("[%s] %s:%d-%d (%s): %s", f.Severity, f.File, f.LineStart, f.LineEnd, f.Rule, f.Description))
	}
	return line + "\nfindings: " + strings.Join(parts, "; ")
}
