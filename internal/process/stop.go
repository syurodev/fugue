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

	// reapPollInterval is how often a step re-probes the group once the
	// leader is gone; surviving members are reparented and reaped by init,
	// which takes a few milliseconds after they die.
	reapPollInterval = 10 * time.Millisecond
	// finalReapTimeout bounds the wait for the group to empty after SIGKILL.
	finalReapTimeout = time.Second
	// pipeSettleTimeout bounds the wait for stdout and stderr to reach EOF
	// once the tree is gone.
	pipeSettleTimeout = time.Second
)

const (
	// ReasonGroupEmpty indicates all processes in the process group have exited.
	ReasonGroupEmpty = "group_empty"

	// ReasonGroupNotEmpty indicates group members survived through SIGKILL.
	ReasonGroupNotEmpty = "group_not_empty"

	// ReasonContextCanceled indicates Stop aborted graceful escalation due to context cancellation.
	ReasonContextCanceled = "context_canceled"

	// ReasonInterruptFailed indicates caller-supplied protocol interrupt returned an error.
	ReasonInterruptFailed = "interrupt_failed"

	// ReasonKillFailed indicates signaling the process group failed.
	ReasonKillFailed = "kill_failed"

	// ReasonPipeHeldOpen indicates the group was gone but stdout or stderr
	// did not reach EOF in time, so a process outside the group (for example
	// a setsid grandchild) still holds a write end. Stop closed the read ends.
	ReasonPipeHeldOpen = "pipe_held_open"
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
// 2. SIGINT to the process group -> wait up to 5 s on injected clock.
// 3. SIGTERM to the process group -> wait up to 5 s on injected clock.
// 4. SIGKILL to the process group -> wait up to 1 s for the group to empty.
//
// A step ends early as soon as the leader has exited and the group is empty;
// once the leader is gone the group is re-probed every 10 ms on the clock.
// If the leader exits while other group members are alive, escalation goes on.
// The leader pid is signaled directly only until it is reaped.
//
// If ctx is canceled before or during Stop, the remaining steps are skipped and
// Stop sends SIGKILL to the group at once, recording "context_canceled".
//
// After the tree is stopped, Stop waits up to 1 s on the clock for stdout and
// stderr to reach EOF. A process that left the group (a setsid grandchild) can
// hold the write ends forever, so on timeout Stop closes every read end that has
// not ended and records "pipe_held_open"; any in-flight or subsequent read then
// returns promptly with an error instead of blocking forever. Escaped processes
// are reaped by F9.2b. When nothing reads stdout, EOF is never observed, so Stop
// waits the full 1 s and reports pipe_held_open. The vendor adapter must always
// run a stdout reader.
//
// StopProof.Reason holds only the fixed Reason* codes. A protocol interrupt or
// signal failure is returned as the error, wrapped with %w.
func (p *Process) Stop(ctx context.Context) (StopProof, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	reason, err := p.escalate(ctx)
	if p.settlePipes() && reason != ReasonKillFailed && reason != ReasonGroupNotEmpty {
		reason = ReasonPipeHeldOpen
	}
	return StopProof{Status: StopProofUnknown, Reason: reason}, err
}

func (p *Process) escalate(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return p.killTree(ctx, ReasonContextCanceled, nil)
	}
	if p.treeGone() {
		return ReasonGroupEmpty, nil
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
		if err := p.signalTree(step.sig); err != nil {
			return ReasonKillFailed, errors.Join(interruptErr, fmt.Errorf("%s process group: %w", step.name, err))
		}
		if reason, done, err := p.afterWait(ctx, p.waitTreeGone(ctx, step.timeout), interruptErr); done {
			return reason, err
		}
	}

	return p.killTree(ctx, completedReason(interruptErr), interruptErr)
}

// afterWait maps a step's wait outcome to a final result, or reports that
// escalation must continue.
func (p *Process) afterWait(ctx context.Context, outcome waitOutcome, interruptErr error) (string, bool, error) {
	switch outcome {
	case waitGone:
		return completedReason(interruptErr), true, interruptErr
	case waitCanceled:
		reason, err := p.killTree(ctx, ReasonContextCanceled, interruptErr)
		return reason, true, err
	case waitTimeout:
		return "", false, nil
	}
	return "", false, nil
}

func completedReason(interruptErr error) string {
	if interruptErr != nil {
		return ReasonInterruptFailed
	}
	return ReasonGroupEmpty
}

// killTree sends SIGKILL and waits a bounded time for the group to empty.
// The wait ignores cancellation: it is the last bounded step of Stop.
func (p *Process) killTree(ctx context.Context, goneReason string, interruptErr error) (string, error) {
	p.emit("sigkill")
	if err := p.signalTree(syscall.SIGKILL); err != nil {
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
	return p.isLeaderExited() && !groupHasMembers(p.PID())
}

// waitTreeGone waits up to d for the leader to exit and the group to empty.
// While the leader runs it waits on its exit; afterwards it polls the group
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
