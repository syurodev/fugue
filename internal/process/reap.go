//go:build unix

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Reap terminates any leftover processes belonging to a previously recorded
// session Identity after a host restart (vendor-runtime §11, VR-E06).
//
// When id.ScopeUnit is non-empty, Reap validates the "fugue-vendor-" prefix and
// ".scope" suffix, kills the cgroup scope, and returns StopProofProved once the
// cgroup is verified empty—even if the original leader PID has already exited.
//
// When id.ScopeUnit is empty, Reap reads the live process's start_token and
// compares it with id.StartToken. If the process is gone or its start_token
// differs (PID reuse), Reap returns ErrIdentityMismatch without signaling
// anything. It never re-runs a vendor command.
func Reap(ctx context.Context, id Identity, opts *ReapOptions) (StopProof, error) {
	if opts == nil || opts.Clock == nil {
		return StopProof{Status: StopProofUnknown, Reason: ReasonIdentityMismatch},
			errcode.New(errcode.InvalidArgument, "reap requires ReapOptions with a non-nil Clock")
	}
	clk := opts.Clock
	cgroupRoot := opts.CgroupRoot
	procRoot := opts.ProcRoot
	if id.ScopeUnit != "" {
		if !isValidScopeUnit(id.ScopeUnit) {
			return StopProof{Status: StopProofUnknown, Reason: ReasonScopeUnverified},
				fmt.Errorf("%w: invalid scope unit %q", ErrIdentityMismatch, id.ScopeUnit)
		}
		return reapPlatform(ctx, id, clk, cgroupRoot, procRoot)
	}
	if id.PID <= 1 || id.StartToken == "" {
		return StopProof{Status: StopProofUnknown, Reason: ReasonIdentityMismatch},
			errcode.Wrap(errcode.InvalidArgument, "reap requires positive pid and start_token", ErrIdentityMismatch)
	}
	return reapPlatform(ctx, id, clk, cgroupRoot, procRoot)
}

func reapContainedScope(ctx context.Context, unitName string, clk clock.Clock, cgroupRoot, procRoot string) (StopProof, error) {
	scope, err := findScopeDir(cgroupRoot, procRoot, unitName)
	if err != nil {
		return StopProof{Status: StopProofUnknown, Reason: ReasonScopeUnverified}, err
	}
	if scope.isEmpty() {
		return StopProof{Status: StopProofProved, Reason: ReasonCgroupEmpty}, nil
	}
	if err := scope.kill(0); err != nil {
		return StopProof{Status: StopProofUnknown, Reason: ReasonKillFailed},
			fmt.Errorf("reap cgroup %s: %w", unitName, err)
	}
	if !waitPollCondition(context.WithoutCancel(ctx), clk, finalReapTimeout, scope.isEmpty) {
		return StopProof{Status: StopProofUnknown, Reason: ReasonGroupNotEmpty}, nil
	}
	return StopProof{Status: StopProofProved, Reason: ReasonCgroupEmpty}, nil
}

func waitPollCondition(ctx context.Context, clk clock.Clock, timeout time.Duration, check func() bool) bool {
	deadline := clk.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if check() {
			return true
		}
		poll := clk.NewTimer(reapPollInterval)
		select {
		case <-poll.C():
		case <-deadline.C():
			poll.Stop()
			return check()
		case <-ctx.Done():
			poll.Stop()
			return check()
		}
		poll.Stop()
	}
}

func reapByStartToken(ctx context.Context, id Identity, clk clock.Clock, source procTableSource) (StopProof, error) {
	liveToken, err := source.startToken(id.PID)
	if err != nil || liveToken != id.StartToken {
		return StopProof{Status: StopProofUnknown, Reason: ReasonIdentityMismatch},
			fmt.Errorf("%w: pid %d start_token mismatch", ErrIdentityMismatch, id.PID)
	}

	tracker := newDescendantTracker(id.PID, id.StartToken, source, clk)
	tracker.snapshot()

	if err := killReapedTree(id.PID, tracker); err != nil {
		return StopProof{Status: StopProofUnknown, Reason: ReasonKillFailed}, err
	}

	gone := waitPollCondition(context.WithoutCancel(ctx), clk, finalReapTimeout, func() bool {
		return isReapedTreeGone(id, source, tracker)
	})
	if !gone {
		return StopProof{Status: StopProofUnknown, Reason: ReasonGroupNotEmpty}, nil
	}
	return StopProof{Status: StopProofUnknown, Reason: ReasonGroupEmpty}, nil
}

func killReapedTree(pid int, tracker *descendantTracker) error {
	var errs []error
	if groupHasMembers(pid) {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		errs = append(errs, err)
	}
	if err := tracker.signalTracked(syscall.SIGKILL); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("kill reaped tree %d: %w", pid, errors.Join(errs...))
	}
	return nil
}

func isReapedTreeGone(id Identity, source procTableSource, tracker *descendantTracker) bool {
	if tok, err := source.startToken(id.PID); err == nil && tok == id.StartToken {
		return false
	}
	return !groupHasMembers(id.PID) && !tracker.hasLiveDescendants()
}

// procfsScanner implements procTableSource over a Linux /proc directory.
type procfsScanner struct {
	procRoot string
}

func newProcfsScanner(procRoot string) *procfsScanner {
	if procRoot == "" {
		procRoot = defaultProcRoot
	}
	return &procfsScanner{procRoot: procRoot}
}

func (s *procfsScanner) readBootID() (string, error) {
	data, err := readRootFile(s.procRoot, "sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read boot_id: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *procfsScanner) startToken(pid int) (string, error) {
	if pid <= 1 {
		return "", fmt.Errorf("%w: invalid pid %d", ErrIdentityMismatch, pid)
	}
	bootID, err := s.readBootID()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrIdentityMismatch, err)
	}
	data, err := readRootFile(s.procRoot, strconv.Itoa(pid)+"/stat")
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrIdentityMismatch, err)
	}
	entry, err := parseProcStat(string(data), bootID)
	if err != nil {
		return "", err
	}
	return entry.startToken, nil
}

func (s *procfsScanner) scan() ([]procEntry, error) {
	bootID, err := s.readBootID()
	if err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(s.procRoot)
	if err != nil {
		return nil, fmt.Errorf("readdir %s: %w", s.procRoot, err)
	}
	var out []procEntry
	for _, d := range dirEntries {
		if !d.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(d.Name())
		if convErr != nil || pid <= 1 {
			continue
		}
		data, rerr := readRootFile(s.procRoot, d.Name()+"/stat")
		if rerr != nil {
			continue
		}
		entry, perr := parseProcStat(string(data), bootID)
		if perr == nil {
			out = append(out, entry)
		}
	}
	return out, nil
}
