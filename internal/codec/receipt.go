package codec

import (
	"bytes"
	"fmt"
)

// DedupStatus indicates the outcome of a receipt deduplication comparison.
type DedupStatus string

const (
	// DedupStatusMatch indicates the command invocation is identical to the existing receipt.
	DedupStatusMatch DedupStatus = "match"

	// DedupStatusConflict indicates the command ID collides with differing content or metadata.
	DedupStatusConflict DedupStatus = "conflict"

	// DedupStatusVaultRequired indicates the payload cannot be decrypted because the vault is locked.
	DedupStatusVaultRequired DedupStatus = "vault_required"
)

// ReceiptResult captures the outcome or event range recorded in a receipt.
type ReceiptResult struct {
	FirstSequence uint64
	LastSequence  uint64
	RejectionCode string
	Message       string
}

// IsRejection reports whether this receipt represents a rejected command.
func (r ReceiptResult) IsRejection() bool {
	return r.RejectionCode != ""
}

// Receipt represents a persisted or in-memory command execution receipt.
type Receipt struct {
	CommandID      string
	Kind           string
	Principal      string
	Target         string
	CodecVersion   string
	InputPayloadID string
	MetadataOnly   bool
	CanonicalInput []byte
	Result         ReceiptResult
}

// DedupRequest contains the parameters needed to evaluate deduplication of an incoming command.
type DedupRequest struct {
	Existing               Receipt
	ExistingInput          []byte
	ExistingInputAvailable bool
	NewKind                string
	NewPrincipal           string
	NewTarget              string
	NewCodecVersion        string
	NewCanonicalInput      []byte
}

// DedupDecision captures the result of receipt deduplication comparison.
type DedupDecision struct {
	Status  DedupStatus
	Receipt Receipt
}

// CheckDedup evaluates an incoming command against an existing receipt.
// Callers must query and supply an Existing receipt matching the command ID
// before invoking CheckDedup. If the canonical payload, kind, principal and
// target match exactly under the same codec version, DedupStatusMatch is
// returned along with the existing receipt. If any content or metadata differs,
// or if the codec version differs, a typed conflict error is returned. If the
// command requires opening a protected payload and the vault is locked,
// DedupStatusVaultRequired and ErrVaultRequired are returned.
func CheckDedup(req *DedupRequest) (DedupDecision, error) {
	if req == nil {
		return DedupDecision{Status: DedupStatusConflict, Receipt: Receipt{}}, ErrNilTarget
	}

	if req.Existing.CodecVersion != req.NewCodecVersion {
		return DedupDecision{Status: DedupStatusConflict, Receipt: req.Existing},
			fmt.Errorf("%w: existing=%s, new=%s", ErrCodecVersionMismatch, req.Existing.CodecVersion, req.NewCodecVersion)
	}

	if req.Existing.Kind != req.NewKind ||
		req.Existing.Principal != req.NewPrincipal ||
		req.Existing.Target != req.NewTarget {
		return DedupDecision{Status: DedupStatusConflict, Receipt: req.Existing},
			fmt.Errorf("%w: command metadata mismatch (kind, principal, or target)", ErrReceiptConflict)
	}

	if req.Existing.MetadataOnly {
		return checkMetadataOnlyDedup(req)
	}

	return checkPayloadDedup(req)
}

func checkMetadataOnlyDedup(req *DedupRequest) (DedupDecision, error) {
	if bytes.Equal(req.Existing.CanonicalInput, req.NewCanonicalInput) {
		return DedupDecision{
			Status:  DedupStatusMatch,
			Receipt: req.Existing,
		}, nil
	}
	return DedupDecision{Status: DedupStatusConflict, Receipt: req.Existing},
		fmt.Errorf("%w: metadata-only canonical content mismatch", ErrReceiptConflict)
}

func checkPayloadDedup(req *DedupRequest) (DedupDecision, error) {
	if !req.ExistingInputAvailable {
		return DedupDecision{
			Status:  DedupStatusVaultRequired,
			Receipt: req.Existing,
		}, ErrVaultRequired
	}

	if bytes.Equal(req.ExistingInput, req.NewCanonicalInput) {
		return DedupDecision{
			Status:  DedupStatusMatch,
			Receipt: req.Existing,
		}, nil
	}
	return DedupDecision{Status: DedupStatusConflict, Receipt: req.Existing},
		fmt.Errorf("%w: protected payload canonical content mismatch", ErrReceiptConflict)
}
