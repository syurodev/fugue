package codec

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type sampleStruct struct {
	Name    string
	Count   int
	Enabled bool
	Tags    []string
	Meta    map[string]string
}

type duplicateTestCase struct {
	name    string
	json    string
	dupKey  string
	wantErr bool
}

func duplicateKeyCases() []duplicateTestCase {
	return []duplicateTestCase{
		{
			name:    "no duplicates",
			json:    `{"a": 1, "b": 2}`,
			wantErr: false,
		},
		{
			name:    "top-level duplicate key",
			json:    `{"a": 1, "a": 2}`,
			dupKey:  "a",
			wantErr: true,
		},
		{
			name:    "nested object duplicate key",
			json:    `{"outer": {"nested": 1, "nested": 2}}`,
			dupKey:  "nested",
			wantErr: true,
		},
		{
			name:    "deeply nested inside array",
			json:    `{"items": [{"id": 1}, {"sub": {"key": 1, "key": 2}}]}`,
			dupKey:  "key",
			wantErr: true,
		},
		{
			name:    "duplicate key in array element",
			json:    `[{"dup": 1, "dup": 2}]`,
			dupKey:  "dup",
			wantErr: true,
		},
		{
			name:    "different keys with same value",
			json:    `{"a": 1, "b": 1}`,
			wantErr: false,
		},
	}
}

func checkDuplicateKeyError(t *testing.T, err error, dupKey string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error for duplicate key %q, got nil", dupKey)
	}
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("expected ErrDuplicateKey, got %v", err)
	}
}

func TestCodecDuplicateKeys(t *testing.T) {
	t.Parallel()

	for _, tc := range duplicateKeyCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCanonicalJSON([]byte(tc.json))
			if tc.wantErr {
				checkDuplicateKeyError(t, err, tc.dupKey)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCodecDisallowUnknownFields(t *testing.T) {
	t.Parallel()

	type target struct {
		Alpha string
		Beta  int
	}

	cases := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{
			name:    "exact fields",
			json:    `{"Alpha":"ok","Beta":10}`,
			wantErr: false,
		},
		{
			name:    "unknown field present",
			json:    `{"Alpha":"ok","Beta":10,"Extra":true}`,
			wantErr: true,
		},
		{
			name:    "unknown field only",
			json:    `{"Gamma":"unexpected"}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var dst target
			err := UnmarshalCanonical([]byte(tc.json), &dst)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for unknown field, got nil")
				}
				if !errors.Is(err, ErrUnknownField) {
					t.Fatalf("expected ErrUnknownField, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

type numberTestCase struct {
	name    string
	json    string
	wantErr error
}

func numberValidationCases() []numberTestCase {
	return []numberTestCase{
		{name: "valid integer zero", json: `{"val": 0}`, wantErr: nil},
		{name: "valid negative integer", json: `{"val": -42}`, wantErr: nil},
		{name: "valid max int64", json: `{"val": 9223372036854775807}`, wantErr: nil},
		{name: "valid min int64", json: `{"val": -9223372036854775808}`, wantErr: nil},
		{name: "overflow int64", json: `{"val": 9223372036854775808}`, wantErr: ErrIntegerRange},
		{name: "underflow int64", json: `{"val": -9223372036854775809}`, wantErr: ErrIntegerRange},
		{name: "float rejected", json: `{"val": 3.14}`, wantErr: ErrFloatNotAllowed},
		{name: "float zero rejected", json: `{"val": 0.0}`, wantErr: ErrFloatNotAllowed},
		{name: "scientific exponent lower rejected", json: `{"val": 1e5}`, wantErr: ErrFloatNotAllowed},
		{name: "scientific exponent upper rejected", json: `{"val": 2E-3}`, wantErr: ErrFloatNotAllowed},
		{name: "root scalar number valid", json: `123`, wantErr: nil},
		{name: "root scalar float rejected", json: `123.45`, wantErr: ErrFloatNotAllowed},
	}
}

func TestCodecNumberValidation(t *testing.T) {
	t.Parallel()

	for _, tc := range numberValidationCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCanonicalJSON([]byte(tc.json))
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

type malformedTestCase struct {
	name    string
	json    []byte
	wantErr error
}

func malformedCases() []malformedTestCase {
	return []malformedTestCase{
		{name: "empty bytes", json: []byte(""), wantErr: ErrMalformedJSON},
		{name: "whitespace only", json: []byte("   \n\t  "), wantErr: ErrMalformedJSON},
		{name: "truncated object", json: []byte(`{"key": `), wantErr: ErrMalformedJSON},
		{name: "truncated string", json: []byte(`{"key": "incomplete`), wantErr: ErrMalformedJSON},
		{name: "unclosed array", json: []byte(`[1, 2, 3`), wantErr: ErrMalformedJSON},
		{name: "unexpected closing delimiter", json: []byte(`}`), wantErr: ErrMalformedJSON},
		{name: "invalid utf8 byte sequence", json: []byte{0xff, 0xfe, 0xfd}, wantErr: ErrInvalidUTF8},
		{name: "trailing token after root object", json: []byte(`{"a": 1} 123`), wantErr: ErrTrailingData},
		{name: "trailing non-whitespace characters", json: []byte(`{"a": 1} garbage`), wantErr: ErrTrailingData},
		{name: "trailing array after root scalar", json: []byte(`"hello" []`), wantErr: ErrTrailingData},
	}
}

func TestCodecMalformedAndTruncated(t *testing.T) {
	t.Parallel()

	for _, tc := range malformedCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCanonicalJSON(tc.json)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

const (
	keyZebra  = "zebra"
	keyApple  = "apple"
	keyBanana = "banana"
	keyOrange = "orange"
	keyMango  = "mango"
)

func TestCodecDeterministicEncoding(t *testing.T) {
	t.Parallel()

	inputMap := map[string]string{
		keyZebra:  "last",
		keyApple:  "first",
		keyBanana: "middle",
		keyOrange: "citrus",
		keyMango:  "sweet",
	}

	firstBytes, err := MarshalCanonical(inputMap)
	if err != nil {
		t.Fatalf("MarshalCanonical failed: %v", err)
	}

	wantJSON := `{"apple":"first","banana":"middle","mango":"sweet","orange":"citrus","zebra":"last"}`
	if string(firstBytes) != wantJSON {
		t.Fatalf("expected %s, got %s", wantJSON, string(firstBytes))
	}

	permutations := [][]string{
		{keyZebra, keyApple, keyBanana, keyOrange, keyMango},
		{keyApple, keyOrange, keyZebra, keyBanana, keyMango},
		{keyBanana, keyMango, keyApple, keyZebra, keyOrange},
		{keyOrange, keyBanana, keyMango, keyApple, keyZebra},
		{keyMango, keyZebra, keyOrange, keyBanana, keyApple},
	}

	for i, order := range permutations {
		m := make(map[string]string, len(order))
		for _, k := range order {
			m[k] = inputMap[k]
		}
		bytes, err := MarshalCanonical(m)
		if err != nil {
			t.Fatalf("permutation %d: MarshalCanonical failed: %v", i, err)
		}
		if diff := cmp.Diff(firstBytes, bytes); diff != "" {
			t.Fatalf("permutation %d: non-deterministic map encoding: diff:\n%s", i, diff)
		}
	}
}

func TestCodecRoundTrip(t *testing.T) {
	t.Parallel()

	orig := sampleStruct{
		Name:    "run-trace-a",
		Count:   100,
		Enabled: true,
		Tags:    []string{"alpha", "beta", "gamma"},
		Meta: map[string]string{
			"env":  "production",
			"tier": "kernel",
		},
	}

	encoded, err := MarshalCanonical(orig)
	if err != nil {
		t.Fatalf("MarshalCanonical failed: %v", err)
	}

	var decoded sampleStruct
	if err := UnmarshalCanonical(encoded, &decoded); err != nil {
		t.Fatalf("UnmarshalCanonical failed: %v", err)
	}

	if diff := cmp.Diff(orig, decoded); diff != "" {
		t.Fatalf("round-trip mismatch (-want +got):\n%s", diff)
	}
}

func TestCodecNilTargets(t *testing.T) {
	t.Parallel()

	if _, err := MarshalCanonical(nil); !errors.Is(err, ErrNilTarget) {
		t.Fatalf("MarshalCanonical(nil) = %v, want ErrNilTarget", err)
	}
	if err := UnmarshalCanonical([]byte(`{"a": 1}`), nil); !errors.Is(err, ErrNilTarget) {
		t.Fatalf("UnmarshalCanonical(data, nil) = %v, want ErrNilTarget", err)
	}
	if _, err := EncodeCommand(nil); !errors.Is(err, ErrNilTarget) {
		t.Fatalf("EncodeCommand(nil) = %v, want ErrNilTarget", err)
	}
}

func TestCodecMarshalUnsupportedType(t *testing.T) {
	t.Parallel()

	unsupported := make(chan int)
	_, err := MarshalCanonical(unsupported)
	if err == nil {
		t.Fatalf("expected error marshaling channel, got nil")
	}
	if !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("expected ErrMalformedJSON, got %v", err)
	}
}

func TestCodecNestedArraysAndContainers(t *testing.T) {
	t.Parallel()

	validInputs := []string{
		`[]`,
		`[[1, 2], [3, 4]]`,
		`[{"k": "v"}, {"k2": "v2"}]`,
		`{"arr": [[true, false], [null]]}`,
	}
	for _, in := range validInputs {
		if err := ValidateCanonicalJSON([]byte(in)); err != nil {
			t.Fatalf("ValidateCanonicalJSON(%s) unexpected error: %v", in, err)
		}
	}

	invalidInputs := []string{
		`[}]`,
		`{"a": ]}`,
	}
	for _, in := range invalidInputs {
		if err := ValidateCanonicalJSON([]byte(in)); err == nil {
			t.Fatalf("ValidateCanonicalJSON(%s) expected error, got nil", in)
		}
	}
}

type taggedInner struct {
	InnerVal string
}

type taggedStruct struct {
	CustomField string `json:"custom_field,omitempty"`
	Ignored     string `json:"-"`
	Inner       taggedInner
}

func TestCodecStructTagsAndNesting(t *testing.T) {
	t.Parallel()

	data := []byte(`{"custom_field":"val","Inner":{"InnerVal":"ok"}}`)
	var s taggedStruct
	if err := UnmarshalCanonical(data, &s); err != nil {
		t.Fatalf("UnmarshalCanonical failed for tagged struct: %v", err)
	}
	if s.CustomField != "val" || s.Inner.InnerVal != "ok" {
		t.Fatalf("unexpected unmarshaled values: %+v", s)
	}

	unknownData := []byte(`{"Ignored":"bad"}`)
	if err := UnmarshalCanonical(unknownData, &s); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("expected ErrUnknownField for ignored tag, got %v", err)
	}
}

func TestCodecNonPointerAndTypeMismatch(t *testing.T) {
	t.Parallel()

	var s taggedStruct
	if err := UnmarshalCanonical([]byte(`{}`), s); !errors.Is(err, ErrNilTarget) {
		t.Fatalf("expected ErrNilTarget for non-pointer, got %v", err)
	}

	var n int
	if err := UnmarshalCanonical([]byte(`{}`), &n); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("expected ErrMalformedJSON for struct-to-int mismatch, got %v", err)
	}
}

func TestCodecInvalidUTF8Containers(t *testing.T) {
	t.Parallel()

	invalidSlice := []string{string([]byte{0xff})}
	if _, err := MarshalCanonical(invalidSlice); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("expected ErrInvalidUTF8 for invalid slice, got %v", err)
	}

	invalidMapKey := map[string]string{string([]byte{0xff}): "ok"}
	if _, err := MarshalCanonical(invalidMapKey); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("expected ErrInvalidUTF8 for invalid map key, got %v", err)
	}
}
