package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/workspace"
)

const (
	defaultPerAccountSessions = 6
	defaultMaxSessionsCap     = 8
	reservedHostMemoryBytes   = uint64(4) << 30
	memoryPerSessionBytes     = uint64(400) << 20
	teamTaskCPUWeight         = 50
)

// SchedulerTiers holds the resolved §7 concurrency limits for a host.
type SchedulerTiers struct {
	PerAccount    int
	MachineWide   int
	HeavyCommands int
}

// HostConfigReader reads the host's effective Tier 2 settings.
type HostConfigReader func(ctx context.Context) (workspace.HostConfig, error)

// ComputeSchedulerTiers derives the three §7 scheduler slot tiers from host
// hardware capacity and Tier 2 configuration overrides.
func ComputeSchedulerTiers(res HostResources, cfg *workspace.HostConfig) (SchedulerTiers, error) {
	var hostCfg workspace.HostConfig
	if cfg != nil {
		hostCfg = *cfg
	}
	resolved := resolveHostResources(res)
	perAccount := defaultPerAccountSessions
	if hostCfg.PerAccountSessions > 0 {
		perAccount = hostCfg.PerAccountSessions
	}
	machineWide := hostCfg.MaxSessions
	if machineWide <= 0 {
		ram, err := resolved.TotalMemoryBytes()
		if err != nil {
			return SchedulerTiers{}, errcode.Wrap(errcode.Internal, "inspect host memory", err)
		}
		machineWide = machineSessionCapForRAM(ram)
	}
	heavy := hostCfg.HeavyCommands
	if heavy <= 0 {
		heavy = max(1, resolved.CPUCores()/4)
	}
	return SchedulerTiers{
		PerAccount:    perAccount,
		MachineWide:   machineWide,
		HeavyCommands: heavy,
	}, nil
}

func machineSessionCapForRAM(totalBytes uint64) int {
	if totalBytes <= reservedHostMemoryBytes {
		return 1
	}
	slots := (totalBytes - reservedHostMemoryBytes) / memoryPerSessionBytes
	for capVal := defaultMaxSessionsCap; capVal >= 1; capVal-- {
		if slots >= uint64(capVal) {
			return capVal
		}
	}
	return 1
}

func globalHostConfigReader(ws *WorkspaceService) HostConfigReader {
	if ws == nil {
		return nil
	}
	return func(ctx context.Context) (workspace.HostConfig, error) {
		eff, err := ws.domain.GlobalEffectiveConfig(ctx)
		if err != nil {
			return workspace.HostConfig{}, publicWorkspaceError(err)
		}
		return eff.Host, nil
	}
}

type heavySemaphore struct {
	limit  func(context.Context) (int, error)
	mu     sync.Mutex
	inUse  int
	closed bool
	wait   chan struct{}
	done   chan struct{}
}

func newHeavySemaphore(res HostResources, read HostConfigReader) *heavySemaphore {
	resolved := resolveHostResources(res)
	return &heavySemaphore{
		done: make(chan struct{}),
		limit: func(ctx context.Context) (int, error) {
			if read != nil {
				cfg, err := read(ctx)
				if err != nil {
					return 0, err
				}
				if cfg.HeavyCommands > 0 {
					return cfg.HeavyCommands, nil
				}
			}
			return max(1, resolved.CPUCores()/4), nil
		},
	}
}

// Close stops future acquisitions and signals active stream holders to exit.
func (s *heavySemaphore) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
		if s.wait != nil {
			close(s.wait)
			s.wait = nil
		}
	}
	s.mu.Unlock()
}

// Acquire blocks until a heavy-command slot is available or ctx is cancelled,
// returning an idempotent release callback and a host-shutdown done channel.
func (s *heavySemaphore) Acquire(ctx context.Context) (func(), <-chan struct{}, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("wait for heavy command slot: %w", err)
		}
		limit, err := s.limit(ctx)
		if err != nil {
			return nil, nil, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, nil, errcode.New(errcode.Unavailable, "host is shutting down")
		}
		if s.inUse < limit {
			s.inUse++
			done := s.done
			s.mu.Unlock()
			var once sync.Once
			return func() { once.Do(s.release) }, done, nil
		}
		if s.wait == nil {
			s.wait = make(chan struct{})
		}
		ch := s.wait
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("wait for heavy command slot: %w", ctx.Err())
		case <-ch:
		}
	}
}

func (s *heavySemaphore) release() {
	s.mu.Lock()
	if s.inUse > 0 {
		s.inUse--
	}
	if s.wait != nil {
		close(s.wait)
		s.wait = nil
	}
	s.mu.Unlock()
}

// SchedulerTiers returns the host's current §7 slot tier limits.
func (rt *HostRuntime) SchedulerTiers(ctx context.Context) (SchedulerTiers, error) {
	var cfg workspace.HostConfig
	if read := globalHostConfigReader(rt.workspaces); read != nil {
		var err error
		cfg, err = read(ctx)
		if err != nil {
			return SchedulerTiers{}, err
		}
	}
	return ComputeSchedulerTiers(rt.hostResources, &cfg)
}

// AcquireHeavySlot acquires one heavy-command semaphore slot for build/test
// commands shared with `fugue exclusive`, returning a release func and a
// host-drain done channel.
func (rt *HostRuntime) AcquireHeavySlot(ctx context.Context) (func(), <-chan struct{}, error) {
	if rt.heavy == nil {
		return nil, nil, errcode.New(errcode.FailedPrecondition, "heavy command semaphore is not initialized")
	}
	return rt.heavy.Acquire(ctx)
}

// TeamQueueSnapshot returns queued auto-startable tasks in global dispatch
// priority order with 1-based queue positions.
func (rt *HostRuntime) TeamQueueSnapshot(ctx context.Context) ([]QueuedTaskPosition, error) {
	if rt.teamDispatch == nil {
		return nil, nil
	}
	return rt.teamDispatch.QueueSnapshot(ctx)
}
