package sealing

import (
	"bytes"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestPayloadVectorAndRoundTrip(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{1}, 32)
	nonce := bytes.Repeat([]byte{2}, 24)
	plain := []byte("protected payload")
	wantBytes := []byte(`{"version":2,"algorithm":"xchacha20-poly1305","key_id":"v1","nonce":"AgICAgICAgICAgICAgICAgICAgICAgIC","ciphertext":"vBRZm88UiCjEQRHwCzwW/6m8sn/xCoF1e4Tt5o54oFSu"}`)
	got, err := SealPayload(bytes.NewReader(nonce), "exec.payload", "sess", "id", "message", "v1", key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantBytes) {
		t.Fatal("envelope differs from original")
	}
	opened, err := OpenPayload("exec.payload", "sess", "id", "message", "v1", key, got)
	if err != nil || !bytes.Equal(opened, plain) {
		t.Fatalf("round trip failed: %v", err)
	}
}
