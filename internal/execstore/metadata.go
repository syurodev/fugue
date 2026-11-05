package execstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

var errInvalidMetadataSchema = errors.New("invalid metadata-only schema")

type acceptRunMeta struct {
	InputRef *string `json:"InputRef,omitempty"`
}

type admitRunMeta struct {
	Token          *uint64 `json:"token,omitempty"`
	AdmissionToken *uint64 `json:"AdmissionToken,omitempty"`
	ContextEpoch   *uint64 `json:"ContextEpoch,omitempty"`
}

type acquireOwnershipMeta struct {
	OwnerIncarnation *uint64 `json:"OwnerIncarnation,omitempty"`
}

type advanceRunMeta struct {
	StepIndex *int `json:"StepIndex,omitempty"`
}

type requestStopMeta struct {
	Stop   *string            `json:"stop,omitempty"`
	Reason *kernel.StopReason `json:"Reason,omitempty"`
	Mode   *kernel.StopMode   `json:"Mode,omitempty"`
	Actor  *string            `json:"Actor,omitempty"`
}

// extendBudgetMeta holds only the kind, the amount and the actor of a user's
// budget extension: no payload, so the command commits while the vault is locked.
type extendBudgetMeta struct {
	Kind            *kernel.BudgetKind `json:"Kind,omitempty"`
	ExtensionAmount *int64             `json:"ExtensionAmount,omitempty"`
	Actor           *string            `json:"Actor,omitempty"`
}

type closeWithoutPayloadMeta struct {
	RunID                *string  `json:"RunID,omitempty"`
	RunRevision          *uint64  `json:"RunRevision,omitempty"`
	ExactAttemptIDs      []string `json:"ExactAttemptIDs,omitempty"`
	CauseIDs             []string `json:"CauseIDs,omitempty"`
	Actor                *string  `json:"Actor,omitempty"`
	UnavailabilityReason *string  `json:"UnavailabilityReason,omitempty"`
}

type selectRunContextMeta struct {
	OldContextRevision *uint64 `json:"OldContextRevision,omitempty"`
	NewContextRevision *uint64 `json:"NewContextRevision,omitempty"`
	Epoch              *uint64 `json:"Epoch,omitempty"`
	StartRunID         *string `json:"StartRunID,omitempty"`
	Mode               *string `json:"Mode,omitempty"`
	Actor              *string `json:"Actor,omitempty"`
	ValidationEvidence *string `json:"ValidationEvidence,omitempty"`
}

type resumeRunMeta struct {
	RunRevision *uint64 `json:"RunRevision,omitempty"`
}

func newMetadataTarget(kind string) (any, error) {
	switch kind {
	case cmdAcceptRun:
		return new(acceptRunMeta), nil
	case cmdAdmitRun:
		return new(admitRunMeta), nil
	case cmdAcquireRunOwnership:
		return new(acquireOwnershipMeta), nil
	case cmdAdvanceRun:
		return new(advanceRunMeta), nil
	case cmdRequestStop:
		return new(requestStopMeta), nil
	case cmdExtendBudget:
		return new(extendBudgetMeta), nil
	case cmdCloseRunWithoutPayload:
		return new(closeWithoutPayloadMeta), nil
	case cmdSelectRunContext:
		return new(selectRunContextMeta), nil
	case cmdResumeRun, cmdRecoverRun:
		return new(resumeRunMeta), nil
	default:
		return nil, fmt.Errorf("%w: command kind %q is not permitted as metadata-only", ErrInvalidBatch, kind)
	}
}

func validateMetadataSemantics(target any) error {
	switch meta := target.(type) {
	case *requestStopMeta:
		return validateStopMeta(meta)
	case *extendBudgetMeta:
		return validateExtendBudgetMeta(meta)
	}
	return nil
}

func validateExtendBudgetMeta(meta *extendBudgetMeta) error {
	if meta.Kind != nil {
		switch *meta.Kind {
		case kernel.BudgetKindSteps, kernel.BudgetKindTokens, kernel.BudgetKindTime, kernel.BudgetKindToolCalls:
		default:
			return fmt.Errorf("%w: %w", ErrInvalidBatch, errInvalidMetadataSchema)
		}
	}
	if meta.ExtensionAmount != nil && *meta.ExtensionAmount <= 0 {
		return fmt.Errorf("%w: %w", ErrInvalidBatch, errInvalidMetadataSchema)
	}
	return nil
}

func validateStopMeta(stopMeta *requestStopMeta) error {
	if stopMeta.Reason != nil {
		switch *stopMeta.Reason {
		case kernel.StopReasonUserCancel, kernel.StopReasonParentCancel,
			kernel.StopReasonBudget, kernel.StopReasonFatalError, kernel.StopReasonShutdown:
		default:
			return fmt.Errorf("%w: %w", ErrInvalidBatch, errInvalidMetadataSchema)
		}
	}
	if stopMeta.Mode != nil {
		switch *stopMeta.Mode {
		case kernel.StopModeTerminal, kernel.StopModeSuspend:
		default:
			return fmt.Errorf("%w: %w", ErrInvalidBatch, errInvalidMetadataSchema)
		}
	}
	return nil
}

func parseAndValidateMetadata(kind string, input []byte) ([]byte, error) {
	target, err := newMetadataTarget(kind)
	if err != nil {
		return nil, err
	}
	if unmarshalErr := codec.UnmarshalCanonical(input, target); unmarshalErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidBatch, errInvalidMetadataSchema)
	}
	if semErr := validateMetadataSemantics(target); semErr != nil {
		return nil, semErr
	}

	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	var raw map[string]any
	if decErr := dec.Decode(&raw); decErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidBatch, errInvalidMetadataSchema)
	}
	canonicalBytes, marshalErr := codec.MarshalCanonical(raw)
	if marshalErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidBatch, errInvalidMetadataSchema)
	}
	return canonicalBytes, nil
}

func isSafeAdmitField(field string) bool {
	return field == "token" || field == "AdmissionToken" || field == "ContextEpoch"
}

func isSafeStopField(field string) bool {
	return field == "stop" || field == "Reason" || field == "Mode" || field == fieldActor
}

func isSafeExtendBudgetField(field string) bool {
	return field == "Kind" || field == "ExtensionAmount" || field == fieldActor
}

func isSafeCloseField(field string) bool {
	switch field {
	case "RunID", fieldRunRevision, "ExactAttemptIDs", "CauseIDs", fieldActor, "UnavailabilityReason":
		return true
	default:
		return false
	}
}

func isSafeContextField(field string) bool {
	switch field {
	case "OldContextRevision", "NewContextRevision", "Epoch", "StartRunID", "Mode", fieldActor, "ValidationEvidence":
		return true
	default:
		return false
	}
}

func isSafeMetadataField(kind, field string) bool {
	switch kind {
	case cmdAcceptRun:
		return field == "InputRef"
	case cmdAdmitRun:
		return isSafeAdmitField(field)
	case cmdAcquireRunOwnership:
		return field == "OwnerIncarnation"
	case cmdAdvanceRun:
		return field == "StepIndex"
	case cmdRequestStop:
		return isSafeStopField(field)
	case cmdExtendBudget:
		return isSafeExtendBudgetField(field)
	case cmdCloseRunWithoutPayload:
		return isSafeCloseField(field)
	case cmdSelectRunContext:
		return isSafeContextField(field)
	case cmdResumeRun, cmdRecoverRun:
		return field == fieldRunRevision
	default:
		return false
	}
}

func isMetadataOnlyPermitted(kind string) bool {
	switch kind {
	case cmdAcceptRun, cmdAdmitRun, cmdAcquireRunOwnership, cmdAdvanceRun,
		cmdRequestStop, cmdExtendBudget, cmdCloseRunWithoutPayload, cmdSelectRunContext,
		cmdResumeRun, cmdRecoverRun:
		return true
	default:
		return false
	}
}
