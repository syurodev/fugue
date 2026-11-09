package app

// The transcript redactor: an app type implementing transcript.Redactor over
// an immutable *secrets.Detector. Owner: application layer. Allowed callers:
// none outside this package.

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/secrets"
	"github.com/syurodev/fugue/internal/vault"
)

// KnownValues lists the protected values the redactor masks exactly. It is a
// port because no stage-A owner can list vault record values or credential
// tokens yet (roadmap Q-34): stage A masks token patterns and key-name
// assignments, and the composition passes the J-04 environment values it
// holds (EnvKnownValues in the tests pins the mapping until the session
// service lands).
type KnownValues func(ctx context.Context) ([]secrets.KnownValue, error)

// SecretRedactor implements transcript.Redactor over an immutable
// *secrets.Detector. The detector is rebuilt when the vault unlocks or the
// known values change, and dropped when it locks: from that moment masking
// falls back to token patterns and key-name assignments, which need no
// protected input. Rebuilds run on one supervised goroutine because the vault
// guard that triggers them must never block on I/O.
type SecretRedactor struct {
	known KnownValues
	wake  chan struct{}

	mu       sync.Mutex
	detector *secrets.Detector

	task       *supervise.Task
	once       sync.Once
	unregister func()
}

// NewSecretRedactor builds the redactor, registers its vault lock guard and
// starts the rebuild loop. A nil vault never unlocks, so the detector stays
// pattern-only. The context belongs to the supervised loop, never to the
// struct.
func NewSecretRedactor(ctx context.Context, v *vault.Service, known KnownValues) *SecretRedactor {
	r := &SecretRedactor{known: known, wake: make(chan struct{}, 1), detector: &secrets.Detector{}}
	if v != nil {
		r.unregister = v.RegisterLockGuard(func(snap vault.Snapshot) {
			if snap.State == vault.StateUnlocked {
				r.Refresh()
				return
			}
			r.mu.Lock()
			r.detector = &secrets.Detector{}
			r.mu.Unlock()
		})
	}
	r.task = supervise.Go(ctx, "app.transcript-redactor", r.run)
	return r
}

// Refresh schedules a detector rebuild. The vault guard calls it when the
// vault unlocks; owners whose known values change without a vault transition
// call it as well once an owner exists (Q-34).
func (r *SecretRedactor) Refresh() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Redact masks every detected span of s with a fixed kind marker.
func (r *SecretRedactor) Redact(s string) string {
	r.mu.Lock()
	d := r.detector
	r.mu.Unlock()
	return secrets.Mask(s, d.Find(s))
}

// Close unregisters the vault guard and waits for the rebuild loop.
func (r *SecretRedactor) Close() error {
	r.once.Do(func() {
		if r.unregister != nil {
			r.unregister()
			r.unregister = nil
		}
	})
	if err := r.task.Wait(); err != nil {
		return fmt.Errorf("wait for the transcript redactor: %w", err)
	}
	return nil
}

// run rebuilds the detector on every wake. A failed rebuild keeps the previous
// detector; the next nudge retries.
func (r *SecretRedactor) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
		}
		if err := r.rebuild(ctx); err != nil {
			slog.Warn("the transcript redactor did not rebuild", "err", err.Error())
		}
	}
}

func (r *SecretRedactor) rebuild(ctx context.Context) error {
	var values []secrets.KnownValue
	if r.known != nil {
		var err error
		if values, err = r.known(ctx); err != nil {
			return err
		}
	}
	detector := secrets.New(values, secrets.Options{})
	r.mu.Lock()
	r.detector = detector
	r.mu.Unlock()
	return nil
}
