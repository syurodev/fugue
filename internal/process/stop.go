//go:build unix

package process

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

const (
	interruptWaitTimeout = 10 * time.Second
	sigintWaitTimeout    = 5 * time.Second
	sigtermWaitTimeout   = 5 * time.Second

	// reapPollInterval is how often a step re-probes the group, tracker, or
	// cgroup once the leader is gone; surviving members are reparented and
	// reaped by init, which takes a few milliseconds after they die.
	reapPollInterval = 10 * time.Millisecond
	// finalReapTimeout bounds the wait for the tree to empty after SIGKILL.
	finalReapTimeout = time.Second
	// pipeSettleTimeout bounds the wait for stdout and stderr to reach EOF
	// once the tree is gone.
	pipeSettleTimeout = time.Second
)

const (
	// ReasonCgroupEmpty indicates a Linux cgroup v2 scope was verified empty.
	ReasonCgroupEmpty = "cgroup_empty"

	// ReasonScopeUnavailable indicates systemd user scopes or cgroup v2 were unavailable.
	ReasonScopeUnavailable = "scope_unavailable"

	// ReasonScopeUnverified indicates /proc/<pid>/cgroup did not resolve to the expected scope unit.
	ReasonScopeUnverified = "scope_unverified"

	// ReasonGroupEmpty indicates all processes in the process group and tracker have exited.
	ReasonGroupEmpty = "group_empty"

	// ReasonGroupNotEmpty indicates processes survived through SIGKILL.
	ReasonGroupNotEmpty = "group_not_empty"

	// ReasonContextCanceled indicates Stop aborted graceful escalation due to context cancellation.
	ReasonContextCanceled = "context_canceled"

	// ReasonInterruptFailed indicates caller-supplied protocol interrupt returned an error.
	ReasonInterruptFailed = "interrupt_failed"

	// ReasonKillFailed indicates signaling the process group or cgroup failed.
	ReasonKillFailed = "kill_failed"

	// ReasonPipeHeldOpen indicates the tree was gone but stdout or stderr
	// did not reach EOF in time, so an untracked process outside the group
	// still holds a write end. Stop closed the read ends.
	ReasonPipeHeldOpen = "pipe_held_open"

	// ReasonIdentityMismatch indicates Reap refused to signal a PID whose start token did not match.
	ReasonIdentityMismatch = "identity_mismatch"
)

type waitOutcome int

const (
	waitGone waitOutcome = iota
	waitTimeout
	waitCanceled
)

type stopStep struct {
	name    string
	sig     syscall.Signal
	timeout time.Duration
}

// Stop executes the graceful shutdown escalation order specified in vendor-runtime §8:
//
// 1. Caller-supplied protocol interrupt -> wait up to 10 s on injected clock.
// 2. SIGINT to the process tree -> wait up to 5 s on injected clock.
// 3. SIGTERM to the process tree -> wait up to 5 s on injected clock.
// 4. SIGKILL (or cgroup.kill on Linux >= 5.14) -> wait up to 1 s for the tree to empty.
//
// Stop returns StopProofProved only when the session runs inside a verified
// Linux cgroup v2 scope and the cgroup is confirmed empty (`populated 0` in
// cgroup.events or an empty cgroup.procs). Every other path—macOS, uncontained
// Linux fallback (`scope_unavailable`), failed `/proc/<pid>/cgroup` verification
// (`scope_unverified`), or surviving processes—returns StopProofUnknown with a
// fixed Reason* code. Proof is never claimed from a signal result or the
// leader's exit alone.
//
// After the tree is stopped, Stop waits up to 1 s on the clock for stdout and
// stderr to reach EOF, closing any held read ends on timeout ("pipe_held_open").
func (p *Process) Stop(ctx context.Context) (StopProof, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tracker != nil {
		p.tracker.close()
		p.tracker.snapshot()
	}

	reason, err := p.escalate(ctx)
	if p.settlePipes() && reason != ReasonKillFailed && reason != ReasonGroupNotEmpty {
		reason = ReasonPipeHeldOpen
	}
	return p.buildStopProof(reason), err
}

func (p *Process) buildStopProof(reason string) StopProof {
	if p.scope != nil && p.scope.isEmpty() && reason != ReasonGroupNotEmpty && reason != ReasonKillFailed && reason != ReasonPipeHeldOpen {
		return StopProof{
			Status: StopProofProved,
			Reason: reason,
		}
	}
	if reason == ReasonGroupEmpty && p.uncontainedReason != "" {
		reason = p.uncontainedReason
	}
	return StopProof{
		Status: StopProofUnknown,
		Reason: reason,
	}
}

func (p *Process) defaultGoneReason() string {
	if p.scope != nil {
		return ReasonCgroupEmpty
	}
	return ReasonGroupEmpty
}

func (p *Process) escalate(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return p.killTree(ctx, ReasonContextCanceled, nil)
	}
	if p.treeGone() {
		return p.defaultGoneReason(), nil
	}

	var interruptErr error
	if p.interrupt != nil && !p.isLeaderExited() {
		if err := p.interrupt(ctx); err != nil {
			interruptErr = fmt.Errorf("protocol interrupt: %w", err)
		}
		p.emit("interrupt")
		if reason, done, err := p.afterWait(ctx, p.waitTreeGone(ctx, interruptWaitTimeout), interruptErr); done {
			return reason, err
		}
	}

	steps := []stopStep{
		{name: "sigint", sig: syscall.SIGINT, timeout: sigintWaitTimeout},
		{name: "sigterm", sig: syscall.SIGTERM, timeout: sigtermWaitTimeout},
	}
	for _, step := range steps {
		p.emit(step.name)
		if err := p.signalStep(step.sig); err != nil {
			return ReasonKillFailed, errors.Join(interruptErr, fmt.Errorf("%s process group: %w", step.name, err))
		}
		if reason, done, err := p.afterWait(ctx, p.waitTreeGone(ctx, step.timeout), interruptErr); done {
			return reason, err
		}
	}

	return p.killTree(ctx, p.completedReason(interruptErr), interruptErr)
}

func (p *Process) signalStep(sig syscall.Signal) error {
	var errs []error
	if err := p.signalTree(sig); err != nil {
		errs = append(errs, err)
	}
	if p.scope != nil {
		if err := p.scope.signalProcs(sig, p.PID()); err != nil {
			errs = append(errs, err)
		}
	}
	if p.tracker != nil {
		if err := p.tracker.signalTracked(sig); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *Process) killStep() error {
	var errs []error
	if err := p.signalTree(syscall.SIGKILL); err != nil {
		errs = append(errs, err)
	}
	if p.scope != nil {
		if err := p.scope.kill(p.PID()); err != nil {
			errs = append(errs, err)
		}
	}
	if p.tracker != nil {
		if err := p.tracker.signalTracked(syscall.SIGKILL); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// afterWait maps a step's wait outcome to a final result, or reports that
// escalation must continue.
func (p *Process) afterWait(ctx context.Context, outcome waitOutcome, interruptErr error) (string, bool, error) {
	switch outcome {
	case waitGone:
		return p.completedReason(interruptErr), true, interruptErr
	case waitCanceled:
		reason, err := p.killTree(ctx, ReasonContextCanceled, interruptErr)
		return reason, true, err
	case waitTimeout:
		return "", false, nil
	}
	return "", false, nil
}

func (p *Process) completedReason(interruptErr error) string {
	if interruptErr != nil {
		return ReasonInterruptFailed
	}
	return p.defaultGoneReason()
}

// killTree sends SIGKILL (and cgroup.kill when contained) and waits a bounded
// time for the tree to empty. The wait ignores cancellation: it is the last
// bounded step of Stop.
func (p *Process) killTree(ctx context.Context, goneReason string, interruptErr error) (string, error) {
	p.emit("sigkill")
	if err := p.killStep(); err != nil {
		return ReasonKillFailed, errors.Join(interruptErr, fmt.Errorf("sigkill process group: %w", err))
	}
	if p.waitTreeGone(context.WithoutCancel(ctx), finalReapTimeout) != waitGone {
		return ReasonGroupNotEmpty, interruptErr
	}
	return goneReason, interruptErr
}

func (p *Process) emit(step string) {
	if p.onEscalation != nil {
		p.onEscalation(step)
	}
}

func (p *Process) isLeaderExited() bool {
	select {
	case <-p.exitCh:
		return true
	default:
		return false
	}
}

func (p *Process) treeGone() bool {
	if !p.isLeaderExited() || groupHasMembers(p.PID()) {
		return false
	}
	if p.scope != nil {
		return p.scope.isEmpty()
	}
	if p.tracker != nil {
		return !p.tracker.hasLiveDescendants()
	}
	return true
}

// waitTreeGone waits up to d for the leader to exit and the tree to empty.
// While the leader runs it waits on its exit; afterwards it polls the tree
// every reapPollInterval on the clock.
func (p *Process) waitTreeGone(ctx context.Context, d time.Duration) waitOutcome {
	deadline := p.clock.NewTimer(d)
	defer deadline.Stop()
	for {
		if p.treeGone() {
			return waitGone
		}
		if outcome, done := p.waitTreeTick(ctx, deadline); done {
			return outcome
		}
	}
}

func (p *Process) waitTreeTick(ctx context.Context, deadline clock.Timer) (waitOutcome, bool) {
	exitC := p.exitCh
	var pollC <-chan time.Time
	if p.isLeaderExited() {
		poll := p.clock.NewTimer(reapPollInterval)
		defer poll.Stop()
		exitC = nil
		pollC = poll.C()
	}
	select {
	case <-exitC:
		return waitGone, false
	case <-pollC:
		return waitGone, false
	case <-deadline.C():
		if p.treeGone() {
			return waitGone, true
		}
		return waitTimeout, true
	case <-ctx.Done():
		return waitCanceled, true
	}
}

// settlePipes waits up to pipeSettleTimeout for stdout and stderr to reach
// EOF. On timeout it closes any read end that has not ended and reports that
// a pipe was held open.
func (p *Process) settlePipes() bool {
	deadline := p.clock.NewTimer(pipeSettleTimeout)
	defer deadline.Stop()
	for {
		stderrC, stdoutC := p.unsettledPipes()
		if stderrC == nil && stdoutC == nil {
			return false
		}
		poll := p.clock.NewTimer(reapPollInterval)
		select {
		case <-stderrC:
		case <-stdoutC:
		case <-poll.C():
		case <-deadline.C():
			poll.Stop()
			return p.closeHeldPipes()
		}
		poll.Stop()
	}
}

// unsettledPipes returns a channel for each pipe still being waited on, or
// nil for a pipe that is settled.
func (p *Process) unsettledPipes() (<-chan struct{}, <-chan struct{}) {
	var stderrC, stdoutC <-chan struct{}
	select {
	case <-p.stderrTask.Done():
	default:
		stderrC = p.stderrTask.Done()
	}
	if !p.stdoutPipe.settled() {
		stdoutC = p.stdoutPipe.ended
	}
	return stderrC, stdoutC
}

func (p *Process) closeHeldPipes() bool {
	stderrC, stdoutC := p.unsettledPipes()
	if stderrC == nil && stdoutC == nil {
		return false
	}
	if stderrC != nil {
		closeQuietly(p.stderrR)
		<-p.stderrTask.Done()
	}
	if stdoutC != nil {
		closeQuietly(p.stdoutPipe)
	}
	return true
}
