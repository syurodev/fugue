package execstore

import (
	"context"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

// ProtectedEquality verifies equality of protected canonical inputs outside database transactions.
// Implementations are supplied by vault/payload managers (F2.5).
type ProtectedEquality interface {
	OpenPayload(ctx context.Context, sessionID, payloadID string) ([]byte, error)
}

// FaultPoint identifies a named write boundary for transactional fault injection.
type FaultPoint string

// Named fault injection points across transactional boundaries.
const (
	// FaultBeforeSessionKeyInsert triggers before session key insertion.
	FaultBeforeSessionKeyInsert FaultPoint = "before_session_key_insert"
	// FaultBeforePayloadInsert triggers before sealed payload insertion.
	FaultBeforePayloadInsert FaultPoint = "before_payload_insert"
	// FaultBeforeRunInsert triggers before execution run projection insertion.
	FaultBeforeRunInsert FaultPoint = "before_run_insert"
	// FaultBeforeEventAppend triggers before event records are appended.
	FaultBeforeEventAppend FaultPoint = "before_event_append"
	// FaultBeforeProjections triggers before invocation projections are persisted.
	FaultBeforeProjections FaultPoint = "before_projections"
	// FaultBeforeTranscript triggers before transcript projections are persisted.
	FaultBeforeTranscript FaultPoint = "before_transcript"
	// FaultBeforeReceiptInsert triggers before receipt record insertion.
	FaultBeforeReceiptInsert FaultPoint = "before_receipt_insert"
	// FaultBeforeAdmissionUpdate triggers before session admission row update.
	FaultBeforeAdmissionUpdate FaultPoint = "before_admission_update"
	// FaultBeforeCommit triggers immediately prior to COMMIT execution.
	FaultBeforeCommit FaultPoint = "before_commit"
	// FaultAfterCommit triggers immediately after COMMIT execution completes.
	FaultAfterCommit FaultPoint = "after_commit"
)

// CommitStatus reports the classified outcome of an uncertain commit.
type CommitStatus string

// Possible outcomes of resolving an uncertain commit.
const (
	// CommitStatusCommitted indicates the receipt was confirmed committed in durable storage.
	CommitStatusCommitted CommitStatus = "committed"
	// CommitStatusNotCommitted indicates the command was definitely not committed.
	CommitStatusNotCommitted CommitStatus = "not_committed"
	// CommitStatusUnavailable indicates the store could not verify commit state due to storage errors.
	CommitStatusUnavailable CommitStatus = "unavailable"
)

// AdmissionFence binds an append operation to an active admitted run.
type AdmissionFence struct {
	Token            uint64
	OwnerIncarnation uint64
}

// QueueFence binds a pre-admission operation to a queued run under session authority.
type QueueFence struct {
	QueueRevision uint64
}

// SealedPayload carries an already sealed payload record to be persisted atomically with events.
type SealedPayload struct {
	PayloadID string
	Kind      string
	KeyID     string
	Size      int64
	Envelope  []byte
}

// SealedSessionKey carries an already sealed session data key to be persisted atomically.
type SealedSessionKey struct {
	KeyID      string
	WrappedKey []byte
}

// AppendRequest specifies the complete inputs for an atomic transition append.
type AppendRequest struct {
	SessionID                 string
	RunID                     string
	CommandID                 string
	Kind                      string
	Principal                 string
	Target                    string
	CodecVersion              string
	ProtectedInputID          string
	MetadataInput             []byte
	CanonicalInput            []byte
	AdmissionFence            *AdmissionFence
	QueueFence                *QueueFence
	ExpectedRunRevision       uint64
	ExpectedAdmissionRevision uint64
	Events                    []kernel.Event
	Result                    codec.ReceiptResult
	SealedPayloads            []SealedPayload
	SealedSessionKey          *SealedSessionKey
	CorrelationID             string
}

// AppendOutcome describes the result of an Append operation.
type AppendOutcome struct {
	Committed bool
	Duplicate bool
	Receipt   codec.Receipt
}

// RunRecord represents the projected control state of an execution run.
type RunRecord struct {
	RunID           string
	SessionID       string
	LastSequence    uint64
	Revision        uint64
	ProtocolVersion uint64
	Lifecycle       kernel.Lifecycle
	Phase           kernel.Phase
	WaitReasons     []kernel.WaitReason
	Outcome         kernel.RunOutcome
	AdmissionToken  uint64
	StepIndex       int
	ContextEpoch    uint64
}

// StorageEnvelope holds durable metadata stored in exec_events columns alongside the event data.
type StorageEnvelope struct {
	EventID           string
	SessionID         string
	SchemaVersion     uint64
	ProtocolVersion   uint64
	CorrelationID     string
	WriterIncarnation uint64
	QueueRevision     uint64
	RecordedAt        time.Time
	PayloadID         string
}

// DurableEvent represents an event record stored in exec_events.
type DurableEvent struct {
	RunID             string
	Sequence          uint64
	EventID           string
	SessionID         string
	EventType         string
	SchemaVersion     uint64
	ProtocolVersion   uint64
	CommandID         string
	CausationID       string
	CorrelationID     string
	Origin            string
	WriterIncarnation uint64
	AdmissionToken    uint64
	QueueRevision     uint64
	RecordedAt        time.Time
	Data              string
	PayloadID         string
}
