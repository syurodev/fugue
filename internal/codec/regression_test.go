package codec

import (
	"bytes"
	"errors"
	"reflect"
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

// TestRegressionUTF8DepthDoesNotFailOpen verifies that deep nesting does not fail open
// or bypass UTF-8 validation (R6).
func TestRegressionUTF8DepthDoesNotFailOpen(t *testing.T) {
	t.Parallel()
	var value any = string([]byte{0xff})
	for range 70 {
		value = []any{value}
	}
	encoded, err := MarshalCanonical(value)
	if !errors.Is(err, ErrInvalidUTF8) {
		t.Errorf("invalid nested UTF-8 accepted: error=%v replacement=%v", err, strings.Contains(string(encoded), `\ufffd`))
	}
}

// TestRegressionNestedStructBinding verifies exact-case validation is enforced
// across slices, maps, and pointer targets (R7).
func TestRegressionNestedStructBinding(t *testing.T) {
	t.Parallel()
	type nestedItem struct{ RunID string }

	t.Run("slice", func(t *testing.T) {
		t.Parallel()
		var dst struct{ Items []nestedItem }
		err := UnmarshalCanonical([]byte(`{"Items":[{"RunID":"safe","runid":"replacement"}]}`), &dst)
		if err == nil {
			t.Errorf("ambiguous nested slice field accepted: %+v", dst)
		}
		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("expected ErrUnknownField, got %v", err)
		}
	})

	t.Run("map", func(t *testing.T) {
		t.Parallel()
		var dst struct{ Items map[string]nestedItem }
		err := UnmarshalCanonical([]byte(`{"Items":{"a":{"RunID":"safe","runid":"replacement"}}}`), &dst)
		if err == nil {
			t.Errorf("ambiguous nested map field accepted: %+v", dst)
		}
		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("expected ErrUnknownField, got %v", err)
		}
	})

	t.Run("pointer", func(t *testing.T) {
		t.Parallel()
		var dst *nestedItem
		err := UnmarshalCanonical([]byte(`{"RunID":"safe","runid":"replacement"}`), &dst)
		if err == nil {
			t.Errorf("ambiguous pointer target accepted: %+v", dst)
		}
		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("expected ErrUnknownField, got %v", err)
		}
	})
}

// TestRegressionNilStructFieldRoundTrip verifies nil struct pointer fields round-trip
// cleanly without erroneous rejection (R8).
func TestRegressionNilStructFieldRoundTrip(t *testing.T) {
	t.Parallel()
	type child struct{ Name string }
	type parent struct{ Child *child }
	original := parent{Child: nil}
	encoded, err := MarshalCanonical(original)
	if err != nil {
		t.Fatalf("MarshalCanonical failed: %v", err)
	}
	var decoded parent
	if err := UnmarshalCanonical(encoded, &decoded); err != nil {
		t.Fatalf("codec rejected own valid output %s: %v", encoded, err)
	}
	if decoded.Child != nil {
		t.Errorf("expected nil Child, got %+v", decoded.Child)
	}
}

// TestRegressionEmbeddedStructRoundTrip verifies anonymous embedded structs have
// promoted fields recognized and round-trip successfully (R8).
func TestRegressionEmbeddedStructRoundTrip(t *testing.T) {
	t.Parallel()
	type Header struct{ RunID string }
	type body struct {
		Header
		Name string
	}
	original := body{
		Header: Header{RunID: "run"},
		Name:   "name",
	}
	encoded, err := MarshalCanonical(original)
	if err != nil {
		t.Fatalf("MarshalCanonical failed: %v", err)
	}
	var decoded body
	if err := UnmarshalCanonical(encoded, &decoded); err != nil {
		t.Fatalf("codec rejected own valid output %s: %v", encoded, err)
	}
	if decoded.RunID != "run" || decoded.Name != "name" {
		t.Errorf("unexpected decoded result: %+v", decoded)
	}
}

// TestRegressionTraversalDepthLimitsEncoder verifies depth boundaries for UTF-8 encoding.
func TestRegressionTraversalDepthLimitsEncoder(t *testing.T) {
	t.Parallel()

	if err := validateUTF8Value(reflect.ValueOf("valid"), maxTraversalDepth); err != nil {
		t.Fatalf("expected valid at limit, got %v", err)
	}

	if err := validateUTF8Value(reflect.ValueOf("valid"), maxTraversalDepth+1); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("expected ErrMalformedJSON over limit, got %v", err)
	}

	var val any = "valid"
	for range 501 {
		val = []any{val}
	}
	if err := validateUTF8(val); err != nil {
		t.Fatalf("expected 501 JSON containers to be within limit, got %v", err)
	}
}

// TestRegressionTraversalDepthLimitsDecoder verifies depth boundaries for JSON decoding.
func TestRegressionTraversalDepthLimitsDecoder(t *testing.T) {
	t.Parallel()
	atLimit := bytes.Repeat([]byte("["), maxTraversalDepth)
	atLimit = append(atLimit, []byte("1")...)
	atLimit = append(atLimit, bytes.Repeat([]byte("]"), maxTraversalDepth)...)
	var dstAtLimit any
	if err := UnmarshalCanonical(atLimit, &dstAtLimit); err != nil {
		t.Fatalf("expected valid at limit, got %v", err)
	}

	overLimit := bytes.Repeat([]byte("["), maxTraversalDepth+1)
	overLimit = append(overLimit, []byte("1")...)
	overLimit = append(overLimit, bytes.Repeat([]byte("]"), maxTraversalDepth+1)...)
	var dstOverLimit any
	if err := UnmarshalCanonical(overLimit, &dstOverLimit); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("expected ErrMalformedJSON over limit, got %v", err)
	}
}

// TestRegressionContainerEdgeCases verifies embedding depth and container skip traversal.
func TestRegressionContainerEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("skip container deeply nested", func(t *testing.T) {
		t.Parallel()
		type structWithAny struct{ Val any }
		var s structWithAny
		data := []byte(`{"Val":{"nested":{"deep":[1,2,{"x":"y"}]}}}`)
		if err := UnmarshalCanonical(data, &s); err != nil {
			t.Fatalf("expected successful unmarshal of nested dynamic object, got %v", err)
		}
	})

	t.Run("validateStructFields with nil target", func(t *testing.T) {
		t.Parallel()
		if err := validateStructFields([]byte("{}"), nil); err != nil {
			t.Fatalf("expected nil error for nil target, got %v", err)
		}
	})
}

// TestRegressionEmbeddedDeclaredFirstDominance verifies outer field dominates embedded field
// when embedded is declared first (R9).
func TestRegressionEmbeddedDeclaredFirstDominance(t *testing.T) {
	t.Parallel()
	type oldField struct{ OldValue string }
	type newField struct{ NewValue string }
	type embeddedStruct struct{ Child oldField }
	type body struct {
		embeddedStruct
		Child newField
	}

	orig := body{
		embeddedStruct: embeddedStruct{Child: oldField{OldValue: "ignored"}},
		Child:          newField{NewValue: "first_wins"},
	}
	enc, err := MarshalCanonical(orig)
	if err != nil {
		t.Fatalf("MarshalCanonical failed: %v", err)
	}
	var dec body
	if err := UnmarshalCanonical(enc, &dec); err != nil {
		t.Fatalf("UnmarshalCanonical failed: %v", err)
	}
	if dec.Child.NewValue != "first_wins" {
		t.Fatalf("unexpected child value: %s", dec.Child.NewValue)
	}
	var bad body
	if err := UnmarshalCanonical([]byte(`{"Child":{"OldValue":"x"}}`), &bad); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("expected ErrUnknownField for shadowed embedded field, got %v", err)
	}
}

// TestRegressionChildDeclaredFirstDominance verifies outer field dominates embedded field
// when outer child is declared first (R9).
func TestRegressionChildDeclaredFirstDominance(t *testing.T) {
	t.Parallel()
	type innerOld struct{ ExtraInfo string }
	type innerNew struct{ TargetData string }
	type innerEmbedded struct{ Target innerOld }
	type childFirstBody struct {
		Target innerNew
		innerEmbedded
	}

	var dec childFirstBody
	data := []byte(`{"Target":{"TargetData":"child_first"}}`)
	if err := UnmarshalCanonical(data, &dec); err != nil {
		t.Fatalf("UnmarshalCanonical failed: %v", err)
	}
	if dec.Target.TargetData != "child_first" {
		t.Fatalf("unexpected target data: %s", dec.Target.TargetData)
	}
	dec.innerEmbedded.Target.ExtraInfo = "active"
	if _, err := MarshalCanonical(dec); err != nil {
		t.Fatalf("re-encode failed: %v", err)
	}
	var bad childFirstBody
	if err := UnmarshalCanonical([]byte(`{"Target":{"ExtraInfo":"y"}}`), &bad); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("expected ErrUnknownField for shadowed embedded field, got %v", err)
	}
}

// TestRegressionFloatTargetsRejected verifies float targets are rejected on decode,
// protecting against integer precision degradation in typed float representations (R10).
func TestRegressionFloatTargetsRejected(t *testing.T) {
	t.Parallel()

	t.Run("root float64 target", func(t *testing.T) {
		t.Parallel()
		var dst float64
		err := UnmarshalCanonical([]byte("9007199254740993"), &dst)
		if !errors.Is(err, ErrFloatNotAllowed) {
			t.Fatalf("expected ErrFloatNotAllowed for root float64 target, got %v", err)
		}
	})

	t.Run("root float32 target", func(t *testing.T) {
		t.Parallel()
		var dst float32
		err := UnmarshalCanonical([]byte("123"), &dst)
		if !errors.Is(err, ErrFloatNotAllowed) {
			t.Fatalf("expected ErrFloatNotAllowed for root float32 target, got %v", err)
		}
	})

	t.Run("nested struct field float target", func(t *testing.T) {
		t.Parallel()
		var dst struct{ Val float64 }
		err := UnmarshalCanonical([]byte(`{"Val":9007199254740993}`), &dst)
		if !errors.Is(err, ErrFloatNotAllowed) {
			t.Fatalf("expected ErrFloatNotAllowed for struct float64 field, got %v", err)
		}
	})

	t.Run("nested slice float target", func(t *testing.T) {
		t.Parallel()
		var dst []float64
		err := UnmarshalCanonical([]byte("[9007199254740993]"), &dst)
		if !errors.Is(err, ErrFloatNotAllowed) {
			t.Fatalf("expected ErrFloatNotAllowed for slice float64 element, got %v", err)
		}
	})

	t.Run("nested map float target", func(t *testing.T) {
		t.Parallel()
		var dst map[string]float64
		err := UnmarshalCanonical([]byte(`{"k":9007199254740993}`), &dst)
		if !errors.Is(err, ErrFloatNotAllowed) {
			t.Fatalf("expected ErrFloatNotAllowed for map float64 value, got %v", err)
		}
	})
}

// TestRegressionNonStringMapsRejected verifies non-string map keys are rejected on encode and decode.
func TestRegressionNonStringMapsRejected(t *testing.T) {
	t.Parallel()

	t.Run("encode non-string map rejected", func(t *testing.T) {
		t.Parallel()
		_, err := MarshalCanonical(map[int]string{1: "value"})
		if !errors.Is(err, ErrMalformedJSON) {
			t.Fatalf("expected ErrMalformedJSON for map[int]string encode, got %v", err)
		}
	})

	t.Run("decode non-string map rejected", func(t *testing.T) {
		t.Parallel()
		var dst map[int]string
		err := UnmarshalCanonical([]byte(`{"1":"value"}`), &dst)
		if !errors.Is(err, ErrMalformedJSON) {
			t.Fatalf("expected ErrMalformedJSON for map[int]string decode, got %v", err)
		}
	})
}
