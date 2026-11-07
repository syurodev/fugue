package sealing

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// AssociatedData binds a payload to its identity and kind.
func AssociatedData(recordType, sessionID, payloadID, kind, keyID string) crypto.AssociatedData {
	return crypto.AssociatedData{RecordType: recordType, RecordID: fmt.Sprintf("%d:%s:%d:%s:%s", len(sessionID), sessionID, len(payloadID), payloadID, kind), KeyID: keyID}
}

// SealPayload encrypts and serializes a payload.
func SealPayload(random io.Reader, recordType, sessionID, payloadID, kind, keyID string, key, plaintext []byte) ([]byte, error) {
	env, err := crypto.SealWithRandom(random, AssociatedData(recordType, sessionID, payloadID, kind, keyID), key, plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal payload: %w", err)
	}
	out, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	return out, nil
}

// OpenPayload authenticates and decrypts a serialized payload.
func OpenPayload(recordType, sessionID, payloadID, kind, keyID string, key, envelope []byte) ([]byte, error) {
	var env crypto.Envelope
	if err := json.Unmarshal(envelope, &env); err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "invalid payload envelope", err)
	}
	plaintext, err := crypto.OpenWithAD(key, &env, AssociatedData(recordType, sessionID, payloadID, kind, keyID))
	if err != nil {
		return nil, errcode.Wrap(errcode.CodeOf(err), "open payload envelope", err)
	}
	return plaintext, nil
}
