package vault

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

func testBinding() RecordBinding {
	return RecordBinding{RecordType: testRecordType, RecordID: testRecordID, KeyID: "v1"}
}

func TestRecordRoundTrip(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	sealed, err := s.SealRecord(testBinding(), []byte("vault record body"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("vault record body")) {
		t.Error("the sealed record contains its plaintext")
	}
	opened, err := s.OpenRecord(testBinding(), sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, []byte("vault record body")) {
		t.Error("plaintext did not survive the round trip")
	}
}

// The golden record was produced by the dev-kit code (see testdata): the same
// data key, binding and plaintext must still open.
func TestARecordSealedByTheOriginalCodeStillOpens(t *testing.T) {
	t.Parallel()
	golden, err := os.ReadFile("testdata/record-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := unlocked(t).OpenRecord(testBinding(), bytes.TrimSpace(golden))
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != "vault record body" {
		t.Error("the original record opened to a different plaintext")
	}
}

func TestARecordIsBoundToItsIdentity(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	sealed, err := s.SealRecord(testBinding(), []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	other := map[string]RecordBinding{
		"type":   {RecordType: "other", RecordID: testRecordID, KeyID: "v1"},
		"ID":     {RecordType: testRecordType, RecordID: "sess-2", KeyID: "v1"},
		"key ID": {RecordType: testRecordType, RecordID: testRecordID, KeyID: "v2"},
	}
	for name, binding := range other {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := s.OpenRecord(binding, sealed)
			if err == nil {
				t.Fatal("a record opened under another identity")
			}
		})
	}
}

func TestRecordsNeedAnUnlockedVault(t *testing.T) {
	t.Parallel()
	s := newService(t)
	_, err := s.SealRecord(testBinding(), []byte("body"))
	if !errors.Is(err, ErrLocked) {
		t.Errorf("SealRecord: %v, want ErrLocked", err)
	}
	_, err = s.OpenRecord(testBinding(), []byte("{}"))
	if !errors.Is(err, ErrLocked) {
		t.Errorf("OpenRecord: %v, want ErrLocked", err)
	}
	wantCode(t, err, errcode.FailedPrecondition)
}

func TestIncompleteBindingsAreRejected(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	for name, binding := range map[string]RecordBinding{
		"no type":   {RecordType: "", RecordID: "r", KeyID: "k"},
		"no ID":     {RecordType: "t", RecordID: "", KeyID: "k"},
		"no key ID": {RecordType: "t", RecordID: "r", KeyID: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := s.SealRecord(binding, []byte("body"))
			wantCode(t, err, errcode.InvalidArgument)
			_, err = s.OpenRecord(binding, []byte("{}"))
			wantCode(t, err, errcode.InvalidArgument)
		})
	}
}

func TestSealRecordBounds(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	if _, err := s.SealRecord(testBinding(), bytes.Repeat([]byte{1}, maxRecordPlaintext)); err != nil {
		t.Errorf("largest record rejected: %v", err)
	}
	_, err := s.SealRecord(testBinding(), bytes.Repeat([]byte{1}, maxRecordPlaintext+1))
	wantCode(t, err, errcode.InvalidArgument)
	_, err = s.SealRecord(testBinding(), nil)
	wantCode(t, err, errcode.InvalidArgument)
}

func TestOpenRecordRejectsWhatIsNotARecord(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	tests := map[string][]byte{
		"empty":            nil,
		"too large":        bytes.Repeat([]byte{' '}, maxRecordEnvelope+1),
		"not JSON":         []byte("not json"),
		"empty envelope":   []byte("{}"),
		"unsupported kind": []byte(`{"version":3}`),
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := s.OpenRecord(testBinding(), encoded)
			wantCode(t, err, errcode.InvalidArgument)
		})
	}
}

func TestOpenRecordAcceptsAnEnvelopeAtTheSizeBound(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	sealed, err := s.SealRecord(testBinding(), []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	padded := append(bytes.Clone(sealed), bytes.Repeat([]byte{' '}, maxRecordEnvelope-len(sealed))...)
	if _, err = s.OpenRecord(testBinding(), padded); err != nil {
		t.Errorf("envelope of exactly %d bytes rejected: %v", maxRecordEnvelope, err)
	}
}

func TestOpenRecordRejectsABodyThatSealRecordWouldNotHaveProduced(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	key, err := hex.DecodeString(cheapDEK)
	if err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"empty": 0, "too large": maxRecordPlaintext + 1} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			envelope, err := crypto.SealV2(crypto.AssociatedData(testBinding()), key, make([]byte, size))
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.OpenRecord(testBinding(), encoded)
			wantCode(t, err, errcode.InvalidArgument)
		})
	}
}

func TestErrorsNeverContainTheBodyOrTheKey(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	sealed, err := s.SealRecord(testBinding(), []byte("the-secret-body"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.OpenRecord(RecordBinding{RecordType: "other", RecordID: testRecordID, KeyID: "v1"}, sealed)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "the-secret-body") || strings.Contains(err.Error(), cheapDEK) {
		t.Error("error text contains the body or the key")
	}
}
