package crypto

import "testing"

func TestEnvelopePlaintextSize(t *testing.T) {
	t.Parallel()
	for _, plain := range [][]byte{nil, []byte("content")} {
		env := seal(t, plain)
		size, err := env.PlaintextSize()
		if err != nil || size != len(plain) {
			t.Fatalf("plaintext length: got %d, err %v", size, err)
		}
	}
	var absent *Envelope
	if _, err := absent.PlaintextSize(); err == nil {
		t.Fatal("nil envelope accepted")
	}
}
