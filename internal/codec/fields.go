package codec

import (
	"reflect"
	"strings"
)

type fieldEntry struct {
	typ       reflect.Type
	depth     int
	tagged    bool
	ambiguous bool
	index     []int
}

type queueItem struct {
	typ   reflect.Type
	depth int
	index []int
}

func selectedStructFields(t reflect.Type) map[string]fieldEntry {
	fields := make(map[string]fieldEntry)
	queue := []queueItem{{typ: t, depth: 0}}
	visited := make(map[reflect.Type]bool)

	for len(queue) > 0 {
		queue = collectFieldLevel(queue, fields, visited)
	}

	result := make(map[string]fieldEntry, len(fields))
	for name, entry := range fields {
		if !entry.ambiguous {
			result[name] = entry
		}
	}
	return result
}

// Multiple paths to an embedded type must preserve ambiguity even though
// visited types are traversed only once to terminate recursive embeddings.
func collectFieldLevel(queue []queueItem, fields map[string]fieldEntry, visited map[reflect.Type]bool) []queueItem {
	counts := make(map[reflect.Type]int)
	for _, item := range queue {
		counts[item.typ]++
	}
	var next []queueItem
	for _, item := range queue {
		if visited[item.typ] {
			continue
		}
		visited[item.typ] = true
		next = collectTypeFields(item, fields, next)
		if counts[item.typ] > 1 {
			next = collectTypeFields(item, fields, next)
		}
	}
	return next
}

func structFieldMap(t reflect.Type) map[string]reflect.Type {
	fields := selectedStructFields(t)
	result := make(map[string]reflect.Type, len(fields))
	for name, entry := range fields {
		result[name] = entry.typ
	}
	return result
}

func collectTypeFields(item queueItem, fields map[string]fieldEntry, queue []queueItem) []queueItem {
	for i := range item.typ.NumField() {
		f := item.typ.Field(i)
		index := append(append([]int(nil), item.index...), i)
		queue = handleStructField(&f, item.depth, index, fields, queue)
	}
	return queue
}

func handleStructField(f *reflect.StructField, depth int, index []int, fields map[string]fieldEntry, queue []queueItem) []queueItem {
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
			return append(queue, queueItem{typ: ft, depth: depth + 1, index: index})
		}
	}

	if !f.IsExported() {
		return queue
	}
	if name == "" {
		name = f.Name
	}

	recordField(name, f.Type, depth, tagged, index, fields)
	return queue
}

func recordField(name string, typ reflect.Type, depth int, tagged bool, index []int, fields map[string]fieldEntry) {
	existing, exists := fields[name]
	// At the shallowest depth, encoding/json considers named tags before
	// untagged fields. A tagged candidate can resolve untagged ambiguity.
	if !exists || depth < existing.depth || (depth == existing.depth && tagged && !existing.tagged) {
		fields[name] = fieldEntry{typ: typ, depth: depth, tagged: tagged, ambiguous: false, index: index}
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
