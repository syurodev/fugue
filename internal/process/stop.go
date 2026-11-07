//go:build unix

package process

import (
	"context"
	"fmt"
	"syscall"
	"time"
)

const (
	interruptWaitTimeout = 10 * time.Second
	sigintWaitTimeout    = 5 * time.Second
	sigtermWaitTimeout   = 5 * time.Second
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
)

// Stop executes the graceful shutdown escalation order specified in vendor-runtime §8:
//
// 1. Caller-supplied protocol interrupt -> wait up to 10 s on injected clock.
// 2. SIGINT to the process group -> wait up to 5 s on injected clock.
// 3. SIGTERM to the process group -> wait up to 5 s on injected clock.
// 4. SIGKILL to the process group.
//
// If ctx is canceled before or during Stop, escalation steps are skipped and Stop
// immediately issues SIGKILL to the process group, recording "context_canceled" in
// the returned StopProof.
//
// If the leader process exits while other members of the process group are still alive,
// Stop continues escalation until the process group is empty or SIGKILL has been delivered.
func (p *Process) Stop(ctx context.Context) (StopProof, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	pid := p.PID()
	if ctx.Err() != nil {
		return p.forceKillGroup(pid)
	}
	if proof, stopped := p.checkAlreadyStopped(pid); stopped {
		return proof, nil
	}

	canceled, done, interruptErr := p.tryInterrupt(ctx, pid)
	if canceled {
		return p.forceKillGroup(pid)
	}
	if done {
		return p.stopCompleted(interruptErr)
	}

	for _, step := range []func(context.Context, int) (bool, bool, error){p.runSigintStep, p.runSigtermStep} {
		stepDone, stepCanceled, err := step(ctx, pid)
		if stepCanceled {
			return p.forceKillGroup(pid)
		}
		if err != nil {
			return StopProof{Status: StopProofUnknown, Reason: ReasonKillFailed}, err
		}
		if stepDone {
			return p.stopCompleted(interruptErr)
		}
	}

	return p.finishSigkill(pid, interruptErr)
}

func (p *Process) checkAlreadyStopped(pid int) (StopProof, bool) {
	if p.isLeaderExited() && isGroupEmpty(pid) {
		<-p.stderrTask.Done()
		return StopProof{
			Status: StopProofUnknown,
			Reason: ReasonGroupEmpty,
		}, true
	}
	return StopProof{Status: StopProofUnknown, Reason: ""}, false
}

func (p *Process) tryInterrupt(ctx context.Context, pid int) (bool, bool, error) {
	if p.interrupt == nil || p.isLeaderExited() {
		return false, false, nil
	}
	done, canceled, err := p.runInterruptStep(ctx, pid)
	return canceled, done, err
}

func (p *Process) finishSigkill(pid int, interruptErr error) (StopProof, error) {
	proof, err := p.stepSigkill(pid)
	if err != nil {
		return proof, err
	}
	if interruptErr != nil && proof.Reason == ReasonGroupEmpty {
		proof.Reason = ReasonInterruptFailed
		return proof, interruptErr
	}
	return proof, nil
}

func (p *Process) isLeaderExited() bool {
	select {
	case <-p.exitCh:
		return true
	default:
		return false
	}
}

func (p *Process) runInterruptStep(ctx context.Context, pid int) (bool, bool, error) {
	var interruptErr error
	if err := p.interrupt(ctx); err != nil {
		interruptErr = fmt.Errorf("protocol interrupt: %w", err)
	}
	if p.onEscalation != nil {
		p.onEscalation("interrupt")
	}
	done, canceled := p.waitExitOrGroupEmpty(ctx, interruptWaitTimeout, pid)
	return done, canceled, interruptErr
}

func (p *Process) runSigintStep(ctx context.Context, pid int) (bool, bool, error) {
	if p.onEscalation != nil {
		p.onEscalation("sigint")
	}
	kErr := killGroup(pid, syscall.SIGINT)
	if kErr != nil {
		kErr = fmt.Errorf("sigint group: %w", kErr)
	}
	done, canceled := p.waitExitOrGroupEmpty(ctx, sigintWaitTimeout, pid)
	return done, canceled, kErr
}

func (p *Process) runSigtermStep(ctx context.Context, pid int) (bool, bool, error) {
	if p.onEscalation != nil {
		p.onEscalation("sigterm")
	}
	kErr := killGroup(pid, syscall.SIGTERM)
	if kErr != nil {
		kErr = fmt.Errorf("sigterm group: %w", kErr)
	}
	done, canceled := p.waitExitOrGroupEmpty(ctx, sigtermWaitTimeout, pid)
	return done, canceled, kErr
}

func (p *Process) stepSigkill(pid int) (StopProof, error) {
	if p.onEscalation != nil {
		p.onEscalation("sigkill")
	}
	kErr := killGroup(pid, syscall.SIGKILL)
	<-p.exitCh
	<-p.stderrTask.Done()
	if kErr != nil {
		return StopProof{
			Status: StopProofUnknown,
			Reason: ReasonKillFailed,
		}, fmt.Errorf("kill process group: %w", kErr)
	}
	if isGroupEmpty(pid) {
		return StopProof{
			Status: StopProofUnknown,
			Reason: ReasonGroupEmpty,
		}, nil
	}
	return StopProof{
		Status: StopProofUnknown,
		Reason: ReasonGroupNotEmpty,
	}, nil
}

func (p *Process) forceKillGroup(pid int) (StopProof, error) {
	if p.onEscalation != nil {
		p.onEscalation("sigkill")
	}
	kErr := killGroup(pid, syscall.SIGKILL)
	<-p.exitCh
	<-p.stderrTask.Done()
	if kErr != nil {
		return StopProof{
			Status: StopProofUnknown,
			Reason: ReasonKillFailed,
		}, fmt.Errorf("kill process group: %w", kErr)
	}
	if !isGroupEmpty(pid) {
		return StopProof{
			Status: StopProofUnknown,
			Reason: ReasonGroupNotEmpty,
		}, nil
	}
	return StopProof{
		Status: StopProofUnknown,
		Reason: ReasonContextCanceled,
	}, nil
}

func (p *Process) stopCompleted(interruptErr error) (StopProof, error) {
	<-p.stderrTask.Done()
	if interruptErr != nil {
		return StopProof{
			Status: StopProofUnknown,
			Reason: ReasonInterruptFailed,
		}, interruptErr
	}
	return StopProof{
		Status: StopProofUnknown,
		Reason: ReasonGroupEmpty,
	}, nil
}

func (p *Process) waitExitOrGroupEmpty(ctx context.Context, d time.Duration, pid int) (bool, bool) {
	timer := p.clock.NewTimer(d)
	defer timer.Stop()

	if p.isLeaderExited() {
		if isGroupEmpty(pid) {
			return true, false
		}
		select {
		case <-timer.C():
			return false, false
		case <-ctx.Done():
			return false, true
		}
	}

	select {
	case <-p.exitCh:
		if isGroupEmpty(pid) {
			return true, false
		}
		select {
		case <-timer.C():
			return false, false
		case <-ctx.Done():
			return false, true
		}
	case <-timer.C():
		return false, false
	case <-ctx.Done():
		return false, true
	}
}
