package pureread

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const dataRevision = "in-memory-v1"

// Args selects one key from the fixture data supplied to New.
type Args struct {
	Key string
}

// Plan pins the selected key and fixture revision between preparation and execution.
// Its fields remain private so callers cannot replace the prepared selection.
type Plan struct {
	owner    *Tool
	key      string
	revision string
}

// Result contains model-facing content. An empty result is valid when the key is absent.
type Result struct {
	Content []byte
}

// Evidence reports preparation and execution counts for headless tests.
type Evidence struct {
	PrepareCalls int64
	ReadyPlans   int64
	ExecuteCalls int64
	Reads        int64
}

// Tool reads only from an immutable copy of injected fixture data.
type Tool struct {
	items        map[string][]byte
	prepareCalls atomic.Int64
	readyPlans   atomic.Int64
	executeCalls atomic.Int64
	reads        atomic.Int64
}

// New copies fixture data so later caller mutations cannot change tool behavior.
func New(items map[string][]byte) *Tool {
	owned := make(map[string][]byte, len(items))
	for key, value := range items {
		owned[key] = slices.Clone(value)
	}
	return &Tool{items: owned}
}

// Prepare validates arguments and pins a key and revision without reading result content.
func (t *Tool) Prepare(ctx context.Context, args Args) (Plan, error) {
	t.prepareCalls.Add(1)
	if ctx == nil {
		return Plan{}, errcode.New(errcode.InvalidArgument, "tool context is required")
	}
	if err := cancelled(ctx); err != nil {
		return Plan{}, err
	}
	if strings.TrimSpace(args.Key) == "" {
		return Plan{}, errcode.New(errcode.InvalidArgument, "invalid tool arguments")
	}
	plan := Plan{owner: t, key: args.Key, revision: dataRevision}
	t.readyPlans.Add(1)
	return plan, nil
}

// Execute revalidates the prepared selection and reads it once. This pure_read tool
// never asks for or consumes an approval receipt; supplied receipt references confer no authority.
func (t *Tool) Execute(ctx context.Context, plan Plan, _ []string) (Result, error) {
	t.executeCalls.Add(1)
	if ctx == nil {
		return Result{}, errcode.New(errcode.InvalidArgument, "tool context is required")
	}
	if err := cancelled(ctx); err != nil {
		return Result{}, err
	}
	if plan.owner != t || plan.revision != dataRevision || strings.TrimSpace(plan.key) == "" {
		return Result{}, errcode.New(errcode.FailedPrecondition, "prepared tool plan is invalid")
	}
	current := t.items[plan.key]
	t.reads.Add(1)
	return Result{Content: slices.Clone(current)}, nil
}

// Snapshot returns counts without exposing fixture data or plan content.
func (t *Tool) Snapshot() Evidence {
	return Evidence{
		PrepareCalls: t.prepareCalls.Load(), ReadyPlans: t.readyPlans.Load(),
		ExecuteCalls: t.executeCalls.Load(), Reads: t.reads.Load(),
	}
}

func cancelled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errcode.Wrap(errcode.CodeOf(err), "pure_read canceled", err)
	}
	return nil
}
