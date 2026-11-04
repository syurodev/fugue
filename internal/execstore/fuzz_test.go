package execstore

import (
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/kernel"
)

func FuzzExecutionEventDecode(f *testing.F) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for _, name := range AllEventNames() {
		evt, err := instantiateEvent(name)
		if err != nil {
			continue
		}
		setEventEnvelope(evt, &kernel.Envelope{
			RunID:       "run-fuzz",
			Sequence:    1,
			CommandID:   "cmd-fuzz",
			CausationID: "cause-fuzz",
			Origin:      "user",
			Token:       1,
			Timestamp:   now,
		})
		durable, err := EncodeEvent(evt, &StorageEnvelope{
			EventID:           "evt-fuzz-" + name,
			SessionID:         "sess-fuzz",
			SchemaVersion:     CurrentSchemaVersion,
			ProtocolVersion:   CurrentProtocolVersion,
			CorrelationID:     "corr-fuzz",
			WriterIncarnation: 1,
			QueueRevision:     0,
			RecordedAt:        now,
			PayloadID:         "",
		})
		if err == nil {
			f.Add(durable.EventType, durable.SchemaVersion, durable.ProtocolVersion, durable.Data)
		}
	}

	f.Fuzz(func(_ *testing.T, eventType string, schemaVer, protoVer uint64, data string) {
		durable := &DurableEvent{
			RunID:             "run-fuzz",
			Sequence:          1,
			EventID:           "evt-fuzz",
			SessionID:         "sess-fuzz",
			EventType:         eventType,
			SchemaVersion:     schemaVer,
			ProtocolVersion:   protoVer,
			CommandID:         "cmd-fuzz",
			CausationID:       "cause-fuzz",
			CorrelationID:     "corr-fuzz",
			Origin:            "user",
			WriterIncarnation: 1,
			AdmissionToken:    1,
			QueueRevision:     0,
			RecordedAt:        now,
			Data:              data,
			PayloadID:         "",
		}
		if _, decErr := DecodeEvent(durable); decErr != nil {
			return
		}
	})
}
