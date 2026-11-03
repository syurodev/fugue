package codec

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/kernel"
)

const (
	reproSecretKey   = "REVIEW_SYNTHETIC_SECRET"
	reproSecretVal   = "1234567890123456789012345678901234567890"
	reproAcceptRun   = "AcceptRun"
	reproSafeRunID   = "safe"
	reproReplacement = "replacement"
)

// TestRegressionDuplicateKeyDiagnostics verifies duplicate key errors do not leak key names (R1).
func TestRegressionDuplicateKeyDiagnostics(t *testing.T) {
	t.Parallel()
	data := []byte(`{"` + reproSecretKey + `":1,"` + reproSecretKey + `":2}`)
	err := ValidateCanonicalJSON(data)
	if err == nil {
		t.Fatal("expected ErrDuplicateKey, got nil")
	}
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("expected ErrDuplicateKey, got %v", err)
	}
	if strings.Contains(err.Error(), reproSecretKey) {
		t.Errorf("diagnostic error leaked input key: %s", err)
	}
}

// TestRegressionUnknownFieldDiagnostics verifies unknown field errors do not leak field names (R1).
func TestRegressionUnknownFieldDiagnostics(t *testing.T) {
	t.Parallel()
	data := []byte(`{"` + reproSecretKey + `":"x"}`)
	_, err := DecodeCommand(reproAcceptRun, data)
	if err == nil {
		t.Fatal("expected ErrUnknownField, got nil")
	}
	if !errors.Is(err, ErrUnknownField) {
		t.Fatalf("expected ErrUnknownField, got %v", err)
	}
	if strings.Contains(err.Error(), reproSecretKey) {
		t.Errorf("diagnostic error leaked unknown field name: %s", err)
	}
}

// TestRegressionNumericTokenDiagnostics verifies range errors do not leak numeric values (R1).
func TestRegressionNumericTokenDiagnostics(t *testing.T) {
	t.Parallel()
	data := []byte(`{"x":` + reproSecretVal + `}`)
	err := ValidateCanonicalJSON(data)
	if err == nil {
		t.Fatal("expected ErrIntegerRange, got nil")
	}
	if !errors.Is(err, ErrIntegerRange) {
		t.Fatalf("expected ErrIntegerRange, got %v", err)
	}
	if strings.Contains(err.Error(), reproSecretVal) {
		t.Errorf("diagnostic error leaked numeric value: %s", err)
	}
}

// TestRegressionEncoderRejectsInvalidUTF8 verifies that EncodeCommand and MarshalCanonical
// reject invalid UTF-8 strings before information loss occurs (R2).
func TestRegressionEncoderRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()

	orig := &kernel.AcceptRunCommand{
		CommandID:       "cmd-1",
		RunID:           string([]byte{0xff}),
		Origin:          "origin-1",
		InputRef:        "in-1",
		SessionID:       "sess-1",
		QueueItemRef:    "qi-1",
		ProtocolBinding: "pb-1",
		InitialBudgets:  "{}",
		PriorityClass:   "default",
		PeerOutboxRef:   "po-1",
		GoalID:          "goal-1",
		GoalRound:       1,
	}
	raw, err := EncodeCommand(orig)
	if err == nil {
		t.Fatalf("expected error for invalid UTF-8, got raw: %s", raw)
	}
	if !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("expected ErrInvalidUTF8, got: %v", err)
	}

	mapOrig := map[string]string{"key": string([]byte{0xfe})}
	_, err = MarshalCanonical(mapOrig)
	if err == nil {
		t.Fatal("expected error for invalid UTF-8 in map value")
	}
	if !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("expected ErrInvalidUTF8, got: %v", err)
	}
}

// TestRegressionRejectsSemanticDuplicateField verifies that differing casing or ambiguous
// keys cannot bind to the same struct field or bypass canonical naming (R3).
func TestRegressionRejectsSemanticDuplicateField(t *testing.T) {
	t.Parallel()

	t.Run("two fields binding to same field rejected", func(t *testing.T) {
		t.Parallel()
		data := []byte(`{"RunID":"` + reproSafeRunID + `","runid":"` + reproReplacement + `"}`)
		cmd, err := DecodeCommand(reproAcceptRun, data)
		if err == nil {
			t.Fatalf("two fields bound to same RunID accepted: %v", cmd)
		}
		if !errors.Is(err, ErrUnknownField) && !errors.Is(err, ErrDuplicateKey) {
			t.Fatalf("expected ErrUnknownField or ErrDuplicateKey, got %v", err)
		}
	})

	t.Run("case mismatch rejected as unknown field", func(t *testing.T) {
		t.Parallel()
		data := []byte(`{"runid":"` + reproSafeRunID + `"}`)
		_, err := DecodeCommand(reproAcceptRun, data)
		if err == nil {
			t.Fatal("expected error for non-canonical case field, got nil")
		}
		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("expected ErrUnknownField, got %v", err)
		}
	})
}

// TestRegressionIntegerPrecisionPreservedInAny verifies that decoding into any preserves
// full int64 precision and integer semantics (R4).
func TestRegressionIntegerPrecisionPreservedInAny(t *testing.T) {
	t.Parallel()

	testValues := [][]byte{
		[]byte(`{"n":9007199254740993}`),
		[]byte(`{"n":-9007199254740993}`),
		[]byte(`{"n":9223372036854775807}`),
		[]byte(`{"n":-9223372036854775808}`),
	}

	for _, input := range testValues {
		var decoded any
		if err := UnmarshalCanonical(input, &decoded); err != nil {
			t.Fatalf("UnmarshalCanonical failed for %s: %v", input, err)
		}
		output, err := MarshalCanonical(decoded)
		if err != nil {
			t.Fatalf("MarshalCanonical failed: %v", err)
		}
		if !bytes.Equal(input, output) {
			t.Errorf("integer precision lost: %s -> %s", input, output)
		}

		m, ok := decoded.(map[string]any)
		if !ok {
			t.Fatalf("expected map[string]any, got %T", decoded)
		}
		if _, isInt64 := m["n"].(int64); !isInt64 {
			t.Errorf("expected int64 type in any map, got %T (%v)", m["n"], m["n"])
		}
	}
}

// TestRegressionNilCommandAndNullRejected verifies that typed nil and JSON null are
// rejected at the command boundary (R5).
func TestRegressionNilCommandAndNullRejected(t *testing.T) {
	t.Parallel()

	t.Run("typed nil command rejected by EncodeCommand", func(t *testing.T) {
		t.Parallel()
		var cmd *kernel.AcceptRunCommand
		encoded, err := EncodeCommand(cmd)
		if err == nil {
			t.Errorf("typed nil accepted as %s", encoded)
		}
		if !errors.Is(err, ErrNilTarget) {
			t.Fatalf("expected ErrNilTarget, got %v", err)
		}
	})

	t.Run("untyped nil command rejected by EncodeCommand", func(t *testing.T) {
		t.Parallel()
		_, err := EncodeCommand(nil)
		if !errors.Is(err, ErrNilTarget) {
			t.Fatalf("expected ErrNilTarget, got %v", err)
		}
	})

	t.Run("null JSON rejected by DecodeCommand", func(t *testing.T) {
		t.Parallel()
		decoded, err := DecodeCommand(reproAcceptRun, []byte(`null`))
		if err == nil {
			t.Errorf("null accepted as command: %#v", decoded)
		}
		if !errors.Is(err, ErrMalformedJSON) {
			t.Fatalf("expected ErrMalformedJSON, got %v", err)
		}
	})

	t.Run("whitespace null JSON rejected by DecodeCommand", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeCommand(reproAcceptRun, []byte("  null \n"))
		if err == nil {
			t.Fatal("expected error for whitespace null command, got nil")
		}
		if !errors.Is(err, ErrMalformedJSON) {
			t.Fatalf("expected ErrMalformedJSON, got %v", err)
		}
	})
}
