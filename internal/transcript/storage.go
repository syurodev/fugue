package transcript

import "unicode/utf8"

// FitStorage preserves UTF-8 head and tail within a payload's byte limit.
func FitStorage(content []byte, limit int) ([]byte, bool, int) {
	size := len(content)
	if limit >= size {
		return append([]byte(nil), content...), false, size
	}
	if limit <= 0 {
		return []byte{}, true, size
	}
	head := limit / 2
	for head > 0 && !utf8.RuneStart(content[head]) {
		head--
	}
	tail := size - (limit - head)
	for tail < size && !utf8.RuneStart(content[tail]) {
		tail++
	}
	out := make([]byte, 0, head+size-tail)
	out = append(out, content[:head]...)
	out = append(out, content[tail:]...)
	return out, true, size
}
