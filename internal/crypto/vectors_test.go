package crypto

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The expected values below were produced by the dev-kit code this package was
// ported from (docs/ports.md), reading the deterministic stream of newStream.
// They must not be regenerated from this package.

func decodeKeyring(t *testing.T) *Keyring {
	t.Helper()
	raw, err := os.ReadFile("testdata/keyring.json")
	if err != nil {
		t.Fatal(err)
	}
	var keyring Keyring
	if err := json.Unmarshal(raw, &keyring); err != nil {
		t.Fatal(err)
	}
	return &keyring
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealMatchesTheOriginalVector(t *testing.T) {
	t.Parallel()
	want := Envelope{
		Version:    EnvelopeVersionV2,
		Algorithm:  AlgorithmXChaCha20Poly1305,
		KeyID:      testKeyID,
		Nonce:      mustBase64(t, "ZB02dKMSIklgAa7WFvcn3ENPeF2FEQC8"),
		Ciphertext: mustBase64(t, "zn/g1ZA9JP8AJNLfAE529QCC/W81+EoBJPo="),
	}
	got, err := sealV2(newStream(), testAssociatedData(), testKey(), []byte("top secret"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("envelope differs from the original (-want +got):\n%s", diff)
	}
	opened, err := OpenWithAD(testKey(), &want, testAssociatedData())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, []byte("top secret")) {
		t.Error("the original envelope no longer opens to its plaintext")
	}
}

func TestSealOfEmptyPlaintextMatchesTheOriginalVector(t *testing.T) {
	t.Parallel()
	want := Envelope{
		Version:    EnvelopeVersionV2,
		Algorithm:  AlgorithmXChaCha20Poly1305,
		KeyID:      testKeyID,
		Nonce:      mustBase64(t, "ZB02dKMSIklgAa7WFvcn3ENPeF2FEQC8"),
		Ciphertext: mustBase64(t, "SwsRG2/JTCpiKIlDIxRqEA=="),
	}
	got, err := sealV2(newStream(), testAssociatedData(), testKey(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("envelope differs from the original (-want +got):\n%s", diff)
	}
	opened, err := OpenWithAD(testKey(), &want, testAssociatedData())
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 0 {
		t.Errorf("opened %d bytes, want none", len(opened))
	}
}

func TestDeriveKeyMatchesTheOriginalVectors(t *testing.T) {
	t.Parallel()
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(0xa0 + i)
	}
	tests := []struct {
		name   string
		params KDFParams
		want   string
	}{
		{
			name:   "lowest accepted cost",
			params: KDFParams{Algorithm: AlgorithmArgon2id, Memory: 32 * 1024, Iterations: 2, Parallelism: 1, KeyLength: 32, Salt: salt},
			want:   "46d762e6a720c7f3d7260a87b3c79f0d6c64cafcf21b75b4ce1b4b5e7c6b0446",
		},
		{
			name:   "default cost",
			params: KDFParams{Algorithm: AlgorithmArgon2id, Memory: 64 * 1024, Iterations: 3, Parallelism: 4, KeyLength: 32, Salt: salt},
			want:   "078ee589f9ce0e7121d7e129f060e565b6d9aa3be37aa8e3f0a95f1655b24462",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := DeriveKey("correct horse", tc.params)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, mustHex(t, tc.want)) {
				t.Error("derived key differs from the original")
			}
		})
	}
}

func TestNewKeyringMatchesTheOriginalVector(t *testing.T) {
	t.Parallel()
	got, recovery, err := newKeyring(newStream(), primaryPhrase)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(decodeKeyring(t), got); diff != "" {
		t.Errorf("keyring differs from the original (-want +got):\n%s", diff)
	}
	if recovery != "f1EOZLClsIpLqjqSXEzBmuWpU72jIa5o" {
		t.Error("recovery key differs from the original")
	}
}

func TestTheOriginalKeyringStillUnlocks(t *testing.T) {
	t.Parallel()
	wantDEK := mustHex(t, "641d3674a31222496001aed616f727dc434f785d851100bc4071498719b802c7")
	keyring := decodeKeyring(t)

	byPassword, err := keyring.UnlockWithPassword(primaryPhrase)
	if err != nil {
		t.Fatal(err)
	}
	byRecovery, err := keyring.UnlockWithRecovery("f1EOZLClsIpLqjqSXEzBmuWpU72jIa5o")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(byPassword, wantDEK) || !bytes.Equal(byRecovery, wantDEK) {
		t.Error("the original keyring no longer unwraps to the original data key")
	}
}
