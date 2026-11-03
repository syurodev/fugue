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

const (
	maxTraversalDepth = 1000
	maxEmbeddingDepth = 32
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
	if depth > maxTraversalDepth {
		return ErrMalformedJSON
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
		if v.Type().Key().Kind() != reflect.String {
			return ErrMalformedJSON
		}
		return validateUTF8Map(v, depth)
	case reflect.Float32, reflect.Float64:
		return ErrFloatNotAllowed
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

type fieldEntry struct {
	typ       reflect.Type
	depth     int
	tagged    bool
	ambiguous bool
}

type queueItem struct {
	typ   reflect.Type
	depth int
}

func structFieldMap(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]fieldEntry)
	queue := []queueItem{{typ: t, depth: 0}}
	visited := make(map[reflect.Type]bool)

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if item.depth > maxEmbeddingDepth || visited[item.typ] {
			continue
		}
		visited[item.typ] = true

		queue = collectTypeFields(item.typ, item.depth, fields, queue)
	}

	result := make(map[string]reflect.Type, len(fields))
	for name, entry := range fields {
		if !entry.ambiguous {
			result[name] = entry.typ
		}
	}
	return result
}

func collectTypeFields(t reflect.Type, depth int, fields map[string]fieldEntry, queue []queueItem) []queueItem {
	for i := range t.NumField() {
		f := t.Field(i)
		queue = handleStructField(&f, depth, fields, queue)
	}
	return queue
}

func handleStructField(f *reflect.StructField, depth int, fields map[string]fieldEntry, queue []queueItem) []queueItem {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return queue
	}
	name := parseJSONTagName(tag)
	tagged := name != ""
	if f.Anonymous && name == "" {
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			return append(queue, queueItem{typ: ft, depth: depth + 1})
		}
	}

	if !f.IsExported() {
		return queue
	}
	if name == "" {
		name = f.Name
	}

	recordField(name, f.Type, depth, tagged, fields)
	return queue
}

func recordField(name string, typ reflect.Type, depth int, tagged bool, fields map[string]fieldEntry) {
	existing, exists := fields[name]
	// At the shallowest depth, encoding/json considers named tags before
	// untagged fields. A tagged candidate can resolve untagged ambiguity.
	if !exists || depth < existing.depth || (depth == existing.depth && tagged && !existing.tagged) {
		fields[name] = fieldEntry{typ: typ, depth: depth, tagged: tagged, ambiguous: false}
		return
	}
	if existing.depth == depth && existing.tagged == tagged {
		existing.ambiguous = true
		fields[name] = existing
	}
}

func parseJSONTagName(tag string) string {
	if comma := strings.Index(tag, ","); comma != -1 {
		return tag[:comma]
	}
	return tag
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
