package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

const (
	checkCommandTimeout = 10 * time.Minute
	maxCheckOutputBytes = 64 << 10
	checkToolName       = "check"
)

func (g *hostResultGate) runChecks(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	task *vendorstore.TaskRecord,
	checks []string,
	workDir string,
) (ResultGateVerdict, bool, error) {
	mode := g.sessionApprovalMode(ctx, task.SessionID)
	cfg := g.workspaceConfig(ctx, team.WorkspaceID)
	for _, cmd := range checks {
		trimmed := strings.TrimSpace(cmd)
		if trimmed == "" {
			continue
		}
		verdict, done, err := g.runSingleCheck(ctx, team, task, mode, &cfg, trimmed, workDir)
		if err != nil || done {
			return verdict, done, err
		}
	}
	return ResultGateVerdict{}, false, nil
}

func (g *hostResultGate) runSingleCheck(
	ctx context.Context,
	team *vendorstore.TeamRecord,
	task *vendorstore.TaskRecord,
	mode interaction.ApprovalMode,
	cfg *workspace.EffectiveConfig,
	command, workDir string,
) (ResultGateVerdict, bool, error) {
	eval := workspace.EvaluateShell(cfg, g.tags, command)
	invID, err := g.ids.NewID()
	if err != nil {
		return ResultGateVerdict{}, true, errcode.Wrap(errcode.Internal, "generate check invocation ID", err)
	}
	digest := sha256.Sum256([]byte(command))
	grantKey := ""
	if eval.Target == "" {
		grantKey = "check:" + hex.EncodeToString(digest[:8])
	}
	binding := interaction.Binding{
		SessionID:       task.SessionID,
		WorkspaceID:     team.WorkspaceID,
		InvocationID:    invID,
		AttemptID:       invID,
		CheckGeneration: 1,
		ToolName:        checkToolName,
		GrantScopeKey:   grantKey,
		HasTarget:       eval.Target != "",
		EnvTag:          eval.EnvTag,
		ArgsDigest:      hex.EncodeToString(digest[:]),
	}
	desc := interaction.Descriptor{Risk: interaction.RiskWrite, Confirm: interaction.ConfirmCustom}
	call := interaction.Call{
		EffectiveRisk:    parseInteractionRisk(eval.Risk),
		EffectiveConfirm: interaction.ConfirmEnvTag,
		ShellAsk:         eval.Ask,
		HasTarget:        eval.Target != "",
		EnvTag:           eval.EnvTag,
		GrantScopeKey:    grantKey,
	}
	var grants interaction.GrantLookup
	if g.interactions != nil {
		grants = g.interactions.owner.GrantLookupFor(&binding)
	}
	verdict, err := interaction.Decide(mode, desc, call, g.tags, grants)
	if err != nil {
		return ResultGateVerdict{}, true, errcode.Wrap(errcode.Internal, "evaluate check policy", err)
	}
	switch verdict.Kind {
	case interaction.VerdictRefuse:
		return failChecksVerdict("check refused by policy: " + verdict.Reason), true, nil
	case interaction.VerdictAsk:
		allowed, askErr := g.askCheckConfirm(ctx, &binding, command, &eval, verdict.Offers)
		if askErr != nil {
			return ResultGateVerdict{}, true, askErr
		}
		if !allowed {
			return failChecksVerdict("check denied by user"), true, nil
		}
	case interaction.VerdictRun:
	}
	return g.executeCheck(ctx, task, command, workDir)
}

func (g *hostResultGate) askCheckConfirm(
	ctx context.Context,
	binding *interaction.Binding,
	command string,
	eval *workspace.ShellEval,
	offers []interaction.Outcome,
) (bool, error) {
	if g.interactions == nil {
		return false, errcode.New(errcode.FailedPrecondition, "interactions service is not configured")
	}
	spec, opID, err := buildCheckConfirmSpec(binding, command, eval, offers)
	if err != nil {
		return false, err
	}
	ticket, err := g.interactions.Ensure(spec)
	if err != nil {
		return false, err
	}
	if !ticket.Resolved {
		if ticket, err = g.interactions.AwaitResolved(ctx, ticket.RequestID); err != nil {
			if _, cancelErr := g.interactions.owner.InvalidateOperation(opID); cancelErr != nil {
				return false, errcode.Wrap(errcode.Internal, "cancel abandoned check confirmation", cancelErr)
			}
			return false, err
		}
	}
	if ticket.Outcome != interaction.OutcomeAllowedOnce && ticket.Outcome != interaction.OutcomeAllowedSession {
		return false, nil
	}
	if _, err := g.interactions.owner.ClaimReceipt(ticket.ReceiptID, binding); err != nil {
		return false, errcode.New(errcode.FailedPrecondition, "check approval receipt could not be claimed")
	}
	return true, nil
}

func buildCheckConfirmSpec(
	binding *interaction.Binding,
	command string,
	eval *workspace.ShellEval,
	offers []interaction.Outcome,
) (*interaction.Spec, string, error) {
	opID, err := interaction.NativeOperationKey(binding.InvocationID, binding.AttemptID, binding.CheckGeneration)
	if err != nil {
		return nil, "", errcode.Wrap(errcode.Internal, "key check confirmation", err)
	}
	if binding.GrantScopeKey == "" || len(offers) == 0 {
		offers = []interaction.Outcome{interaction.OutcomeAllowedOnce}
	}
	label := ""
	if slices.Contains(offers, interaction.OutcomeAllowedSession) {
		label = "Allow this check for the session"
	}
	summary := "Run check: " + command
	if eval.EnvTag != "" {
		summary = fmt.Sprintf("Run check [%s: %s]: %s", eval.EnvTag, eval.Target, command)
	}
	if len(summary) > interaction.MaxSummaryBytes {
		summary = summary[:interaction.MaxSummaryBytes]
	}
	return &interaction.Spec{
		OperationID:     opID,
		Kind:            interaction.KindConfirm,
		Summary:         summary,
		Risk:            eval.Risk,
		GrantScopeLabel: label,
		Offers:          offers,
		Binding:         *binding,
	}, opID, nil
}

func (g *hostResultGate) executeCheck(
	ctx context.Context,
	task *vendorstore.TaskRecord,
	command, workDir string,
) (ResultGateVerdict, bool, error) {
	if g.heavy != nil {
		release, _, err := g.heavy.Acquire(ctx)
		if err != nil {
			return ResultGateVerdict{}, true, err
		}
		defer release()
	}

	itemID, turnID, err := g.ensureCheckTranscriptIDs(ctx, task.SessionID, task.TaskID)
	if err != nil {
		return ResultGateVerdict{}, true, err
	}
	pending := newCheckToolItem(itemID, task.SessionID, turnID, command, "pending", nil, "")
	if err := g.recordCheckItem(ctx, task.SessionID, nil, pending); err != nil {
		return ResultGateVerdict{}, true, err
	}

	runner := g.runCheck
	if runner == nil {
		runner = execCheckCommand
	}
	out, code, runErr := runner(ctx, workDir, command)

	status := "succeeded"
	if runErr != nil || code != 0 {
		status = "failed"
	}
	exitCode := code
	done := newCheckToolItem(itemID, task.SessionID, turnID, command, status, &exitCode, out)
	done.Revision = 1
	if err := g.recordCheckItem(ctx, task.SessionID, pending, done); err != nil {
		return ResultGateVerdict{}, true, err
	}
	if runErr != nil || code != 0 {
		return failChecksVerdict(fmt.Sprintf("check %q failed (exit %d)", command, code)), true, nil
	}
	return ResultGateVerdict{}, false, nil
}

func newCheckToolItem(itemID, sessionID, turnID, command, status string, exitCode *int, output string) *transcript.Item {
	return &transcript.Item{
		ID:        itemID,
		SessionID: sessionID,
		TurnID:    turnID,
		Kind:      transcript.ToolCall,
		Metadata: transcript.ToolCallMetadata{
			ToolName: checkToolName,
			Status:   status,
			ExitCode: exitCode,
		},
		Content: transcript.ToolCallContent{
			Title:  checkToolName,
			Input:  command,
			Output: output,
		},
	}
}

func (g *hostResultGate) ensureCheckTranscriptIDs(ctx context.Context, sessionID, taskID string) (itemID, turnID string, err error) {
	itemID, idErr := g.ids.NewID()
	if idErr != nil {
		itemID = "check-" + taskID
	}
	if sessionID == "" {
		return itemID, "gate-" + taskID, nil
	}
	if g.sessions != nil {
		if active, activeErr := g.sessions.ActiveTurnID(ctx, sessionID); activeErr == nil && active != "" {
			return itemID, active, nil
		}
	}
	if turns, turnErr := g.store.SessionTurns(ctx, sessionID); turnErr == nil && len(turns) > 0 {
		return itemID, turns[len(turns)-1].ID, nil
	}
	turnID = "gate-" + taskID
	startEv := &vendorstore.Event{
		Type:   vendorstore.VendorTurnStarted,
		TurnID: turnID,
	}
	if g.sessions != nil {
		if recErr := g.sessions.RecordEvent(ctx, sessionID, startEv); recErr == nil {
			return itemID, turnID, nil
		}
	}
	snap, snapErr := g.store.Snapshot(ctx, sessionID)
	if snapErr != nil {
		return "", "", fmt.Errorf("read session snapshot: %w", snapErr)
	}
	if _, appErr := g.store.Append(ctx, sessionID, snap.LastSequence, startEv); appErr != nil {
		return "", "", fmt.Errorf("append gate turn start: %w", appErr)
	}
	return itemID, turnID, nil
}

func (g *hostResultGate) recordCheckItem(ctx context.Context, sessionID string, prev, item *transcript.Item) error {
	if g.sessions == nil || sessionID == "" {
		return nil
	}
	if prev == nil {
		return g.sessions.RecordTranscriptItem(ctx, sessionID, item)
	}
	return g.sessions.UpdateTranscriptItem(ctx, sessionID, prev, item)
}

func (g *hostResultGate) sessionApprovalMode(ctx context.Context, sessionID string) interaction.ApprovalMode {
	if sessionID == "" {
		return interaction.ModeAuto
	}
	snap, err := g.store.Snapshot(ctx, sessionID)
	if err != nil || snap.ApprovalMode == "" {
		return interaction.ModeAuto
	}
	return interaction.ApprovalMode(snap.ApprovalMode)
}

func (g *hostResultGate) workspaceConfig(ctx context.Context, workspaceID string) workspace.EffectiveConfig {
	if g.workspaces == nil || workspaceID == "" {
		return workspace.EffectiveConfig{}
	}
	cfg, _, err := g.workspaces.GetEffectiveConfig(ctx, workspaceID, nil)
	if err != nil {
		return workspace.EffectiveConfig{}
	}
	return cfg
}

func parseInteractionRisk(risk string) interaction.Risk {
	switch interaction.Risk(risk) {
	case interaction.RiskRead, interaction.RiskWrite, interaction.RiskDestructive, interaction.RiskExternal:
		return interaction.Risk(risk)
	default:
		return interaction.RiskWrite
	}
}

func failChecksVerdict(summary string) ResultGateVerdict {
	return ResultGateVerdict{
		Ready:       true,
		Passed:      false,
		FailureCode: "checks_failed",
		Summary:     summary,
	}
}

func execCheckCommand(ctx context.Context, dir, command string) (string, int, error) {
	runCtx, cancel := context.WithTimeout(ctx, checkCommandTimeout)
	defer cancel()

	// #nosec G204 -- check commands are model/proposal-authored and gated by interaction.Decide + shellrules before execution.
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	var buf bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &buf, max: maxCheckOutputBytes}
	cmd.Stderr = &limitedWriter{w: &buf, max: maxCheckOutputBytes}

	err := cmd.Run()
	if err == nil {
		return buf.String(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return buf.String(), exitErr.ExitCode(), err
	}
	return buf.String(), -1, err
}

type limitedWriter struct {
	w   *bytes.Buffer
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	rem := l.max - l.w.Len()
	if rem > 0 {
		if len(p) > rem {
			_, _ = l.w.Write(p[:rem])
		} else {
			_, _ = l.w.Write(p)
		}
	}
	return len(p), nil
}

// RecordTranscriptItem records a transcript item through the session's live
// actor when present so sequence numbers stay contiguous, or directly through
// Transcripts when the session is not currently attached (F11.5a checks).
func (m *VendorSessions) RecordTranscriptItem(ctx context.Context, sessionID string, item *transcript.Item) error {
	if actor := m.actor(sessionID); actor != nil {
		return actor.call(ctx, &actorCommand{kind: commandRecordItem, itemOp: &transcriptItemOp{next: item}})
	}
	snap, err := m.deps.Store.Snapshot(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("read session snapshot: %w", err)
	}
	return m.deps.Transcripts.RecordItem(ctx, sessionID, snap.LastSequence, item, nil)
}

// UpdateTranscriptItem updates a transcript item through the live actor when
// present, or directly through Transcripts when no actor is running (F11.5a).
func (m *VendorSessions) UpdateTranscriptItem(ctx context.Context, sessionID string, prev, next *transcript.Item) error {
	if actor := m.actor(sessionID); actor != nil {
		return actor.call(ctx, &actorCommand{kind: commandRecordItem, itemOp: &transcriptItemOp{prev: prev, next: next}})
	}
	snap, err := m.deps.Store.Snapshot(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("read session snapshot: %w", err)
	}
	return m.deps.Transcripts.UpdateItem(ctx, sessionID, snap.LastSequence, prev, next, nil)
}
