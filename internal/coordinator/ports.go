package coordinator

import (
	"context"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

// Fence defines ownership and token boundaries for transaction execution.
type Fence struct {
	AdmissionToken   uint64 // set after admission
	OwnerIncarnation uint64
	QueueRevision    uint64 // used only when AdmissionToken == 0
}

// Transition describes an atomic state transition to be committed to the store.
type Transition struct {
	SessionID                 string
	RunID                     string
	CommandID                 string
	Kind                      string
	Principal                 string
	Target                    string
	Fence                     Fence
	ExpectedRunRevision       uint64
	ExpectedAdmissionRevision uint64
	Events                    []kernel.Event
	Result                    codec.ReceiptResult
	MetadataInput             []byte
	ProtectedInput            []byte // sealed by the adapter outside the transaction
	CorrelationID             string
}

// AppendResult is the outcome of a confirmed transition append.
type AppendResult struct {
	Duplicate         bool
	Receipt           codec.Receipt
	RunRevision       uint64
	AdmissionRevision uint64
}

// CommitResolution classifies whether an uncertain commit succeeded.
type CommitResolution string

// Valid outcomes of resolving an uncertain commit.
const (
	CommitResolutionCommitted    CommitResolution = "committed"
	CommitResolutionNotCommitted CommitResolution = "not_committed"
	CommitResolutionUnavailable  CommitResolution = "unavailable"
)

// Admission represents the session admission control row.
type Admission struct {
	SessionID        string
	ActiveRunID      string
	OwnerIncarnation uint64
	AdmissionToken   uint64
	Revision         uint64
	QueueRevision    uint64
	// ContextEpoch, ContextRevision and ContextStartRunID are the session's
	// current context continuity; epoch and revision are 0 while the session has
	// no history. A run after the first continues them (see initialContext).
	ContextEpoch      uint64
	ContextRevision   uint64
	ContextStartRunID string
}

// Store defines persistence operations for execution streams, admissions, and receipts.
type Store interface {
	Append(ctx context.Context, t *Transition) (AppendResult, error)
	ResolveCommit(ctx context.Context, commandID string) (CommitResolution, codec.Receipt, error)
	LoadRun(ctx context.Context, runID string) (*kernel.State, uint64, error)
	LoadAdmission(ctx context.Context, sessionID string) (Admission, error)
}

// Lease binds dispatched work to an executor attempt fence and admission token.
type Lease struct {
	SessionID        string
	RunID            string
	InvocationID     string
	AttemptID        string
	AdmissionToken   uint64
	OwnerIncarnation uint64
	ExecutorFence    uint64
}

// Work contains the lease and dispatch event for an asynchronous worker.
type Work struct {
	Lease    Lease
	Dispatch kernel.Event
}

// Observation conveys progress or terminal output reported by a worker.
type Observation struct {
	Lease          Lease
	Command        kernel.Command
	Facts          kernel.DecisionFacts
	MetadataInput  []byte
	ProtectedInput []byte
}

// Executor executes dispatched work items and reports observations.
type Executor interface {
	Execute(ctx context.Context, w *Work, report func(*Observation))
}
