package codec

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

func validateUTF8Value(v reflect.Value, depth int) error {
	if depth > 64 {
		return nil
	}
	//exhaustive:ignore
	switch v.Kind() {
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return ErrInvalidUTF8
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return validateUTF8Value(v.Elem(), depth+1)
		}
	case reflect.Struct:
		return validateUTF8Struct(v, depth)
	case reflect.Slice, reflect.Array:
		return validateUTF8Slice(v, depth)
	case reflect.Map:
		return validateUTF8Map(v, depth)
	}
	return nil
}

func validateUTF8Struct(v reflect.Value, depth int) error {
	for i := range v.NumField() {
		if err := validateUTF8Value(v.Field(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateUTF8Slice(v reflect.Value, depth int) error {
	for i := range v.Len() {
		if err := validateUTF8Value(v.Index(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateUTF8Map(v reflect.Value, depth int) error {
	iter := v.MapRange()
	for iter.Next() {
		if err := validateUTF8Value(iter.Key(), depth+1); err != nil {
			return err
		}
		if err := validateUTF8Value(iter.Value(), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateStructFields(data []byte, target any) error {
	t := reflect.TypeOf(target)
	if t.Kind() != reflect.Pointer {
		return nil
	}
	t = t.Elem()
	if t.Kind() != reflect.Struct {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	return validateStructContainer(dec, t)
}

func validateStructContainer(dec *json.Decoder, t reflect.Type) error {
	tok, err := dec.Token()
	if err != nil {
		return ErrMalformedJSON
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return ErrMalformedJSON
	}

	fieldMap := structFieldMap(t)

	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return ErrMalformedJSON
		}
		key, isStr := keyTok.(string)
		if !isStr {
			return ErrMalformedJSON
		}
		subType, exists := fieldMap[key]
		if !exists {
			return ErrUnknownField
		}
		if subErr := skipOrRecurseValue(dec, subType); subErr != nil {
			return subErr
		}
	}

	tok, err = dec.Token()
	if err != nil || tok != json.Delim('}') {
		return ErrMalformedJSON
	}
	return nil
}

func structFieldMap(t reflect.Type) map[string]reflect.Type {
	fieldMap := make(map[string]reflect.Type, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := f.Name
		if comma := strings.Index(tag, ","); comma != -1 {
			tag = tag[:comma]
		}
		if tag != "" {
			name = tag
		}
		fieldMap[name] = f.Type
	}
	return fieldMap
}

func skipOrRecurseValue(dec *json.Decoder, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct && t != reflect.TypeOf(time.Time{}) {
		return validateStructContainer(dec, t)
	}
	var dummy any
	if err := dec.Decode(&dummy); err != nil {
		return ErrMalformedJSON
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

