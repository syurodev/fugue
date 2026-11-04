package execstore

import (
	"fmt"
	"reflect"
	"unicode"

	"github.com/syurodev/fugue/internal/kernel"
)

// AllEventNames returns all 54 registered kernel event names.
func AllEventNames() []string {
	return []string{
		"RunAccepted",
		"RunAdmitted",
		"RunOwnershipAcquired",
		"RunStopRequested",
		"RunBlockAdded",
		"RunBlockCleared",
		"RunInterrupted",
		"RunRecoveryCleared",
		"UnknownAccepted",
		"RunClosureConfirmed",
		"ContextBoundarySelected",
		"RunFinished",
		"StepStarted",
		"ToolBindingRevised",
		"InstructionsLoaded",
		"ContextInjected",
		"ToolResultsPruned",
		"ContextCompacted",
		"InputSteered",
		"InputInterjected",
		"StepCompleted",
		"BudgetExtended",
		"StepAborted",
		"TimeBudgetWindowOpened",
		"TimeBudgetWindowClosed",
		"TimerScheduled",
		"TimerElapsed",
		"TimerCancelled",
		"BudgetObserved",
		"ModelRequestPlanned",
		"ModelDispatchStarted",
		"ModelStreamStarted",
		"ModelResponseRecorded",
		"ModelRequestFailed",
		"ModelRequestInterrupted",
		"ModelRetryScheduled",
		"ModelAttemptPlanned",
		"ModelAttemptCancelled",
		"ModelAttemptAbandoned",
		"ModelInvocationFailed",
		"ModelUsageReconciled",
		"ToolCallPlanned",
		"ToolCheckReset",
		"ToolCheckStarted",
		"ToolApprovalRequested",
		"ToolApprovalResolved",
		"ToolReadyRecorded",
		"ToolDispatchStarted",
		"ToolResultRecorded",
		"ToolOutcomeUnknown",
		"ToolReconciled",
		"ToolAttemptAbandoned",
		"ToolAttemptPlanned",
		"InvocationQuiesceRequested",
	}
}

func instantiateEvent(name string) (kernel.Event, error) {
	if evt := instantiateRunEventA(name); evt != nil {
		return evt, nil
	}
	if evt := instantiateRunEventB(name); evt != nil {
		return evt, nil
	}
	if evt := instantiateStepEventA(name); evt != nil {
		return evt, nil
	}
	if evt := instantiateStepEventB(name); evt != nil {
		return evt, nil
	}
	if evt := instantiateTimerEvent(name); evt != nil {
		return evt, nil
	}
	if evt := instantiateModelEventA(name); evt != nil {
		return evt, nil
	}
	if evt := instantiateModelEventB(name); evt != nil {
		return evt, nil
	}
	if evt := instantiateToolEventA(name); evt != nil {
		return evt, nil
	}
	if evt := instantiateToolEventB(name); evt != nil {
		return evt, nil
	}
	return nil, fmt.Errorf("%w: unrecognized event %q", ErrUnknownVersion, name)
}

func instantiateRunEventA(name string) kernel.Event {
	switch name {
	case "RunAccepted":
		return new(kernel.RunAcceptedEvent)
	case "RunAdmitted":
		return new(kernel.RunAdmittedEvent)
	case "RunOwnershipAcquired":
		return new(kernel.RunOwnershipAcquiredEvent)
	case "RunStopRequested":
		return new(kernel.RunStopRequestedEvent)
	case "RunBlockAdded":
		return new(kernel.RunBlockAddedEvent)
	case "RunBlockCleared":
		return new(kernel.RunBlockClearedEvent)
	default:
		return nil
	}
}

func instantiateRunEventB(name string) kernel.Event {
	switch name {
	case "RunInterrupted":
		return new(kernel.RunInterruptedEvent)
	case "RunRecoveryCleared":
		return new(kernel.RunRecoveryClearedEvent)
	case "UnknownAccepted":
		return new(kernel.UnknownAcceptedEvent)
	case "RunClosureConfirmed":
		return new(kernel.RunClosureConfirmedEvent)
	case "ContextBoundarySelected":
		return new(kernel.ContextBoundarySelectedEvent)
	case "RunFinished":
		return new(kernel.RunFinishedEvent)
	default:
		return nil
	}
}

func instantiateStepEventA(name string) kernel.Event {
	switch name {
	case "StepStarted":
		return new(kernel.StepStartedEvent)
	case "ToolBindingRevised":
		return new(kernel.ToolBindingRevisedEvent)
	case "InstructionsLoaded":
		return new(kernel.InstructionsLoadedEvent)
	case "ContextInjected":
		return new(kernel.ContextInjectedEvent)
	case "ToolResultsPruned":
		return new(kernel.ToolResultsPrunedEvent)
	case "ContextCompacted":
		return new(kernel.ContextCompactedEvent)
	default:
		return nil
	}
}

func instantiateStepEventB(name string) kernel.Event {
	switch name {
	case "InputSteered":
		return new(kernel.InputSteeredEvent)
	case "InputInterjected":
		return new(kernel.InputInterjectedEvent)
	case "StepCompleted":
		return new(kernel.StepCompletedEvent)
	case "BudgetExtended":
		return new(kernel.BudgetExtendedEvent)
	case "StepAborted":
		return new(kernel.StepAbortedEvent)
	default:
		return nil
	}
}

func instantiateTimerEvent(name string) kernel.Event {
	switch name {
	case "TimeBudgetWindowOpened":
		return new(kernel.TimeBudgetWindowOpenedEvent)
	case "TimeBudgetWindowClosed":
		return new(kernel.TimeBudgetWindowClosedEvent)
	case "TimerScheduled":
		return new(kernel.TimerScheduledEvent)
	case "TimerElapsed":
		return new(kernel.TimerElapsedEvent)
	case "TimerCancelled":
		return new(kernel.TimerCancelledEvent)
	case "BudgetObserved":
		return new(kernel.BudgetObservedEvent)
	default:
		return nil
	}
}

func instantiateModelEventA(name string) kernel.Event {
	switch name {
	case "ModelRequestPlanned":
		return new(kernel.ModelRequestPlannedEvent)
	case "ModelDispatchStarted":
		return new(kernel.ModelDispatchStartedEvent)
	case "ModelStreamStarted":
		return new(kernel.ModelStreamStartedEvent)
	case "ModelResponseRecorded":
		return new(kernel.ModelResponseRecordedEvent)
	case "ModelRequestFailed":
		return new(kernel.ModelRequestFailedEvent)
	case "ModelRequestInterrupted":
		return new(kernel.ModelRequestInterruptedEvent)
	default:
		return nil
	}
}

func instantiateModelEventB(name string) kernel.Event {
	switch name {
	case "ModelRetryScheduled":
		return new(kernel.ModelRetryScheduledEvent)
	case "ModelAttemptPlanned":
		return new(kernel.ModelAttemptPlannedEvent)
	case "ModelAttemptCancelled":
		return new(kernel.ModelAttemptCancelledEvent)
	case "ModelAttemptAbandoned":
		return new(kernel.ModelAttemptAbandonedEvent)
	case "ModelInvocationFailed":
		return new(kernel.ModelInvocationFailedEvent)
	case "ModelUsageReconciled":
		return new(kernel.ModelUsageReconciledEvent)
	default:
		return nil
	}
}

func instantiateToolEventA(name string) kernel.Event {
	switch name {
	case "ToolCallPlanned":
		return new(kernel.ToolCallPlannedEvent)
	case "ToolCheckReset":
		return new(kernel.ToolCheckResetEvent)
	case "ToolCheckStarted":
		return new(kernel.ToolCheckStartedEvent)
	case "ToolApprovalRequested":
		return new(kernel.ToolApprovalRequestedEvent)
	case "ToolApprovalResolved":
		return new(kernel.ToolApprovalResolvedEvent)
	case "ToolReadyRecorded":
		return new(kernel.ToolReadyRecordedEvent)
	case "ToolDispatchStarted":
		return new(kernel.ToolDispatchStartedEvent)
	default:
		return nil
	}
}

func instantiateToolEventB(name string) kernel.Event {
	switch name {
	case "ToolResultRecorded":
		return new(kernel.ToolResultRecordedEvent)
	case "ToolOutcomeUnknown":
		return new(kernel.ToolOutcomeUnknownEvent)
	case "ToolReconciled":
		return new(kernel.ToolReconciledEvent)
	case "ToolAttemptAbandoned":
		return new(kernel.ToolAttemptAbandonedEvent)
	case "ToolAttemptPlanned":
		return new(kernel.ToolAttemptPlannedEvent)
	case "InvocationQuiesceRequested":
		return new(kernel.InvocationQuiesceRequestedEvent)
	default:
		return nil
	}
}

func validateStringMetadata(fieldName, s string) error {
	maxLen := 256
	if fieldName == "SourcePath" {
		maxLen = 1024
	}
	if len(s) > maxLen {
		return fmt.Errorf("%w: field %s length %d exceeds max %d", ErrInvalidBatch, fieldName, len(s), maxLen)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: field %s contains control character", ErrInvalidBatch, fieldName)
		}
	}
	return nil
}

func validateSliceMetadata(fieldName string, val reflect.Value) error {
	for i := range val.Len() {
		if err := validateSafeMetadataValue(fmt.Sprintf("%s[%d]", fieldName, i), val.Index(i)); err != nil {
			return err
		}
	}
	return nil
}

func validateMapMetadata(fieldName string, val reflect.Value) error {
	iter := val.MapRange()
	for iter.Next() {
		k := iter.Key()
		if k.Kind() != reflect.String {
			return fmt.Errorf("%w: field %s map key must be string", ErrInvalidBatch, fieldName)
		}
		if err := validateSafeMetadataValue(fmt.Sprintf("%s.%s", fieldName, k.String()), iter.Value()); err != nil {
			return err
		}
	}
	return nil
}

func validateSafeMetadataValue(fieldName string, val reflect.Value) error {
	switch val.Kind() {
	case reflect.String:
		return validateStringMetadata(fieldName, val.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Bool, reflect.Struct:
		return nil
	case reflect.Slice:
		return validateSliceMetadata(fieldName, val)
	case reflect.Map:
		return validateMapMetadata(fieldName, val)
	case reflect.Invalid, reflect.Uintptr, reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128, reflect.Array, reflect.Chan,
		reflect.Func, reflect.Interface, reflect.Pointer, reflect.UnsafePointer:
		return fmt.Errorf("%w: field %s has unsupported kind %v", ErrInvalidBatch, fieldName, val.Kind())
	}
	return nil
}

func validateEventMetadata(evt kernel.Event) error {
	rv := reflect.ValueOf(evt)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("%w: event is not a struct", ErrInvalidBatch)
	}
	rt := rv.Type()
	for i := range rv.NumField() {
		field := rt.Field(i)
		if field.Name == "Envelope" {
			continue
		}
		val := rv.Field(i)
		if err := validateSafeMetadataValue(field.Name, val); err != nil {
			return err
		}
	}
	return nil
}
