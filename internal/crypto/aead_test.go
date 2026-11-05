package crypto

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func seal(t *testing.T, plaintext []byte) *Envelope {
	t.Helper()
	envelope, err := sealV2(newStream(), testAssociatedData(), testKey(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return &envelope
}

func wantCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", want)
	}
	if got := errcode.CodeOf(err); got != want {
		t.Fatalf("code = %q, want %q (%v)", got, want, err)
	}
}

func TestSealV2OpenWithADRoundTrip(t *testing.T) {
	t.Parallel()
	envelope, err := SealV2(testAssociatedData(), testKey(), []byte("top secret"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := OpenWithAD(testKey(), &envelope, testAssociatedData())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plaintext, []byte("top secret")) {
		t.Error("plaintext did not survive the round trip")
	}
}

func TestSealUsesAFreshNonceEachTime(t *testing.T) {
	t.Parallel()
	first, err := SealV2(testAssociatedData(), testKey(), []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := SealWithRandom(newStream(), testAssociatedData(), testKey(), []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Nonce, second.Nonce) || bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Error("two seals of the same plaintext produced the same nonce or ciphertext")
	}
}

func TestOpenFailsWithTheWrongKey(t *testing.T) {
	t.Parallel()
	wrong := testKey()
	wrong[0] ^= 0xFF
	_, err := OpenWithAD(wrong, seal(t, []byte("secret")), testAssociatedData())
	wantCode(t, err, errcode.PermissionDenied)
}

func TestOpenFailsWhenTheEnvelopeIsTampered(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Envelope){
		"ciphertext": func(e *Envelope) { e.Ciphertext[0] ^= 0xFF },
		"nonce":      func(e *Envelope) { e.Nonce[0] ^= 0xFF },
		"tag":        func(e *Envelope) { e.Ciphertext[len(e.Ciphertext)-1] ^= 0xFF },
	}
	for name, tamper := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			envelope := seal(t, []byte("secret"))
			tamper(envelope)
			_, err := OpenWithAD(testKey(), envelope, testAssociatedData())
			wantCode(t, err, errcode.PermissionDenied)
		})
	}
}

func TestOpenFailsWhenTheRecordIdentityChanges(t *testing.T) {
	t.Parallel()
	envelope := seal(t, []byte("secret"))
	tests := map[string]AssociatedData{
		"record type": {RecordType: "other", RecordID: testRecordID, KeyID: testKeyID},
		"record ID":   {RecordType: testRecordType, RecordID: "record-2", KeyID: testKeyID},
	}
	for name, associatedData := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := OpenWithAD(testKey(), envelope, associatedData)
			wantCode(t, err, errcode.PermissionDenied)
		})
	}
}

func TestOpenFailsWhenTheKeyIDDiffers(t *testing.T) {
	t.Parallel()
	_, err := OpenWithAD(testKey(), seal(t, []byte("secret")), AssociatedData{RecordType: testRecordType, RecordID: testRecordID, KeyID: "other"})
	wantCode(t, err, errcode.InvalidArgument)
}

func TestOpenRejectsAnEnvelopeThatIsNotV2(t *testing.T) {
	t.Parallel()
	envelope := seal(t, []byte("secret"))
	envelope.Version = 1
	_, err := OpenWithAD(testKey(), envelope, testAssociatedData())
	wantCode(t, err, errcode.InvalidArgument)
}

func TestOpenRejectsAMalformedEnvelopeWithoutPanicking(t *testing.T) {
	t.Parallel()
	tests := map[string]struct{ nonce, ciphertext []byte }{
		"short nonce":      {[]byte{1}, make([]byte, chacha20poly1305.Overhead)},
		"long nonce":       {make([]byte, chacha20poly1305.NonceSizeX+1), make([]byte, chacha20poly1305.Overhead)},
		"short ciphertext": {make([]byte, chacha20poly1305.NonceSizeX), make([]byte, chacha20poly1305.Overhead-1)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			envelope := &Envelope{
				Version:    EnvelopeVersionV2,
				Algorithm:  AlgorithmXChaCha20Poly1305,
				KeyID:      testAssociatedData().KeyID,
				Nonce:      tc.nonce,
				Ciphertext: tc.ciphertext,
			}
			_, err := OpenWithAD(testKey(), envelope, testAssociatedData())
			wantCode(t, err, errcode.InvalidArgument)
		})
	}
}

func TestSealAndOpenRejectABadKeyLength(t *testing.T) {
	t.Parallel()
	_, err := sealV2(newStream(), testAssociatedData(), []byte("short"), []byte("secret"))
	wantCode(t, err, errcode.InvalidArgument)
	_, err = OpenWithAD([]byte("short"), seal(t, []byte("secret")), testAssociatedData())
	wantCode(t, err, errcode.InvalidArgument)
}

func TestAssociatedDataIsValidatedOnSealAndOpen(t *testing.T) {
	t.Parallel()
	tests := map[string]AssociatedData{
		"empty record type": {RecordType: "", RecordID: "r", KeyID: "k"},
		"empty record ID":   {RecordType: "t", RecordID: "", KeyID: "k"},
		"empty key ID":      {RecordType: "t", RecordID: "r", KeyID: ""},
		"NUL in type":       {RecordType: "t\x00x", RecordID: "r", KeyID: "k"},
		"NUL in ID":         {RecordType: "t", RecordID: "r\x00x", KeyID: "k"},
		"NUL in key ID":     {RecordType: "t", RecordID: "r", KeyID: "k\x00x"},
	}
	for name, associatedData := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := sealV2(newStream(), associatedData, testKey(), []byte("secret"))
			wantCode(t, err, errcode.InvalidArgument)
			_, err = OpenWithAD(testKey(), seal(t, []byte("secret")), associatedData)
			wantCode(t, err, errcode.InvalidArgument)
		})
	}
}

func TestAssociatedDataFieldsCannotShiftBetweenEachOther(t *testing.T) {
	t.Parallel()
	// Without the NUL separator "ab"+"c" and "a"+"bc" would authenticate alike.
	first, err := sealV2(newStream(), AssociatedData{RecordType: "ab", RecordID: "c", KeyID: "k"}, testKey(), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = OpenWithAD(testKey(), &first, AssociatedData{RecordType: "a", RecordID: "bc", KeyID: "k"})
	wantCode(t, err, errcode.PermissionDenied)
}

func TestSealFailsWhenTheRandomSourceFails(t *testing.T) {
	t.Parallel()
	_, err := sealV2(io.LimitReader(newStream(), 5), testAssociatedData(), testKey(), []byte("secret"))
	wantCode(t, err, errcode.Internal)
}

func TestSealRejectsAPlaintextWhoseCiphertextExceedsTheBound(t *testing.T) {
	t.Parallel()
	fits := bytes.Repeat([]byte{1}, MaxCiphertextBytes-chacha20poly1305.Overhead)
	if _, err := sealV2(newStream(), testAssociatedData(), testKey(), fits); err != nil {
		t.Fatalf("largest allowed plaintext rejected: %v", err)
	}
	_, err := sealV2(newStream(), testAssociatedData(), testKey(), append(fits, 1))
	wantCode(t, err, errcode.InvalidArgument)
}

func TestErrorsNeverCarryKeyMaterialOrPlaintext(t *testing.T) {
	t.Parallel()
	const secret = "top-secret-plaintext"
	wrong := testKey()
	wrong[0] ^= 0xFF
	_, err := OpenWithAD(wrong, seal(t, []byte(secret)), testAssociatedData())
	if err == nil {
		t.Fatal("open with the wrong key succeeded")
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("error %T is not an errcode.Error", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), string(testKey())) {
		t.Error("error text contains the plaintext or the key")
	}
}
