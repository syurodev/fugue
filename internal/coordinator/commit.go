package coordinator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// maxCommitAttempts bounds how often one transition is appended again after a
// read-back proved that an earlier attempt did not commit.
const maxCommitAttempts = 3

const (
	resolveBackoffStart = time.Second
	resolveBackoffMax   = 30 * time.Second
)

// step is one decided batch on its way to the store. Every append of the
// coordinator, whatever command produced it, goes through commit so that a lost
// acknowledgement and a definite rejection are handled the same way everywhere.
type step struct {
	t       *Transition
	events  []kernel.Event
	attempt int
	// done runs on the actor once the batch is known to be committed and the
	// cached run and admission reflect it.
	done func(ctx context.Context, res *AppendResult)
	// fail runs on the actor when the batch is definitely not committed.
	fail func(ctx context.Context, err error)
}

// resolution is the result of a background read-back, posted to the mailbox.
type resolution struct {
	commandID string
	status    CommitResolution
	receipt   codec.Receipt
}

// halted reports whether any commit is still unresolved. Dispatching stops until
// every one is settled.
func (s *Session) halted() bool {
	return len(s.unresolved) > 0
}

// busy reports whether a commit for the run is unresolved. The cached state of
// such a run cannot be trusted until the read-back settles.
func (s *Session) busy(runID string) bool {
	for _, st := range s.unresolved {
		if st.t.RunID == runID {
			return true
		}
	}
	return false
}

// commit appends the step. A confirmed commit updates the cache and calls done;
// a lost acknowledgement is read back by the same command ID before anything
// else happens to the run; a definite rejection reloads the run and the
// admission row so the next append starts from what the store holds.
func (s *Session) commit(ctx context.Context, st *step) {
	if err := s.refreshAppendRevisions(ctx, st.t); err != nil {
		st.fail(ctx, err)
		return
	}
	res, err := s.cfg.Store.Append(ctx, st.t)
	if err == nil {
		s.applyAppended(ctx, st, &res)
		st.done(ctx, &res)
		return
	}
	if errors.Is(err, ErrCommitUnknown) {
		s.readBack(ctx, st)
		return
	}
	s.reload(ctx, st.t.RunID)
	st.fail(ctx, err)
}

// Confirmed events remain usable by completion callbacks, while missing revision
// entries prevent the next append from trusting an incomplete cache update.
func (s *Session) refreshAppendRevisions(ctx context.Context, tr *Transition) error {
	if s.admissionStale {
		if err := s.refreshAdmission(ctx, false); err != nil {
			return err
		}
		tr.ExpectedAdmissionRevision = s.admission.Revision
	}
	if _, known := s.runRevs[tr.RunID]; !known {
		if err := s.loadRun(ctx, tr.RunID); err != nil {
			return err
		}
		tr.ExpectedRunRevision = s.runRevs[tr.RunID]
	}
	return nil
}

// applyAppended folds a confirmed append into the cache. A duplicate append means
// an earlier attempt committed these events, so the store is the only truth.
func (s *Session) applyAppended(ctx context.Context, st *step, res *AppendResult) {
	runID := st.t.RunID
	if res.Duplicate {
		s.reload(ctx, runID)
		return
	}
	for _, ev := range st.events {
		s.runs[runID] = kernel.Evolve(s.runs[runID], ev)
	}
	if res.RevisionsUnavailable {
		delete(s.runRevs, runID)
		s.admissionStale = true
		return
	}
	s.runRevs[runID] = res.RunRevision
	s.admission.Revision = res.AdmissionRevision
}

// readBack asks the store once, inline, what became of the command. When that
// does not settle it, the step stays unresolved and a background resolver keeps
// asking on a backoff schedule; dispatching is halted meanwhile.
func (s *Session) readBack(ctx context.Context, st *step) {
	if s.onResolve != nil {
		s.onResolve(s.cfg.Clock.Now())
	}
	status, receipt, err := s.cfg.Store.ResolveCommit(ctx, st.t.CommandID)
	if err == nil && s.settle(ctx, st, status, &receipt) {
		return
	}
	s.unresolved[st.t.CommandID] = st
	s.startResolver(ctx, st.t.CommandID)
}

// settle acts on a read-back result and reports whether the outcome was known.
func (s *Session) settle(ctx context.Context, st *step, status CommitResolution, receipt *codec.Receipt) bool {
	switch status {
	case CommitResolutionCommitted:
		s.reload(ctx, st.t.RunID)
		res := AppendResult{
			Duplicate:         false,
			Receipt:           *receipt,
			RunRevision:       s.runRevs[st.t.RunID],
			AdmissionRevision: s.admission.Revision,
		}
		st.done(ctx, &res)
		return true
	case CommitResolutionNotCommitted:
		s.appendAgain(ctx, st)
		return true
	case CommitResolutionUnavailable:
		return false
	}
	return false
}

// appendAgain sends the same transition, with the same command ID, after a
// read-back proved that it did not commit. Other commands may have moved the
// revisions meanwhile, so the expectations are refreshed from the cache; a
// fence that went stale is rejected by the store and handled as any rejection.
func (s *Session) appendAgain(ctx context.Context, st *step) {
	if st.attempt+1 >= maxCommitAttempts {
		s.reload(ctx, st.t.RunID)
		st.fail(ctx, ErrCommitUnknown)
		return
	}
	st.attempt++
	st.t.ExpectedRunRevision = s.runRevs[st.t.RunID]
	st.t.ExpectedAdmissionRevision = s.admission.Revision
	s.commit(ctx, st)
}

// handleResolution applies, on the actor, a result the resolver posted.
func (s *Session) handleResolution(ctx context.Context, r *resolution) {
	st := s.unresolved[r.commandID]
	if st == nil {
		return
	}
	delete(s.unresolved, r.commandID)
	if !s.settle(ctx, st, r.status, &r.receipt) {
		s.unresolved[r.commandID] = st
		s.startResolver(ctx, r.commandID)
		return
	}
	s.retryParked(ctx)
	s.maybeAdmitNext(ctx)
}

func (s *Session) startResolver(ctx context.Context, commandID string) {
	s.resolvers.Add(1)
	supervise.Go(ctx, "coordinator.commit_resolver", func(rCtx context.Context) error {
		defer s.resolvers.Done()
		s.resolveLoop(rCtx, commandID)
		return nil
	})
}

// resolveLoop only reads the commit back and posts the result to the mailbox. It
// never decides, appends or touches session state: the actor stays the single
// writer of the stream.
func (s *Session) resolveLoop(ctx context.Context, commandID string) {
	delay := resolveBackoffStart
	for {
		timer := s.cfg.Clock.NewTimer(delay)
		if s.onTimerArmed != nil {
			s.onTimerArmed()
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.stopped:
			timer.Stop()
			return
		case <-timer.C():
		}

		if s.onResolve != nil {
			s.onResolve(s.cfg.Clock.Now())
		}
		status, receipt, err := s.cfg.Store.ResolveCommit(ctx, commandID)
		if err == nil && status != CommitResolutionUnavailable {
			sub := submission{
				req:      Request{},
				obs:      nil,
				resolved: &resolution{commandID: commandID, status: status, receipt: receipt},
				replyCh:  nil,
			}
			select {
			case s.mailbox <- sub:
			case <-ctx.Done():
			case <-s.stopped:
			}
			return
		}
		delay = min(delay*2, resolveBackoffMax)
	}
}

// loadRun caches the run as the store holds it.
func (s *Session) loadRun(ctx context.Context, runID string) error {
	st, rev, err := s.cfg.Store.LoadRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("load run: %w", err)
	}
	if st == nil {
		st = kernel.NewState()
	}
	s.runs[runID] = st
	s.runRevs[runID] = rev
	return nil
}

// reload replaces the cached run and the admission row with what the store
// holds. What cannot be reloaded is dropped from the cache or marked stale, so
// the next command loads it before it appends.
func (s *Session) reload(ctx context.Context, runID string) {
	if err := s.loadRun(ctx, runID); err != nil {
		delete(s.runs, runID)
		delete(s.runRevs, runID)
	}
	if err := s.refreshAdmission(ctx, true); err != nil {
		s.admissionStale = true
	}
}

// refreshAdmission loads the admission row when it is stale or force is set.
func (s *Session) refreshAdmission(ctx context.Context, force bool) error {
	if !force && !s.admissionStale {
		return nil
	}
	adm, err := s.cfg.Store.LoadAdmission(ctx, s.cfg.SessionID)
	if err != nil {
		return fmt.Errorf("load admission: %w", err)
	}
	s.admission = adm
	s.admissionStale = false
	return nil
}
