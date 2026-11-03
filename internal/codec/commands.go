package codec

import (
	"bytes"

	"github.com/syurodev/fugue/internal/kernel"
)

// EncodeCommand encodes a kernel command into canonical JSON bytes.
func EncodeCommand(cmd kernel.Command) ([]byte, error) {
	if isNil(cmd) {
		return nil, ErrNilTarget
	}
	return MarshalCanonical(cmd)
}

// DecodeCommand decodes canonical JSON bytes into a typed kernel.Command
// corresponding to the given command kind name.
func DecodeCommand(kind string, data []byte) (kernel.Command, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, ErrMalformedJSON
	}
	cmd, err := instantiateCommand(kind)
	if err != nil {
		return nil, err
	}
	if err := UnmarshalCanonical(data, cmd); err != nil {
		return nil, err
	}
	if isNil(cmd) {
		return nil, ErrMalformedJSON
	}
	return cmd, nil
}

func instantiateCommand(kind string) (kernel.Command, error) {
	if cmd := newRunCommand(kind); cmd != nil {
		return cmd, nil
	}
	if cmd := newModelCommand(kind); cmd != nil {
		return cmd, nil
	}
	if cmd := newToolCommand(kind); cmd != nil {
		return cmd, nil
	}
	if cmd := newObservationCommand(kind); cmd != nil {
		return cmd, nil
	}
	if cmd := newControlCommand(kind); cmd != nil {
		return cmd, nil
	}
	return nil, ErrUnrecognizedCommand
}

func newRunCommand(kind string) kernel.Command {
	switch kind {
	case "AcceptRun":
		return new(kernel.AcceptRunCommand)
	case "AdmitRun":
		return new(kernel.AdmitRunCommand)
	case "AcquireRunOwnership":
		return new(kernel.AcquireRunOwnershipCommand)
	case "AdvanceRun":
		return new(kernel.AdvanceRunCommand)
	case "PrepareDispatch":
		return new(kernel.PrepareDispatchCommand)
	case "RecoverRun":
		return new(kernel.RecoverRunCommand)
	case "ResumeRun":
		return new(kernel.ResumeRunCommand)
	default:
		return nil
	}
}

func newModelCommand(kind string) kernel.Command {
	switch kind {
	case "ObserveModelStream":
		return new(kernel.ObserveModelStreamCommand)
	case "ObserveModelResponse":
		return new(kernel.ObserveModelResponseCommand)
	case "ObserveModelFailure":
		return new(kernel.ObserveModelFailureCommand)
	case "ObserveAccounting":
		return new(kernel.ObserveAccountingCommand)
	case "ExtendBudget":
		return new(kernel.ExtendBudgetCommand)
	default:
		return nil
	}
}

func newToolCommand(kind string) kernel.Command {
	switch kind {
	case "ObserveToolCheck":
		return new(kernel.ObserveToolCheckCommand)
	case "ObserveToolApproval":
		return new(kernel.ObserveToolApprovalCommand)
	case "ObserveToolResult":
		return new(kernel.ObserveToolResultCommand)
	case "ReviseToolBinding":
		return new(kernel.ReviseToolBindingCommand)
	default:
		return nil
	}
}

func newObservationCommand(kind string) kernel.Command {
	switch kind {
	case "ObserveVault":
		return new(kernel.ObserveVaultCommand)
	case "ObserveHostPause":
		return new(kernel.ObserveHostPauseCommand)
	case "ObserveDependency":
		return new(kernel.ObserveDependencyCommand)
	case "TimerFired":
		return new(kernel.TimerFiredCommand)
	default:
		return nil
	}
}

func newControlCommand(kind string) kernel.Command {
	switch kind {
	case "RequestStop":
		return new(kernel.RequestStopCommand)
	case "SteerInput":
		return new(kernel.SteerInputCommand)
	case "InterjectInput":
		return new(kernel.InterjectInputCommand)
	case "ReconcileInvocation":
		return new(kernel.ReconcileInvocationCommand)
	case "AcceptUnknown":
		return new(kernel.AcceptUnknownCommand)
	case "CloseRunWithoutPayload":
		return new(kernel.CloseRunWithoutPayloadCommand)
	case "SelectRunContext":
		return new(kernel.SelectRunContextCommand)
	default:
		return nil
	}
}
