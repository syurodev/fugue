package execstore

import (
	"fmt"

	"github.com/syurodev/fugue/internal/kernel"
)

// Queue authority cannot authorize execution. New commands and events must opt in
// here rather than silently inheriting authority when the kernel grows.
func isPermittedQueuedEvent(kind string, evt kernel.Event) bool {
	switch kind {
	case cmdAcceptRun:
		_, ok := evt.(*kernel.RunAcceptedEvent)
		return ok
	case cmdRequestStop:
		switch evt.(type) {
		case *kernel.RunStopRequestedEvent, *kernel.RunFinishedEvent,
			*kernel.TimerCancelledEvent, *kernel.RunBlockClearedEvent:
			return true
		}
	case cmdCloseRunWithoutPayload:
		switch evt.(type) {
		case *kernel.RunClosureConfirmedEvent, *kernel.RunFinishedEvent,
			*kernel.TimerCancelledEvent, *kernel.RunBlockClearedEvent:
			return true
		}
	}
	return false
}

func validateQueuedBatch(req *AppendRequest) error {
	if req.QueueFence == nil {
		return nil
	}
	switch req.Kind {
	case cmdAcceptRun, cmdRequestStop:
	case cmdCloseRunWithoutPayload:
		if len(req.MetadataInput) == 0 {
			return fmt.Errorf("%w: queued closure requires metadata-only input", ErrStaleFence)
		}
	// Queued-safe timer purposes and queue metadata commands must be added with
	// their owning phase; the current retry and active-time timers need admission.
	default:
		return fmt.Errorf("%w: command requires admission authority", ErrStaleFence)
	}
	for _, evt := range req.Events {
		if evt == nil {
			return fmt.Errorf("%w: event is nil", ErrInvalidBatch)
		}
		if !isPermittedQueuedEvent(req.Kind, evt) || evt.EventEnvelope().Token != 0 {
			return fmt.Errorf("%w: event not permitted under queue authority", ErrStaleFence)
		}
	}
	return nil
}
