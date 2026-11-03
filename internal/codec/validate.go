package codec

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	maxTraversalDepth = 1000
)

func isNil(v any) bool {
	if v == nil {
		return true
	}
	val := reflect.ValueOf(v)
	//exhaustive:ignore
	switch val.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map:
		return val.IsNil()
	default:
		return false
	}
}

func validateUTF8(val any) error {
	if isNil(val) {
		return nil
	}
	return validateUTF8Value(reflect.ValueOf(val), 0)
}

type utf8Pointer struct {
	typ     reflect.Type
	address uintptr
}

type utf8Validation struct {
	pointers map[utf8Pointer]bool
}

func validateUTF8Value(v reflect.Value, depth int) error {
	validation := utf8Validation{pointers: make(map[utf8Pointer]bool)}
	return validation.value(v, depth)
}

func (validation *utf8Validation) value(v reflect.Value, depth int) error {
	if depth > maxTraversalDepth {
		return ErrMalformedJSON
	}
	//exhaustive:ignore
	switch v.Kind() {
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return ErrInvalidUTF8
		}
	case reflect.Pointer:
		return validation.pointer(v, depth)
	case reflect.Interface:
		if !v.IsNil() {
			return validation.value(v.Elem(), depth)
		}
	case reflect.Struct:
		return validation.structValue(v, depth)
	case reflect.Slice, reflect.Array:
		return validation.sliceValue(v, depth)
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return ErrMalformedJSON
		}
		return validation.mapValue(v, depth)
	case reflect.Float32, reflect.Float64:
		return ErrFloatNotAllowed
	}
	return nil
}

func (validation *utf8Validation) pointer(v reflect.Value, depth int) error {
	if v.IsNil() {
		return nil
	}
	key := utf8Pointer{typ: v.Type(), address: v.Pointer()}
	if validation.pointers[key] {
		return ErrMalformedJSON
	}
	validation.pointers[key] = true
	defer delete(validation.pointers, key)
	return validation.value(v.Elem(), depth)
}

func (validation *utf8Validation) structValue(v reflect.Value, depth int) error {
	if v.Type() == reflect.TypeFor[time.Time]() {
		return nil
	}
	if depth >= maxTraversalDepth {
		return ErrMalformedJSON
	}
	for _, field := range selectedStructFields(v.Type()) {
		child, ok := indexedField(v, field.index)
		if ok {
			if err := validation.value(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func indexedField(v reflect.Value, index []int) (reflect.Value, bool) {
	for _, i := range index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v, true
}

func (validation *utf8Validation) sliceValue(v reflect.Value, depth int) error {
	if depth >= maxTraversalDepth {
		return ErrMalformedJSON
	}
	for i := range v.Len() {
		if err := validation.value(v.Index(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (validation *utf8Validation) mapValue(v reflect.Value, depth int) error {
	if depth >= maxTraversalDepth {
		return ErrMalformedJSON
	}
	iter := v.MapRange()
	for iter.Next() {
		if err := validation.value(iter.Key(), depth+1); err != nil {
			return err
		}
		if err := validation.value(iter.Value(), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateStructFields(data []byte, target any) error {
	t := reflect.TypeOf(target)
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	return validateValueAgainstType(dec, t, 0)
}

func validateValueAgainstType(dec *json.Decoder, t reflect.Type, depth int) error {
	if depth > maxTraversalDepth {
		return ErrMalformedJSON
	}

	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64 {
		return ErrFloatNotAllowed
	}

	tok, err := dec.Token()
	if err != nil {
		return ErrMalformedJSON
	}
	if tok == nil {
		return nil
	}

	switch d := tok.(type) {
	case json.Delim:
		switch d {
		case '{':
			return validateObjectAgainstType(dec, t, depth)
		case '[':
			return validateArrayAgainstType(dec, t, depth)
		default:
			return ErrMalformedJSON
		}
	default:
		return nil
	}
}

func validateObjectAgainstType(dec *json.Decoder, t reflect.Type, depth int) error {
	if t.Kind() == reflect.Struct && t != reflect.TypeOf(time.Time{}) {
		return validateStructObject(dec, t, depth)
	}
	if t.Kind() == reflect.Map {
		if t.Key().Kind() != reflect.String {
			return ErrMalformedJSON
		}
		return validateMapObject(dec, t, depth)
	}
	return skipRemainingContainer(dec)
}

func validateStructObject(dec *json.Decoder, t reflect.Type, depth int) error {
	fieldMap := structFieldMap(t)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return ErrMalformedJSON
		}
		key, ok := keyTok.(string)
		if !ok {
			return ErrMalformedJSON
		}
		subType, exists := fieldMap[key]
		if !exists {
			return ErrUnknownField
		}
		if err := validateValueAgainstType(dec, subType, depth+1); err != nil {
			return err
		}
	}
	closingTok, err := dec.Token()
	if err != nil || closingTok != json.Delim('}') {
		return ErrMalformedJSON
	}
	return nil
}

func validateMapObject(dec *json.Decoder, t reflect.Type, depth int) error {
	valType := t.Elem()
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return ErrMalformedJSON
		}
		if _, ok := keyTok.(string); !ok {
			return ErrMalformedJSON
		}
		if err := validateValueAgainstType(dec, valType, depth+1); err != nil {
			return err
		}
	}
	closingTok, err := dec.Token()
	if err != nil || closingTok != json.Delim('}') {
		return ErrMalformedJSON
	}
	return nil
}

func validateArrayAgainstType(dec *json.Decoder, t reflect.Type, depth int) error {
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		elemType := t.Elem()
		for dec.More() {
			if err := validateValueAgainstType(dec, elemType, depth+1); err != nil {
				return err
			}
		}
		closingTok, err := dec.Token()
		if err != nil || closingTok != json.Delim(']') {
			return ErrMalformedJSON
		}
		return nil
	}
	return skipRemainingContainer(dec)
}

func skipRemainingContainer(dec *json.Decoder) error {
	containerDepth := 1
	for containerDepth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return ErrMalformedJSON
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				containerDepth++
			case '}', ']':
				containerDepth--
			}
		}
	}
	return nil
}

func normalizeDecodedTarget(v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil
	}
	elem := rv.Elem()
	if (elem.Kind() == reflect.Interface || elem.Kind() == reflect.Map || elem.Kind() == reflect.Slice) && !elem.IsNil() {
		norm, err := normalizeDecodedIntegers(elem.Interface())
		if err != nil {
			return err
		}
		elem.Set(reflect.ValueOf(norm))
	}
	return nil
}

func parseCanonicalInteger(num json.Number) (int64, error) {
	n, err := strconv.ParseInt(string(num), 10, 64)
	if err != nil {
		return 0, ErrIntegerRange
	}
	return n, nil
}

func normalizeDecodedIntegers(val any) (any, error) {
	switch v := val.(type) {
	case json.Number:
		return parseCanonicalInteger(v)
	case map[string]any:
		return normalizeMap(v)
	case []any:
		return normalizeSlice(v)
	default:
		return val, nil
	}
}

func normalizeMap(m map[string]any) (any, error) {
	for k, child := range m {
		norm, err := normalizeDecodedIntegers(child)
		if err != nil {
			return nil, err
		}
		m[k] = norm
	}
	return m, nil
}

func normalizeSlice(s []any) (any, error) {
	for i, child := range s {
		norm, err := normalizeDecodedIntegers(child)
		if err != nil {
			return nil, err
		}
		s[i] = norm
	}
	return s, nil
}
