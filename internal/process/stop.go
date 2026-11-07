//go:build unix

package process

import (
	"context"
	"syscall"
	"time"
)

const (
	interruptWaitTimeout = 10 * time.Second
	sigintWaitTimeout    = 5 * time.Second
	sigtermWaitTimeout   = 5 * time.Second
)

// Stop executes the graceful shutdown escalation order specified in vendor-runtime §8:
//
// 1. Caller-supplied protocol interrupt -> wait up to 10 s on injected clock.
// 2. SIGINT to the process group -> wait up to 5 s on injected clock.
// 3. SIGTERM to the process group -> wait up to 5 s on injected clock.
// 4. SIGKILL to the process group.
//
// It returns as soon as the process has exited.
func (p *Process) Stop(ctx context.Context) (StopProof, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	proof := StopProof{
		Status: StopProofUnknown,
		Reason: "process_group_no_proof",
	}

	// 0. If already exited, return immediately.
	select {
	case <-p.exitCh:
		<-p.stderrTask.Done()
		return proof, nil
	default:
	}

	pid := p.PID()

	// 1. Protocol interrupt (if provided) -> 10 s wait.
	if p.interrupt != nil {
		if err := p.interrupt(ctx); err != nil {
			proof.Reason = "interrupt_error: " + err.Error()
		}
		if p.onEscalation != nil {
			p.onEscalation("interrupt")
		}
		if p.waitExit(ctx, interruptWaitTimeout) {
			<-p.stderrTask.Done()
			return proof, nil
		}
	}

	// 2. SIGINT to group -> 5 s wait.
	killGroup(pid, syscall.SIGINT)
	if p.onEscalation != nil {
		p.onEscalation("sigint")
	}
	if p.waitExit(ctx, sigintWaitTimeout) {
		<-p.stderrTask.Done()
		return proof, nil
	}

	// 3. SIGTERM to group -> 5 s wait.
	killGroup(pid, syscall.SIGTERM)
	if p.onEscalation != nil {
		p.onEscalation("sigterm")
	}
	if p.waitExit(ctx, sigtermWaitTimeout) {
		<-p.stderrTask.Done()
		return proof, nil
	}

	// 4. SIGKILL to group.
	killGroup(pid, syscall.SIGKILL)
	if p.onEscalation != nil {
		p.onEscalation("sigkill")
	}

	// Wait for process to fully terminate.
	<-p.exitCh
	<-p.stderrTask.Done()

	return proof, nil
}

// waitExit waits for process termination on p.exitCh, or for timeout d on p.clock.
// Returns true if process exited, false on timeout or ctx cancellation.
func (p *Process) waitExit(ctx context.Context, d time.Duration) bool {
	timer := p.clock.NewTimer(d)
	defer timer.Stop()

	select {
	case <-p.exitCh:
		return true
	case <-timer.C():
		return false
	case <-ctx.Done():
		return false
	}
}
