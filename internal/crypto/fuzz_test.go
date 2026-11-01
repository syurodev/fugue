package crypto

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzEnvelopeJSON feeds arbitrary stored bytes through the path a stored
// envelope takes: decode, Validate, then open. Nothing may panic, and anything
// that opens must have passed Validate.
func FuzzEnvelopeJSON(f *testing.F) {
	valid, err := sealV2(newStream(), testAssociatedData(), testKey(), []byte("seed"))
	if err != nil {
		f.Fatal(err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Add([]byte(`{"version":2,"algorithm":"xchacha20-poly1305","key_id":"note-body","nonce":"AAAA","ciphertext":"AAAA"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"version":2,"algorithm":"xchacha20-poly1305","key_id":"note-body","nonce":null,"ciphertext":null}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var envelope Envelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			return
		}
		plaintext, err := OpenWithAD(testKey(), &envelope, testAssociatedData())
		if err == nil && envelope.Validate() != nil {
			t.Fatalf("opened an envelope that does not validate (%d plaintext bytes)", len(plaintext))
		}
	})
}

// FuzzSealOpenRoundTrip checks that whatever Seal accepts, Open returns
// unchanged, and that a record identity other than the sealed one never opens it.
func FuzzSealOpenRoundTrip(f *testing.F) {
	f.Add([]byte("hello"), "note", "record-1", "key-1")
	f.Add([]byte(nil), "t", "r", "k")
	f.Add(bytes.Repeat([]byte{0}, 4096), "a b", "ü", "\xff")

	f.Fuzz(func(t *testing.T, plaintext []byte, recordType, recordID, keyID string) {
		associatedData := AssociatedData{RecordType: recordType, RecordID: recordID, KeyID: keyID}
		envelope, err := sealV2(newStream(), associatedData, testKey(), plaintext)
		if err != nil {
			return
		}
		opened, err := OpenWithAD(testKey(), &envelope, associatedData)
		if err != nil {
			t.Fatalf("sealed value does not open: %v", err)
		}
		if !bytes.Equal(opened, plaintext) {
			t.Fatal("round trip changed the plaintext")
		}
		associatedData.RecordID = recordID + "x"
		if _, err := OpenWithAD(testKey(), &envelope, associatedData); err == nil {
			t.Fatal("opened under a different record identity")
		}
	})
}
