package hostconn

import (
	"context"
	"fmt"
	"time"

	"github.com/syurodev/fugue/pkg/client"
)

// Background owns one app-background lease until the preference is disabled.
type Background struct {
	cancel  context.CancelFunc
	done    chan struct{}
	manager *Manager
}

// StartBackground starts the sole background owner, or returns nil if disabled or owned elsewhere.
func StartBackground(ctx context.Context, dir string, params client.Params, executable string) (*Background, error) {
	enabled, err := client.BackgroundEnabled(dir)
	if err != nil {
		return nil, fmt.Errorf("read background preference: %w", err)
	}
	if !enabled {
		return nil, client.ErrBackgroundDisabled
	}
	lock, err := client.TryBackgroundLock(dir)
	if err != nil {
		return nil, fmt.Errorf("acquire background ownership: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	b := &Background{cancel: cancel, done: make(chan struct{})}
	params.Kind = client.KindAppBackground
	updates := make(chan Update, 128)
	m := NewManager(&Config{DataDir: dir, Params: params, AutoStart: true, ExecutablePath: executable, Topics: []string{"host"}, Updates: updates})
	b.manager = m
	m.Start(ctx)
	go b.run(ctx, dir, lock, m, updates)
	return b, nil
}

func (b *Background) run(ctx context.Context, dir string, lock *client.BackgroundLock, m *Manager, updates <-chan Update) {
	defer close(b.done)
	defer closeHandle(lock)
	defer m.Stop()
	timer := RealClock{}.NewTimer(500 * time.Millisecond)
	defer func() { timer.Stop() }()
	for {
		select {
		case <-ctx.Done():
			return
		case u := <-updates:
			if backgroundFailed(u) {
				return
			}
		case <-timer.C():
			enabled, err := client.BackgroundEnabled(dir)
			if err != nil || !enabled {
				return
			}
			timer = RealClock{}.NewTimer(500 * time.Millisecond)
		}
	}
}

func backgroundFailed(u Update) bool {
	return u.State == StateIncompatible || (u.State == StateDisconnected && u.Reason != "connection lost" && u.Reason != "host not running")
}

// Done closes after the lease and background ownership are released.
func (b *Background) Done() <-chan struct{} { return b.done }

// Close releases the background lease and waits for its goroutines.
func (b *Background) Close() { b.cancel(); <-b.done }
