package app

// The vendor transcript service: it composes internal/transcript's item model
// with internal/vendorstore's journal and the event hub into the per-session
// durable and delta topics, the item write path and the paged reads
// (transcript §5–8, roadmap F9.4b). Owner: application layer. Allowed callers:
// hostapi (F9.9) and application tests.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// The per-vendor-session topic names (transcript §6). F9.9 exposes them over
// the Host API; the payload encoding is JSON until then.
const (
	TopicTranscript      = "transcript:"
	TopicTranscriptDelta = "transcript-delta:"
)

// The transcript topic event types.
const (
	EventTranscriptItem  = "transcript.item"
	EventTranscriptState = "transcript.state"
	EventTranscriptDelta = "transcript.delta"
)

// TranscriptDeps are the ports the transcript service needs.
type TranscriptDeps struct {
	Store *vendorstore.Store
	Hub   *EventHub
	Vault *vault.Service
	Clock clock.Clock
	// KnownValues lists the protected values the redactor masks exactly.
	// Stage A wires only the J-04 environment values; no owner can list vault
	// record values or credential tokens yet (roadmap Q-34).
	KnownValues KnownValues
}

// sessionTopics is one registered session: its delta coalescer and the latest
// redacted view published per open item, which the delta topic's snapshot
// serves from memory alone (transcript §6).
type sessionTopics struct {
	mu        sync.Mutex
	views     map[string]transcript.View
	revisions map[string]uint64
	coalescer *transcript.DeltaCoalescer
}

// Transcripts is the vendor transcript read model. Its context is captured in
// the constructor closures and never stored.
type Transcripts struct {
	deps      TranscriptDeps
	redactor  *SecretRedactor
	spawn     func(name string, fn func(context.Context) error) *supervise.Task
	coalescer func(session string) *transcript.DeltaCoalescer

	mu              sync.Mutex
	sessions        map[string]*sessionTopics
	closing         bool
	cancel          context.CancelFunc
	tasksMu         sync.Mutex
	tasks           []*supervise.Task
	unregisterVault func()
	closeOnce       sync.Once
	closeErr        error
}

// NewTranscripts builds the service and registers its vault lock guard: on
// unlock the redactor rebuilds, on lock the redactor drops its detector and
// every session's deltas stop and its stored views are dropped from memory.
// The context is the caller's base context; the service keeps only its cancel.
func NewTranscripts(ctx context.Context, deps TranscriptDeps) (*Transcripts, error) {
	if deps.Store == nil || deps.Hub == nil || deps.Clock == nil {
		return nil, errcode.New(errcode.InvalidArgument, "store, hub and clock are required")
	}
	base, cancel := context.WithCancel(ctx)
	t := &Transcripts{deps: deps, sessions: map[string]*sessionTopics{}, cancel: cancel}
	t.redactor = NewSecretRedactor(base, deps.Vault, deps.KnownValues)
	t.spawn = func(name string, fn func(context.Context) error) *supervise.Task {
		task := supervise.Go(base, name, fn)
		t.tasksMu.Lock()
		t.tasks = append(t.tasks, task)
		t.tasksMu.Unlock()
		return task
	}
	t.coalescer = func(session string) *transcript.DeltaCoalescer {
		return transcript.NewDeltaCoalescer(base, deps.Clock, t.deltaSink(session), t.redactor)
	}
	if deps.Vault != nil {
		t.unregisterVault = deps.Vault.RegisterLockGuard(t.locked)
	}
	return t, nil
}

// locked is the vault guard: it only swaps in-memory state and never blocks.
// The coalescers close on one supervised task because Close flushes at delta
// rate and a guard must be quick.
func (t *Transcripts) locked(snap vault.Snapshot) {
	if snap.State == vault.StateUnlocked {
		return
	}
	t.mu.Lock()
	pending := t.sessions
	t.sessions = map[string]*sessionTopics{}
	t.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	t.spawn("app.transcript-lock", func(context.Context) error {
		for _, st := range pending {
			st.mu.Lock()
			co := st.coalescer
			st.mu.Unlock()
			if co != nil {
				if err := co.Close(); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("closing a transcript coalescer failed", "err", err.Error())
				}
			}
		}
		return nil
	})
}

// unlocked reports whether protected content may be read or streamed: only an
// unlocked vault counts, so an unknown state fails closed.
func (t *Transcripts) unlocked() bool {
	return t.deps.Vault != nil && t.deps.Vault.Snapshot().State == vault.StateUnlocked
}

// RegisterSession registers the durable and delta topics of one session. It is
// called when the session is created and, through RegisterStoredSessions, at
// startup for every session the store still holds.
func (t *Transcripts) RegisterSession(session string) error {
	t.mu.Lock()
	if _, ok := t.sessions[session]; ok {
		// Another registration is in flight or done; the hub rejects a
		// duplicate, so the first caller owns the result.
		t.mu.Unlock()
		return nil
	}
	st := &sessionTopics{views: map[string]transcript.View{}, revisions: map[string]uint64{}}
	t.sessions[session] = st
	t.mu.Unlock()
	if err := t.deps.Hub.RegisterDurable(TopicTranscript+session, &transcriptDurable{t: t, session: session}); err != nil {
		t.dropSessionEntry(session)
		return err
	}
	if err := t.deps.Hub.RegisterEphemeral(TopicTranscriptDelta+session,
		func(_ context.Context) ([]byte, error) { return t.deltaSnapshot(session) },
		defaultEphemeralBufferCap); err != nil {
		t.deps.Hub.Unregister(TopicTranscript + session)
		t.dropSessionEntry(session)
		return err
	}
	return nil
}

// RegisterStoredSessions registers the topics of every session the store still
// holds; called once at startup.
func (t *Transcripts) RegisterStoredSessions(ctx context.Context) error {
	sessions, listErr := t.deps.Store.ListSessions(ctx)
	if listErr != nil {
		return fmt.Errorf("list the vendor sessions: %w", listErr)
	}
	for i := range sessions {
		if err := t.RegisterSession(sessions[i].ID); err != nil {
			return err
		}
	}
	return nil
}

// PurgeSession crypto-shreds the session and unregisters its topics, so a
// purged session's topics are denied afterwards.
func (t *Transcripts) PurgeSession(ctx context.Context, session string) error {
	if err := t.deps.Store.PurgeSession(ctx, session); err != nil {
		return fmt.Errorf("purge the vendor session: %w", err)
	}
	t.UnregisterSession(session)
	return nil
}

// UnregisterSession removes the session's topics — live subscribers end with
// resync_required — and its coalescer and stored views.
func (t *Transcripts) UnregisterSession(session string) {
	t.mu.Lock()
	st := t.sessions[session]
	delete(t.sessions, session)
	t.mu.Unlock()
	t.deps.Hub.Unregister(TopicTranscript + session)
	t.deps.Hub.Unregister(TopicTranscriptDelta + session)
	if st == nil {
		return
	}
	st.mu.Lock()
	co := st.coalescer
	st.coalescer = nil
	st.mu.Unlock()
	if co != nil {
		if err := co.Close(); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("closing the transcript coalescer failed", "err", err.Error())
		}
	}
}

func (t *Transcripts) dropSessionEntry(session string) {
	t.mu.Lock()
	delete(t.sessions, session)
	t.mu.Unlock()
}

// sessionEntry returns the session's entry, creating its coalescer when it is
// the first streaming update. Creation stays under the service mutex so a
// concurrent lock can never orphan a coalescer: an entry the guard collected
// is already out of the map and accepts no new coalescer.
func (t *Transcripts) sessionEntry(session string) *sessionTopics {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.sessions[session]
	if !ok || t.closing {
		return nil
	}
	if st.coalescer == nil {
		st.coalescer = t.coalescer(session)
	}
	return st
}

func (t *Transcripts) coalescerOf(st *sessionTopics) *transcript.DeltaCoalescer {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.coalescer
}

// Close unregisters the vault guard, closes every session's topics and
// coalescers, then cancels the base context and waits for the supervised
// tasks. It is idempotent.
func (t *Transcripts) Close() error {
	t.closeOnce.Do(func() {
		if t.unregisterVault != nil {
			t.unregisterVault()
		}
		t.mu.Lock()
		t.closing = true
		pending := t.sessions
		t.sessions = map[string]*sessionTopics{}
		t.mu.Unlock()
		for session := range pending {
			t.deps.Hub.Unregister(TopicTranscript + session)
			t.deps.Hub.Unregister(TopicTranscriptDelta + session)
		}
		for _, st := range pending {
			st.mu.Lock()
			co := st.coalescer
			st.coalescer = nil
			st.mu.Unlock()
			if co != nil {
				if err := co.Close(); err != nil && !errors.Is(err, context.Canceled) {
					// A coalescer whose caller context already ended stopped
					// cleanly; only a real failure joins the Close error.
					t.closeErr = errors.Join(t.closeErr, err)
				}
			}
		}
		t.cancel()
		t.closeErr = errors.Join(t.closeErr, t.redactor.Close())
		t.closeErr = errors.Join(t.closeErr, t.waitTasks())
	})
	return t.closeErr
}

// waitTasks waits for the supervised tasks, re-checking for tasks spawned
// while waiting (a lock may land as Close runs).
func (t *Transcripts) waitTasks() error {
	var waitErr error
	for {
		t.tasksMu.Lock()
		tasks := t.tasks
		t.tasks = nil
		t.tasksMu.Unlock()
		if len(tasks) == 0 {
			return waitErr
		}
		for _, task := range tasks {
			if err := task.Wait(); err != nil {
				waitErr = errors.Join(waitErr, err)
			}
		}
	}
}

// storeSeq converts the store's committed sequence to the hub's sequence
// type. Store sequences are positive by the schema CHECK; a negative value is
// a store violation the caller refuses.
func storeSeq(n int64) (EventSeq, error) {
	if n < 0 {
		return 0, errcode.New(errcode.Internal, "negative vendor sequence")
	}
	return EventSeq(n), nil
}

// storeCursor converts a hub sequence to the store's cursor type.
func storeCursor(seq EventSeq) (int64, error) {
	if seq > math.MaxInt64 {
		return 0, errcode.New(errcode.Internal, "vendor sequence out of range")
	}
	return int64(seq), nil
}

// storeRevision converts a stored revision to the hub's revision type; the
// schema CHECK keeps revisions positive.
func storeRevision(n int64) (uint64, error) {
	if n < 0 {
		return 0, errcode.New(errcode.Internal, "negative vendor item revision")
	}
	return uint64(n), nil
}

// expectedRevision converts a transcript revision to the store's expected
// revision.
func expectedRevision(n uint64) (int64, error) {
	if n > math.MaxInt64 {
		return 0, errcode.New(errcode.Internal, "transcript revision out of range")
	}
	return int64(n), nil
}
