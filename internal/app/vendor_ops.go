package app

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// vendorOps deduplicates state-changing vendor commands by command_id with the
// F5.3a bounds (1024 entries, 10-minute retention after completion). Each
// operation runs detached from the RPC that started it, bounded only by its
// host maximum: a caller whose deadline expires stops waiting, the operation
// still finishes (stop order included), and a retry with the same command_id
// attaches to it instead of running it twice (Q-56 A). Only a digest of the
// canonical command is kept, never the prompt text.
type vendorOps struct {
	mu    sync.Mutex
	clock clock.Clock
	spawn func(name string, fn func(context.Context) error) *supervise.Task
	ops   map[string]*vendorOp
	order []string
}

type vendorOp struct {
	digest     [sha256.Size]byte
	done       chan struct{}
	view       VendorSessionView
	err        error
	finished   bool
	finishedAt time.Time
}

var errVendorCommandsBusy = errcode.New(errcode.Unavailable, "too many vendor commands are in flight")

func newVendorOps(clk clock.Clock, spawn func(string, func(context.Context) error) *supervise.Task) *vendorOps {
	return &vendorOps{clock: clk, spawn: spawn, ops: map[string]*vendorOp{}}
}

// run starts or attaches to the operation for commandID and waits for it while
// ctx lives. It reports done=false when ctx ended first; the operation goes on.
func (o *vendorOps) run(ctx context.Context, commandID string, canonical []string, limit time.Duration,
	fn func(context.Context) (VendorSessionView, error),
) (VendorSessionView, bool, error) {
	if commandID == "" {
		return VendorSessionView{}, false, errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	digest := sha256.Sum256([]byte(strings.Join(canonical, "\x00")))
	op, err := o.attach(commandID, digest, limit, fn)
	if err != nil {
		return VendorSessionView{}, false, err
	}
	select {
	case <-op.done:
		o.mu.Lock()
		defer o.mu.Unlock()
		return op.view, true, op.err
	case <-ctx.Done():
		return VendorSessionView{}, false, nil
	}
}

func (o *vendorOps) attach(commandID string, digest [sha256.Size]byte, limit time.Duration,
	fn func(context.Context) (VendorSessionView, error),
) (*vendorOp, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.evictLocked()
	if op, ok := o.ops[commandID]; ok {
		if op.digest != digest {
			return nil, errcode.New(errcode.VendorCommandConflict, "command_id was already used for a different command")
		}
		return op, nil
	}
	if len(o.ops) >= defaultDedupCapacity {
		return nil, errVendorCommandsBusy
	}
	op := &vendorOp{digest: digest, done: make(chan struct{})}
	o.ops[commandID] = op
	o.order = append(o.order, commandID)
	o.spawn("app.vendor-command", func(base context.Context) error {
		opCtx, cancel := context.WithTimeout(base, limit)
		defer cancel()
		view, opErr := fn(opCtx)
		o.mu.Lock()
		op.view, op.err, op.finished, op.finishedAt = view, opErr, true, o.clock.Now()
		o.mu.Unlock()
		close(op.done)
		return nil
	})
	return op, nil
}

// evictLocked drops finished operations past retention, and the oldest
// finished ones while the cache is full. In-flight operations are never
// evicted, so a retry always finds them.
func (o *vendorOps) evictLocked() {
	cutoff := o.clock.Now().Add(-defaultDedupTTL)
	kept := o.order[:0]
	for _, id := range o.order {
		op := o.ops[id]
		expired := op.finished && op.finishedAt.Before(cutoff)
		full := op.finished && len(o.ops) >= defaultDedupCapacity
		if expired || full {
			delete(o.ops, id)
			continue
		}
		kept = append(kept, id)
	}
	o.order = kept
}
