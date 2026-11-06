package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/execstore"
	"github.com/syurodev/fugue/internal/kernel"
)

type coordinatorStore struct{ store *execstore.Store }

// NewCoordinatorStore connects the execution store to the coordinator's persistence port.
func NewCoordinatorStore(s *execstore.Store) coordinator.Store { return &coordinatorStore{store: s} }

func (s *coordinatorStore) Append(ctx context.Context, tr *coordinator.Transition) (coordinator.AppendResult, error) {
	req := appendRequest(tr)
	if err := s.sealInputs(ctx, tr, req); err != nil {
		return coordinator.AppendResult{}, err
	}
	out, err := s.store.Append(ctx, req)
	if err != nil {
		return coordinator.AppendResult{}, classifyAppendError(err)
	}
	return s.confirmedResult(ctx, tr, &out), nil
}

func (s *coordinatorStore) confirmedResult(ctx context.Context, tr *coordinator.Transition, out *execstore.AppendOutcome) coordinator.AppendResult {
	res := coordinator.AppendResult{Duplicate: out.Duplicate, Receipt: out.Receipt}
	run, runErr := s.store.ReadRun(ctx, tr.RunID)
	adm, admErr := s.store.ReadAdmission(ctx, tr.SessionID)
	res.RevisionsUnavailable = runErr != nil || admErr != nil
	if !res.RevisionsUnavailable {
		res.RunRevision, res.AdmissionRevision = run.Revision, adm.Revision
	}
	return res
}

func appendRequest(tr *coordinator.Transition) *execstore.AppendRequest {
	req := &execstore.AppendRequest{
		SessionID: tr.SessionID, RunID: tr.RunID, CommandID: tr.CommandID, Kind: tr.Kind, Principal: tr.Principal, Target: tr.Target,
		CodecVersion: codec.CodecV1, MetadataInput: tr.MetadataInput, CanonicalInput: tr.ProtectedInput,
		ExpectedRunRevision: tr.ExpectedRunRevision, ExpectedAdmissionRevision: tr.ExpectedAdmissionRevision,
		Events: tr.Events, Result: tr.Result, CorrelationID: tr.CorrelationID,
	}
	if req.ExpectedAdmissionRevision == 0 {
		req.ExpectedAdmissionRevision = 1
	}
	if tr.Fence.AdmissionToken != 0 {
		req.AdmissionFence = &execstore.AdmissionFence{Token: tr.Fence.AdmissionToken, OwnerIncarnation: tr.Fence.OwnerIncarnation}
	} else {
		req.QueueFence = &execstore.QueueFence{QueueRevision: tr.Fence.QueueRevision}
	}
	return req
}

func (s *coordinatorStore) sealInputs(ctx context.Context, tr *coordinator.Transition, req *execstore.AppendRequest) error {
	if len(tr.ProtectedInput) > 0 {
		p, key, err := s.store.SealPayload(ctx, tr.SessionID, "", "command_input", tr.ProtectedInput)
		if err != nil {
			return fmt.Errorf("seal command input: %w", err)
		}
		req.ProtectedInputID = p.PayloadID
		req.SealedPayloads = append(req.SealedPayloads, p)
		req.SealedSessionKey = key
	}
	for _, payload := range tr.Payloads {
		p, key, err := s.store.SealPayload(ctx, tr.SessionID, payload.ID, payload.Kind, payload.Plaintext)
		if err != nil {
			return fmt.Errorf("seal event payload: %w", err)
		}
		req.SealedPayloads = append(req.SealedPayloads, p)
		if req.SealedSessionKey == nil {
			req.SealedSessionKey = key
		}
	}
	return nil
}

// Only rejections whose no-commit guarantee is part of the store contract are
// definite. In particular, a database error can hide a successful COMMIT.
func classifyAppendError(err error) error {
	definite := []error{
		execstore.ErrStaleFence, execstore.ErrStaleRevision, execstore.ErrReceiptConflict,
		execstore.ErrInvalidBatch, execstore.ErrCodecVersionMismatch, execstore.ErrUnknownVersion,
		execstore.ErrSessionPurging, execstore.ErrPayloadTooLarge, execstore.ErrRunPayloadLimitExceeded,
		execstore.ErrSessionPayloadLimitExceeded, execstore.ErrProtectedInputUnavailable,
	}
	for _, sentinel := range definite {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("append rejected: %w", err)
		}
	}
	return fmt.Errorf("%w: %w", coordinator.ErrCommitUnknown, err)
}

func (s *coordinatorStore) ResolveCommit(ctx context.Context, commandID string) (coordinator.CommitResolution, codec.Receipt, error) {
	status, receipt, err := s.store.ResolveCommit(ctx, commandID)
	switch status {
	case execstore.CommitStatusCommitted:
		return coordinator.CommitResolutionCommitted, receipt, nil
	case execstore.CommitStatusNotCommitted:
		return coordinator.CommitResolutionNotCommitted, receipt, nil
	case execstore.CommitStatusUnavailable:
		return coordinator.CommitResolutionUnavailable, receipt, fmt.Errorf("resolve commit: %w", err)
	}
	return coordinator.CommitResolutionUnavailable, receipt, execstore.ErrStoreUnavailable
}

func (s *coordinatorStore) LoadRun(ctx context.Context, runID string) (*kernel.State, uint64, error) {
	state, err := s.store.RebuildRun(ctx, runID)
	if errors.Is(err, execstore.ErrRunNotFound) {
		return kernel.NewState(), 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("rebuild run: %w", err)
	}
	record, err := s.store.ReadRun(ctx, runID)
	if err != nil {
		return nil, 0, fmt.Errorf("read run revision: %w", err)
	}
	return state, record.Revision, nil
}

func (s *coordinatorStore) LoadAdmission(ctx context.Context, sessionID string) (coordinator.Admission, error) {
	row, err := s.store.ReadAdmission(ctx, sessionID)
	if err != nil {
		return coordinator.Admission{}, fmt.Errorf("read admission: %w", err)
	}
	return coordinator.Admission{
		SessionID: row.SessionID, ActiveRunID: row.ActiveRunID, OwnerIncarnation: row.OwnerIncarnation,
		AdmissionToken: row.AdmissionToken, Revision: row.Revision, QueueRevision: row.QueueRevision,
		ContextEpoch: row.ContextEpoch, ContextRevision: row.ContextRevision, ContextStartRunID: row.ContextStartRunID,
	}, nil
}
