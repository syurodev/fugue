package execstore

import (
	"fmt"
	"reflect"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

// CurrentSchemaVersion is the storage schema version written by this implementation.
const CurrentSchemaVersion uint64 = 1

// CurrentProtocolVersion is the event and execution protocol version written by this implementation.
const CurrentProtocolVersion uint64 = 1

func validateStorageEnvelope(env *kernel.Envelope, envelope *StorageEnvelope) error {
	if env.RunID == "" || env.Sequence == 0 || env.CommandID == "" || env.CausationID == "" || env.Origin == "" {
		return fmt.Errorf("%w: incomplete event envelope", ErrInvalidBatch)
	}
	if envelope == nil || envelope.EventID == "" || envelope.SessionID == "" {
		return fmt.Errorf("%w: incomplete storage envelope", ErrInvalidBatch)
	}
	return nil
}

func extractEventData(evt kernel.Event) (string, error) {
	rv := reflect.ValueOf(evt)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	rt := rv.Type()
	dataMap := make(map[string]any, rt.NumField())
	for i := range rt.NumField() {
		field := rt.Field(i)
		if field.Name == "Envelope" {
			continue
		}
		dataMap[field.Name] = rv.Field(i).Interface()
	}

	raw, err := codec.MarshalCanonical(dataMap)
	if err != nil {
		return "", fmt.Errorf("%w: marshal canonical event data: %w", ErrInvalidBatch, err)
	}
	return string(raw), nil
}

// EncodeEvent encodes a kernel.Event and storage metadata into a DurableEvent record.
func EncodeEvent(evt kernel.Event, envelope *StorageEnvelope) (*DurableEvent, error) {
	if evt == nil {
		return nil, fmt.Errorf("%w: event is nil", ErrInvalidBatch)
	}
	env := evt.EventEnvelope()
	if err := validateStorageEnvelope(&env, envelope); err != nil {
		return nil, err
	}
	if err := validateEventMetadata(evt); err != nil {
		return nil, err
	}

	raw, err := extractEventData(evt)
	if err != nil {
		return nil, err
	}

	schemaVer := envelope.SchemaVersion
	if schemaVer == 0 {
		schemaVer = CurrentSchemaVersion
	}
	protoVer := envelope.ProtocolVersion
	if protoVer == 0 {
		protoVer = CurrentProtocolVersion
	}
	recordedAt := envelope.RecordedAt
	if recordedAt.IsZero() {
		recordedAt = env.Timestamp
	}

	return &DurableEvent{
		RunID:             env.RunID,
		Sequence:          env.Sequence,
		EventID:           envelope.EventID,
		SessionID:         envelope.SessionID,
		EventType:         evt.EventName(),
		SchemaVersion:     schemaVer,
		ProtocolVersion:   protoVer,
		CommandID:         env.CommandID,
		CausationID:       env.CausationID,
		CorrelationID:     envelope.CorrelationID,
		Origin:            env.Origin,
		WriterIncarnation: envelope.WriterIncarnation,
		AdmissionToken:    env.Token,
		QueueRevision:     envelope.QueueRevision,
		RecordedAt:        recordedAt.UTC(),
		Data:              raw,
		PayloadID:         envelope.PayloadID,
	}, nil
}

// DecodeEvent decodes a DurableEvent record back into a typed kernel.Event with its envelope restored.
func DecodeEvent(d *DurableEvent) (kernel.Event, error) {
	if d == nil {
		return nil, fmt.Errorf("%w: durable event is nil", ErrCorruptHistory)
	}
	if d.SchemaVersion != CurrentSchemaVersion || d.ProtocolVersion != CurrentProtocolVersion {
		return nil, fmt.Errorf("%w: schema=%d, proto=%d", ErrUnknownVersion, d.SchemaVersion, d.ProtocolVersion)
	}

	evt, err := instantiateEvent(d.EventType)
	if err != nil {
		return nil, err
	}

	if err := codec.UnmarshalCanonical([]byte(d.Data), evt); err != nil {
		return nil, fmt.Errorf("%w: unmarshal event %s data: %w", ErrCorruptHistory, d.EventType, err)
	}

	env := kernel.Envelope{
		RunID:       d.RunID,
		Sequence:    d.Sequence,
		CommandID:   d.CommandID,
		CausationID: d.CausationID,
		Origin:      d.Origin,
		Token:       d.AdmissionToken,
		Timestamp:   d.RecordedAt.UTC(),
	}
	setEventEnvelope(evt, &env)

	if err := validateEventMetadata(evt); err != nil {
		return nil, fmt.Errorf("%w: decoded metadata validation failed: %w", ErrCorruptHistory, err)
	}

	return evt, nil
}

func setEventEnvelope(evt kernel.Event, env *kernel.Envelope) {
	rv := reflect.ValueOf(evt)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	f := rv.FieldByName("Envelope")
	if f.IsValid() && f.CanSet() {
		f.Set(reflect.ValueOf(*env))
	}
}
