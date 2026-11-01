package vault

import (
	"encoding/json"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	maxRecordPlaintext = 1_000_000
	maxRecordEnvelope  = 1_500_000
)

// RecordBinding names the immutable identity of a host-owned record. Build
// RecordType from the record's complete scope and never take these values from
// an untrusted client.
type RecordBinding struct {
	RecordType string
	RecordID   string
	KeyID      string
}

// SealRecord encrypts a bounded record body with the data key. The binding is
// authenticated as associated data and is not stored with the body.
func (s *Service) SealRecord(binding RecordBinding, plaintext []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state != StateUnlocked {
		return nil, ErrLocked
	}
	if err := validateBinding(binding); err != nil {
		return nil, err
	}
	if len(plaintext) == 0 || len(plaintext) > maxRecordPlaintext {
		return nil, errcode.New(errcode.InvalidArgument, "vault record is empty or too large")
	}
	envelope, err := crypto.SealV2(associatedData(binding), s.dek, plaintext)
	if err != nil {
		return nil, fail("seal the record", err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "encode the record failed", err)
	}
	return encoded, nil
}

// OpenRecord authenticates and decrypts a record under the host-owned binding.
// Any change of binding is rejected by the AEAD.
func (s *Service) OpenRecord(binding RecordBinding, encoded []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state != StateUnlocked {
		return nil, ErrLocked
	}
	if err := validateBinding(binding); err != nil {
		return nil, err
	}
	if len(encoded) == 0 || len(encoded) > maxRecordEnvelope {
		return nil, errcode.New(errcode.InvalidArgument, "vault record envelope is empty or too large")
	}
	var envelope crypto.Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "vault record envelope is invalid", err)
	}
	plaintext, err := crypto.OpenWithAD(s.dek, &envelope, associatedData(binding))
	if err != nil {
		return nil, fail("open the record", err)
	}
	// A record sealed elsewhere may carry more than SealRecord would accept.
	if len(plaintext) == 0 || len(plaintext) > maxRecordPlaintext {
		return nil, errcode.New(errcode.InvalidArgument, "vault record is empty or too large")
	}
	return plaintext, nil
}

func validateBinding(binding RecordBinding) error {
	if binding.RecordType == "" || binding.RecordID == "" || binding.KeyID == "" {
		return errcode.New(errcode.InvalidArgument, "vault record binding is incomplete")
	}
	return nil
}

func associatedData(binding RecordBinding) crypto.AssociatedData {
	return crypto.AssociatedData{RecordType: binding.RecordType, RecordID: binding.RecordID, KeyID: binding.KeyID}
}
