package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const vendorFailureCode = "vendor_failure"

func (a *vendorSessionActor) handleObservation(ctx context.Context, observation *claude.Observation) error {
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
	return a.persistTurnEnd(ctx, observation.TurnEnd)
}

func (a *vendorSessionActor) persistInit(ctx context.Context, init *claude.Init) error {
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
		if strings.HasPrefix(item.TurnID, "vendor-") && !a.turns[item.TurnID] {
			if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnStarted, TurnID: item.TurnID, TurnOrigin: vendorstore.TurnOriginVendor}); err != nil {
				return err
			}
			a.turns[item.TurnID] = true
			if _, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactVendorTurn}); err != nil {
				return fmt.Errorf("record vendor-origin turn: %w", err)
			}
		}
		if err := a.recordItem(ctx, &item); err != nil {
			return err
		}
	}
	return nil
}

func (a *vendorSessionActor) persistObservedUpdates(ctx context.Context, updates []claude.Update) error {
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

func (a *vendorSessionActor) publishPartial(partial *claude.Partial) {
	if partial != nil && a.manager.deps.Vault.Snapshot().State == vault.StateUnlocked {
		a.manager.deps.Transcripts.StreamItem(a.info.ID, partial.MessageID, 0, transcript.ViewText, partial.Fragment)
	}
}

func (a *vendorSessionActor) persistTurnEnd(ctx context.Context, turnEnd *claude.TurnEnd) error {
	if turnEnd == nil {
		return nil
	}
	outcome, fact, stored := turnOutcome(turnEnd.Outcome)
	if strings.HasPrefix(turnEnd.TurnID, "vendor-") {
		return a.persistVendorTurnEnd(ctx, turnEnd.TurnID, stored, failureCode(outcome))
	}
	return a.persistFugueTurnEnd(ctx, turnEnd.TurnID, fact, stored, failureCode(outcome))
}

func turnOutcome(observed string) (vendor.Outcome, vendor.Fact, string) {
	if observed == claude.OutcomeCompleted {
		return vendor.OutcomeCompleted, vendor.Fact{Kind: vendor.FactTurnCompleted}, vendorstore.OutcomeCompleted
	}
	return vendor.OutcomeFailed, vendor.Fact{Kind: vendor.FactTurnFailed, Code: vendorFailureCode}, vendorstore.OutcomeFailed
}

func (a *vendorSessionActor) persistVendorTurnEnd(ctx context.Context, turnID, outcome, failure string) error {
	if !a.turns[turnID] {
		return nil
	}
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: turnID, Outcome: outcome, FailureCode: failure}); err != nil {
		return err
	}
	delete(a.turns, turnID)
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
	return nil
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
	a.sequence = next
	return nil
}
