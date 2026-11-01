package crypto

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"io"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// streamReader is a deterministic random source. The vectors in vectors_test.go
// were produced by the original dev-kit code reading exactly this stream
// through crypto/rand.Reader, so the same stream must yield the same bytes here.
type streamReader struct {
	counter uint64
	pending []byte
}

func newStream() io.Reader { return &streamReader{counter: 0, pending: nil} }

func (s *streamReader) Read(p []byte) (int, error) {
	for len(s.pending) < len(p) {
		var counter [8]byte
		binary.BigEndian.PutUint64(counter[:], s.counter)
		s.counter++
		block := sha256.Sum256(append([]byte("fugue-vector-stream"), counter[:]...))
		s.pending = append(s.pending, block[:]...)
	}
	n := copy(p, s.pending[:len(p)])
	s.pending = s.pending[n:]
	return n, nil
}

// testKey is a fixed AEAD key: 0x00, 0x01, ... 0x1f.
func testKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

const (
	testRecordType    = "note"
	testRecordID      = "record-1"
	testKeyID         = "note-body"
	primaryPhrase     = "master-pw"
	replacementPhrase = "new-pw"
)

func testAssociatedData() AssociatedData {
	return AssociatedData{RecordType: testRecordType, RecordID: testRecordID, KeyID: testKeyID}
}

func mustBase64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
