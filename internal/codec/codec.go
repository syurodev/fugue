package codec

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

type containerKind int

const (
	containerObject containerKind = iota
	containerArray
)

type containerState struct {
	kind         containerKind
	expectingKey bool
	seenKeys     map[string]struct{}
}

// MarshalCanonical serializes v into canonical JSON bytes. Struct fields are
// serialized in declaration order, map keys are sorted, and no extra whitespace
// is emitted. The resulting bytes are validated against canonical rules.
func MarshalCanonical(v any) ([]byte, error) {
	if isNil(v) {
		return nil, ErrNilTarget
	}
	if err := validateUTF8(v); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, ErrMalformedJSON
	}
	if err := ValidateCanonicalJSON(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// UnmarshalCanonical deserializes canonical JSON bytes into target pointer v.
// It enforces UTF-8 validity, absence of duplicate keys at any depth, absence
// of unknown fields (with exact case), absence of floating-point numbers, and integer range.
func UnmarshalCanonical(data []byte, v any) error {
	if isNil(v) {
		return ErrNilTarget
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer {
		return ErrNilTarget
	}
	if err := ValidateCanonicalJSON(data); err != nil {
		return err
	}
	if err := validateStructFields(data, v); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return ErrUnknownField
		}
		return ErrMalformedJSON
	}
	return normalizeDecodedTarget(v)
}

// ValidateCanonicalJSON verifies that data conforms to canonical codec v1 rules:
// valid UTF-8, no duplicate keys in objects, no floats, integers within int64,
// well-formed structure, and no trailing data. Diagnostics never expose input keys or values.
func ValidateCanonicalJSON(data []byte) error {
	if !utf8.Valid(data) {
		return ErrInvalidUTF8
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrMalformedJSON
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	stack := make([]containerState, 0, 8)
	rootSeen := false

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if rootSeen {
			return ErrTrailingData
		}
		if err != nil {
			return ErrMalformedJSON
		}
		stack, rootSeen, err = advanceState(tok, stack, rootSeen)
		if err != nil {
			return err
		}
	}
	if len(stack) > 0 || !rootSeen {
		return ErrMalformedJSON
	}
	return checkTrailingBuffer(dec)
}

func advanceState(tok json.Token, stack []containerState, rootSeen bool) ([]containerState, bool, error) {
	if len(stack) == 0 {
		return handleRootToken(tok, stack)
	}
	newStack, err := handleContainerToken(tok, stack)
	if err != nil {
		return nil, false, err
	}
	if len(newStack) == 0 {
		rootSeen = true
	}
	return newStack, rootSeen, nil
}

func handleRootToken(tok json.Token, stack []containerState) ([]containerState, bool, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return append(stack, newObjectState()), false, nil
		case '[':
			return append(stack, containerState{kind: containerArray, expectingKey: false, seenKeys: nil}), false, nil
		default:
			return nil, false, ErrMalformedJSON
		}
	case json.Number:
		if err := validateNumber(t); err != nil {
			return nil, false, err
		}
		return stack, true, nil
	default:
		return stack, true, nil
	}
}

func handleContainerToken(tok json.Token, stack []containerState) ([]containerState, error) {
	topIdx := len(stack) - 1
	top := stack[topIdx]

	if top.kind == containerObject {
		return handleObjectToken(tok, stack)
	}
	return handleArrayToken(tok, stack)
}

func handleObjectToken(tok json.Token, stack []containerState) ([]containerState, error) {
	topIdx := len(stack) - 1
	if stack[topIdx].expectingKey {
		if delim, ok := tok.(json.Delim); ok && delim == '}' {
			return closeContainer(stack)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, ErrMalformedJSON
		}
		if _, exists := stack[topIdx].seenKeys[key]; exists {
			return nil, ErrDuplicateKey
		}
		stack[topIdx].seenKeys[key] = struct{}{}
		stack[topIdx].expectingKey = false
		return stack, nil
	}
	return handleObjectValue(tok, stack)
}

func handleObjectValue(tok json.Token, stack []containerState) ([]containerState, error) {
	topIdx := len(stack) - 1
	stack[topIdx].expectingKey = true
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return append(stack, newObjectState()), nil
		case '[':
			return append(stack, containerState{kind: containerArray, expectingKey: false, seenKeys: nil}), nil
		default:
			return nil, ErrMalformedJSON
		}
	case json.Number:
		if err := validateNumber(t); err != nil {
			return nil, err
		}
		return stack, nil
	default:
		return stack, nil
	}
}

func handleArrayToken(tok json.Token, stack []containerState) ([]containerState, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case ']':
			return closeContainer(stack)
		case '{':
			return append(stack, newObjectState()), nil
		case '[':
			return append(stack, containerState{kind: containerArray, expectingKey: false, seenKeys: nil}), nil
		default:
			return nil, ErrMalformedJSON
		}
	case json.Number:
		if err := validateNumber(t); err != nil {
			return nil, err
		}
		return stack, nil
	default:
		return stack, nil
	}
}

func closeContainer(stack []containerState) ([]containerState, error) {
	stack = stack[:len(stack)-1]
	if len(stack) > 0 && stack[len(stack)-1].kind == containerObject {
		stack[len(stack)-1].expectingKey = true
	}
	return stack, nil
}

func newObjectState() containerState {
	return containerState{
		kind:         containerObject,
		expectingKey: true,
		seenKeys:     make(map[string]struct{}),
	}
}

func validateNumber(num json.Number) error {
	s := string(num)
	if strings.ContainsAny(s, ".eE") {
		return ErrFloatNotAllowed
	}
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		return ErrIntegerRange
	}
	return nil
}

func checkTrailingBuffer(dec *json.Decoder) error {
	buf, err := io.ReadAll(dec.Buffered())
	if err != nil {
		return ErrMalformedJSON
	}
	if len(bytes.TrimSpace(buf)) > 0 {
		return ErrTrailingData
	}
	return nil
}
