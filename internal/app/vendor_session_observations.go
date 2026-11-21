package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const vendorFailureCode = "vendor_failure"

func (a *vendorSessionActor) handleObservation(ctx context.Context, observation *vendor.Observation) error {
	a.observeAccount(observation)
	a.manager.notifyObservation(a.info.ID)
	if observation.Init != nil {
		commands := slices.Clone(observation.Init.SlashCommands)
		if !slices.Equal(commands, a.slashCommands) {
			a.slashCommands = commands
			state, reason := stateToStore(a.state)
			if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorSessionStateChanged, State: state, WaitingReason: reason}); err != nil {
				return err
			}
		}
	}
	if err := a.persistInit(ctx, observation.Init); err != nil {
		return err
	}
	if err := a.persistObservedItems(ctx, observation.Records); err != nil {
		return err
	}
	if err := a.persistObservedUpdates(ctx, observation.Updates); err != nil {
		return err
	}
	a.publishPartial(observation.Partial)
	if err := a.handleServerRequest(ctx, observation.ServerRequest); err != nil {
		return err
	}
	if err := a.persistTurnEnd(ctx, observation.TurnEnd); err != nil {
		return err
	}
	a.resetIdleTimer(ctx)
	return nil
}

func (a *vendorSessionActor) persistInit(ctx context.Context, init *vendor.Init) error {
	if init == nil || !ids.IsUUID(init.SessionID) || init.SessionID == a.info.ResumeHandle {
		return nil
	}
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorSessionStateChanged, ResumeHandle: init.SessionID}); err != nil {
		return err
	}
	a.info.ResumeHandle = init.SessionID
	return nil
}

func (a *vendorSessionActor) persistObservedItems(ctx context.Context, items []transcript.Item) error {
	for i := range items {
		item := items[i]
		if item.TurnID == "" {
			item.TurnID = a.activeTurnID()
		}
		if item.TurnID == "" {
			continue
		}
		if err := a.ensureVendorTurnStarted(ctx, item.TurnID); err != nil {
			return err
		}
		if err := a.recordItem(ctx, &item); err != nil {
			return err
		}
	}
	return nil
}

func (a *vendorSessionActor) ensureVendorTurnStarted(ctx context.Context, turnID string) error {
	if !strings.HasPrefix(turnID, "vendor-") || a.turns[turnID] {
		return nil
	}
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnStarted, TurnID: turnID, TurnOrigin: vendorstore.TurnOriginVendor}); err != nil {
		return err
	}
	a.stopIdleTimer()
	a.turns[turnID] = true
	if _, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactVendorTurn}); err != nil {
		return fmt.Errorf("record vendor-origin turn: %w", err)
	}
	a.manager.notifyTurnStarted(a.info.ID)
	return nil
}

func (a *vendorSessionActor) handleServerRequest(ctx context.Context, req *vendor.ServerRequest) error {
	if req == nil {
		return nil
	}
	if err := a.ensureVendorTurnStarted(ctx, req.TurnID); err != nil {
		return err
	}
	if a.manager.deps.Relay != nil {
		reqCopy := *req
		a.manager.spawn("codex-relay-"+a.info.ID, func(taskCtx context.Context) error {
			a.manager.deps.Relay.RelayServerRequest(taskCtx, a.info.ID, &reqCopy, a.session)
			return nil
		})
	}
	return nil
}

func (a *vendorSessionActor) persistObservedUpdates(ctx context.Context, updates []vendor.Update) error {
	for i := range updates {
		update := updates[i]
		if a.manager.deps.Vault.Snapshot().State != vault.StateUnlocked {
			update.After.Content = nil
			update.After.ContentTruncated = false
			update.After.ContentOriginalSize = 0
			update.After.RawTruncated = false
			update.After.RawOriginalSize = 0
		}
		if err := a.manager.deps.Transcripts.UpdateItem(ctx, a.info.ID, a.sequence, &update.Before, &update.After, nil); err != nil {
			return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
		}
		a.sequence++
	}
	return nil
}

func (a *vendorSessionActor) publishPartial(partial *vendor.Partial) {
	if partial != nil && a.manager.deps.Vault.Snapshot().State == vault.StateUnlocked {
		a.manager.deps.Transcripts.StreamItem(a.info.ID, partial.MessageID, 0, transcript.ViewText, partial.Fragment)
	}
}

func (a *vendorSessionActor) persistTurnEnd(ctx context.Context, turnEnd *vendor.TurnEnd) error {
	if turnEnd == nil {
		return nil
	}
	outcome, fact, stored := turnOutcome(turnEnd.Outcome)
	failure := storedFailureCode(outcome, turnEnd.Failure)
	if strings.HasPrefix(turnEnd.TurnID, "vendor-") {
		return a.persistVendorTurnEnd(ctx, turnEnd.TurnID, stored, failure)
	}
	return a.persistFugueTurnEnd(ctx, turnEnd.TurnID, fact, stored, failure)
}

// storedFailureCode is the failure code a failed turn is stored with: the
// account-relevant classes (vendor-runtime §11 failed(auth), failed(quota))
// keep their name, every other failure is the generic vendor failure.
func storedFailureCode(outcome vendor.Outcome, class vendor.FailureClass) string {
	failure := failureCode(outcome)
	if failure != "" && (class == vendor.FailureAuth || class == vendor.FailureQuota) {
		return string(class)
	}
	return failure
}

// observeAccount feeds the session's quota windows and classified failure to
// the vendor's account (orchestration §8). It runs before anything is stored,
// so a storage failure never loses a quota signal.
func (a *vendorSessionActor) observeAccount(observation *vendor.Observation) {
	accounts := a.manager.deps.Accounts
	if accounts == nil {
		return
	}
	accounts.ObserveQuota(a.info.Vendor, observation.Quota)
	if observation.TurnEnd != nil {
		accounts.ObserveFailure(a.info.Vendor, observation.TurnEnd.Failure)
	}
}

func turnOutcome(observed string) (vendor.Outcome, vendor.Fact, string) {
	switch observed {
	case string(vendor.OutcomeCompleted):
		return vendor.OutcomeCompleted, vendor.Fact{Kind: vendor.FactTurnCompleted}, vendorstore.OutcomeCompleted
	case string(vendor.OutcomeInterrupted):
		return vendor.OutcomeInterrupted, vendor.Fact{Kind: vendor.FactTurnInterrupted}, vendorstore.OutcomeInterrupted
	default:
		return vendor.OutcomeFailed, vendor.Fact{Kind: vendor.FactTurnFailed, Code: vendorFailureCode}, vendorstore.OutcomeFailed
	}
}

func (a *vendorSessionActor) persistVendorTurnEnd(ctx context.Context, turnID, outcome, failure string) error {
	if !a.turns[turnID] {
		return nil
	}
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: turnID, Outcome: outcome, FailureCode: failure}); err != nil {
		return err
	}
	delete(a.turns, turnID)
	a.cancelTurnInteractions(turnID)
	a.manager.notifyWake(a.info.ID)
	if outcome == vendorstore.OutcomeCompleted {
		a.manager.notifyTurnCompleted(a.info.ID)
	} else if outcome == vendorstore.OutcomeFailed && (failure == string(vendor.FailureQuota) || failure == string(vendor.FailureAuth)) {
		a.manager.notifyTurnFailed(a.info.ID, failure)
	}
	return nil
}

func (a *vendorSessionActor) persistFugueTurnEnd(ctx context.Context, observedTurnID string, fact vendor.Fact, outcome, failure string) error {
	turnID := a.turnID
	if turnID == "" || observedTurnID != turnID {
		return nil
	}
	if a.vaultInterruptTurnID == turnID {
		return a.endVaultLockedTurn(ctx, vendorstore.OutcomeInterrupted, vendor.Fact{Kind: vendor.FactTurnInterrupted})
	}
	if a.userInterruptTurnID == turnID {
		return a.endInterruptedTurn(ctx, turnID, vendorstore.OutcomeInterrupted, vendor.Fact{Kind: vendor.FactTurnInterrupted})
	}
	next, _, err := vendor.Transition(a.state, fact)
	if err != nil {
		return fmt.Errorf("end Fugue turn state: %w", err)
	}
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: turnID, Outcome: outcome, FailureCode: failure, State: vendorstore.StateReady}); err != nil {
		return err
	}
	a.state = next
	delete(a.turns, turnID)
	a.turnID = ""
	a.cancelTurnInteractions(turnID)
	a.manager.notifyWake(a.info.ID)
	if outcome == vendorstore.OutcomeCompleted {
		a.manager.notifyTurnCompleted(a.info.ID)
	} else if outcome == vendorstore.OutcomeFailed && (failure == string(vendor.FailureQuota) || failure == string(vendor.FailureAuth)) {
		a.manager.notifyTurnFailed(a.info.ID, failure)
	}
	return nil
}

func (a *vendorSessionActor) cancelTurnInteractions(turnID string) {
	if a.manager.deps.Relay != nil && turnID != "" {
		a.manager.deps.Relay.CancelTurn(a.info.ID, turnID)
	}
}

func (a *vendorSessionActor) recordItem(ctx context.Context, item *transcript.Item) error {
	if a.manager.deps.Vault.Snapshot().State != vault.StateUnlocked {
		item.Content = nil
		item.ContentTruncated = false
		item.ContentOriginalSize = 0
		item.RawTruncated = false
		item.RawOriginalSize = 0
	}
	if err := a.manager.deps.Transcripts.RecordItem(ctx, a.info.ID, a.sequence, item, nil); err != nil {
		return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
	}
	a.sequence++
	return nil
}

func (a *vendorSessionActor) updateItem(ctx context.Context, prev, next *transcript.Item) error {
	if a.manager.deps.Vault.Snapshot().State != vault.StateUnlocked {
		next.Content = nil
		next.ContentTruncated = false
		next.ContentOriginalSize = 0
		next.RawTruncated = false
		next.RawOriginalSize = 0
	}
	if err := a.manager.deps.Transcripts.UpdateItem(ctx, a.info.ID, a.sequence, prev, next, nil); err != nil {
		return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
	}
	a.sequence++
	return nil
}

func (a *vendorSessionActor) transition(ctx context.Context, fact vendor.Fact) error {
	next, _, err := vendor.Transition(a.state, fact)
	if err != nil {
		return fmt.Errorf("transition vendor session: %w", err)
	}
	state, reason := stateToStore(next)
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorSessionStateChanged, State: state, WaitingReason: reason}); err != nil {
		return err
	}
	a.state = next
	// A process that just turned ready may take its mailbox turn (F11.3b).
	if fact.Kind == vendor.FactReady {
		a.manager.notifyWake(a.info.ID)
	}
	return nil
}

func (a *vendorSessionActor) append(ctx context.Context, event *vendorstore.Event) error {
	next, err := a.manager.deps.Store.Append(ctx, a.info.ID, a.sequence, event)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
	}
	if next != a.sequence+1 {
		return fmt.Errorf("%w: non-contiguous append result", ErrVendorActorFailed)
	}
	if err := a.manager.deps.Transcripts.PublishCommittedEvent(context.WithoutCancel(ctx), a.info.ID, next); err != nil {
		return fmt.Errorf("%w: publish committed transcript event: %w", ErrVendorActorFailed, err)
	}
	a.sequence = next
	return nil
}

// SetWakeHook binds the team wake trigger (F11.3b): the hook runs when a
// session may have become able to take a Fugue-origin turn — after its
// process turned ready and after any turn ended.
func (m *VendorSessions) SetWakeHook(hook func(sessionID string)) {
	m.wakeHook = hook
}

func (m *VendorSessions) notifyWake(sessionID string) {
	if m.wakeHook != nil {
		m.wakeHook(sessionID)
	}
}

// SetTurnCompletedHook binds the no-report nudge check (Q-131): runs only
// when a turn ends with OutcomeCompleted.
func (m *VendorSessions) SetTurnCompletedHook(hook func(sessionID string)) {
	m.turnCompletedHook = hook
}

func (m *VendorSessions) notifyTurnCompleted(sessionID string) {
	m.resolveTurnWaiter(sessionID, nil)
	if m.turnCompletedHook != nil {
		m.turnCompletedHook(sessionID)
	}
}

// SetTurnFailedHook binds the quota/auth turn failure handler (F11.6b, Q-149).
func (m *VendorSessions) SetTurnFailedHook(hook func(sessionID, failureCode string)) {
	m.turnFailedHook = hook
}

func (m *VendorSessions) notifyTurnFailed(sessionID, failureCode string) {
	m.resolveTurnWaiter(sessionID, errcode.New(errcode.Unavailable, "reviewer turn failed: "+failureCode))
	if m.turnFailedHook != nil {
		m.turnFailedHook(sessionID, failureCode)
	}
}

// SetTurnStartedHook binds the task turn budget handler (F11.6c, Q-149).
func (m *VendorSessions) SetTurnStartedHook(hook func(sessionID string)) {
	m.turnStartedHook = hook
}

func (m *VendorSessions) notifyTurnStarted(sessionID string) {
	if m.turnStartedHook != nil {
		m.turnStartedHook(sessionID)
	}
}

// SetObservationHook binds the task watchdog silence reset hook (F11.6c, Q-150).
func (m *VendorSessions) SetObservationHook(hook func(sessionID string)) {
	m.observationHook = hook
}

func (m *VendorSessions) notifyObservation(sessionID string) {
	if m.observationHook != nil {
		m.observationHook(sessionID)
	}
}

// SetSessionDiedHook binds the §12 mid-turn exit handler (F11.9): the hook
// runs once when a session's vendor process exited without a requested stop.
func (m *VendorSessions) SetSessionDiedHook(hook func(sessionID string)) {
	m.sessionDiedHook = hook
}

func (m *VendorSessions) notifySessionDied(sessionID string) {
	m.resolveTurnWaiter(sessionID, errcode.New(errcode.Unavailable, "reviewer session exited mid-turn"))
	if m.sessionDiedHook != nil {
		m.sessionDiedHook(sessionID)
	}
}
