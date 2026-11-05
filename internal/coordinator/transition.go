package coordinator

import (
	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

func (s *Session) buildTransition(
	runID, cmdID, origin string,
	state *kernel.State,
	events []kernel.Event,
	req *Request,
) *Transition {
	fence := Fence{
		AdmissionToken:   0,
		OwnerIncarnation: s.cfg.OwnerIncarnation,
		QueueRevision:    s.admission.QueueRevision,
	}
	if state.Admitted {
		fence.AdmissionToken = s.admission.AdmissionToken
		fence.QueueRevision = 0
	}

	kind := req.Kind
	if kind == "" && req.Command != nil {
		kind = req.Command.CommandName()
	}
	principal := req.Principal
	if principal == "" {
		principal = origin
	}
	target := req.Target
	if target == "" {
		target = runID
	}

	var firstSeq, lastSeq uint64
	if len(events) > 0 {
		firstSeq = events[0].EventEnvelope().Sequence
		lastSeq = events[len(events)-1].EventEnvelope().Sequence
	}

	return &Transition{
		SessionID:                 s.cfg.SessionID,
		RunID:                     runID,
		CommandID:                 cmdID,
		Kind:                      kind,
		Principal:                 principal,
		Target:                    target,
		Fence:                     fence,
		ExpectedRunRevision:       s.runRevs[runID],
		ExpectedAdmissionRevision: s.admission.Revision,
		Events:                    events,
		Result: codec.ReceiptResult{
			FirstSequence: firstSeq,
			LastSequence:  lastSeq,
			RejectionCode: "",
			Message:       "",
		},
		MetadataInput:  req.MetadataInput,
		ProtectedInput: req.ProtectedInput,
		CorrelationID:  "",
	}
}
