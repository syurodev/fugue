package markdown

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Minimal RFC 3492 punycode, enough to keep an IDN host in both its
// punycode and display forms. The obvious library, golang.org/x/net/idna,
// would add a direct module dependency for these two directions, so the
// algorithm lives here; it is the only place host labels are converted.
// A label that fails to convert is kept exactly as written, so a hostile
// host cannot erase itself.

var errPunycode = errors.New("markdown: invalid punycode label")

const (
	punyBase     = 36
	punyTMin     = 1
	punyTMax     = 26
	punySkew     = 38
	punyDamp     = 700
	punyInitialB = 72
	punyInitialN = 128
	punyPrefix   = "xn--"
)

// punyDigits is the §5 digit alphabet, indexed 0 to 35.
const punyDigits = "abcdefghijklmnopqrstuvwxyz0123456789"

// hostDisplay converts a hostname holding xn-- labels to its unicode form.
func hostDisplay(host string) string {
	if !strings.Contains(host, punyPrefix) {
		return host
	}
	labels := strings.Split(host, ".")
	for i, label := range labels {
		if strings.HasPrefix(label, punyPrefix) {
			decoded, err := punyDecode(strings.TrimPrefix(label, punyPrefix))
			if err != nil {
				return host
			}
			labels[i] = decoded
		}
	}
	return strings.Join(labels, ".")
}

// hostASCII converts a hostname with non-ASCII labels to punycode.
func hostASCII(host string) string {
	if isASCII(host) {
		return host
	}
	labels := strings.Split(host, ".")
	for i, label := range labels {
		if !isASCII(label) {
			encoded, err := punyEncode(label)
			if err != nil {
				return host
			}
			labels[i] = encoded
		}
	}
	return strings.Join(labels, ".")
}

// isASCII reports whether s holds only ASCII bytes.
func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// punyThreshold is the digit width at code point k (§6.1).
func punyThreshold(k, bias int) int {
	switch {
	case k <= bias+punyTMin:
		return punyTMin
	case k >= bias+punyTMax:
		return punyTMax
	default:
		return k - bias
	}
}

// punyAdapt recalculates the bias (§6.1).
func punyAdapt(delta, numpoints int, firsttime bool) int {
	if firsttime {
		delta /= punyDamp
	} else {
		delta /= 2
	}
	delta += delta / numpoints
	k := 0
	for delta > ((punyBase-punyTMin)*punyTMax)/2 {
		delta /= punyBase - punyTMin
		k += punyBase
	}
	return k + (punyBase-punyTMin+1)*delta/(delta+punySkew)
}

// punyDigitValue decodes one basic code point.
func punyDigitValue(r rune) (int, error) {
	i := strings.IndexRune(punyDigits, r)
	if (r >= 'A' && r <= 'Z') || i < 0 {
		return 0, errPunycode
	}
	return i, nil
}

// punyEncode converts one non-ASCII label to its xn-- form (§6.3).
func punyEncode(label string) (string, error) {
	runes := []rune(label)
	out, h := punyBasicRunes(runes)
	basic := h
	if basic > 0 {
		out = append(out, '-')
	}
	n, delta, bias := punyInitialN, 0, punyInitialB
	for h < len(runes) {
		m := punyMinRuneAbove(runes, n)
		delta += (m - n) * (h + 1)
		if delta < 0 {
			return "", errPunycode
		}
		n = m
		var err error
		delta, h, bias, out, err = punyEncodeRun(runes, n, delta, h, basic, bias, out)
		if err != nil {
			return "", err
		}
		delta++
		n++
	}
	return punyPrefix + string(out), nil
}

// punyBasicRunes collects the ASCII prefix runes.
func punyBasicRunes(runes []rune) ([]rune, int) {
	var out []rune
	for _, r := range runes {
		if r < punyInitialN {
			out = append(out, r)
		}
	}
	return out, len(out)
}

// punyMinRuneAbove returns the smallest rune at or above n.
func punyMinRuneAbove(runes []rune, n int) int {
	m := 0x10FFFF + 1
	for _, r := range runes {
		if int(r) >= n && int(r) < m {
			m = int(r)
		}
	}
	return m
}

// punyEncodeRun walks one round: every rune below n bumps the delta, and
// each rune equal to n emits its delta digits and readapts the bias.
func punyEncodeRun(runes []rune, n, delta, h, basic, bias int, out []rune) (int, int, int, []rune, error) {
	for _, r := range runes {
		if int(r) < n {
			delta++
			if delta < 0 {
				return 0, 0, 0, nil, errPunycode
			}
			continue
		}
		if int(r) != n {
			continue
		}
		out = appendDeltaDigits(out, delta, bias)
		bias = punyAdapt(delta, h+1, h == basic)
		delta, h = 0, h+1
	}
	return delta, h, bias, out, nil
}

// appendDeltaDigits generalizes delta as basic code points (§6.3 step 6).
func appendDeltaDigits(out []rune, delta, bias int) []rune {
	q := delta
	for k := punyBase; ; k += punyBase {
		t := punyThreshold(k, bias)
		if q < t {
			break
		}
		out = append(out, rune(punyDigits[t+(q-t)%(punyBase-t)]))
		q = (q - t) / (punyBase - t)
	}
	return append(out, rune(punyDigits[q]))
}

// punyDecode converts the part after xn-- back to unicode (§6.2).
func punyDecode(encoded string) (string, error) {
	digits, basic := splitPunycode(encoded)
	out := basic
	n, i, bias := punyInitialN, 0, punyInitialB
	for next := 0; next < len(digits); {
		var err error
		i, bias, next, err = punyDecodeDelta(digits, next, bias, i, len(out)+1)
		if err != nil {
			return "", err
		}
		size := len(out) + 1
		if i/size > 0x10FFFF-n {
			return "", errPunycode
		}
		n += i / size
		i %= size
		if n < 0 || n > 0x10FFFF {
			return "", errPunycode
		}
		out = insertRune(out, i, rune(n))
		i++
	}
	return string(out), nil
}

// punyDecodeDelta decodes one generalized time delta and readapts the
// bias; size is the current output length plus one.
func punyDecodeDelta(digits []rune, next, bias, i, size int) (int, int, int, error) {
	oldi := i
	w := 1
	for k := punyBase; ; k += punyBase {
		if next == len(digits) {
			return 0, 0, 0, errPunycode
		}
		d, err := punyDigitValue(digits[next])
		if err != nil {
			return 0, 0, 0, err
		}
		next++
		i += d * w
		if i < 0 || i > 0x10FFFF {
			return 0, 0, 0, errPunycode
		}
		t := punyThreshold(k, bias)
		if d < t {
			break
		}
		w *= punyBase - t
	}
	return i, punyAdapt(i-oldi, size, oldi == 0), next, nil
}

// insertRune inserts r at position i of out.
func insertRune(out []rune, i int, r rune) []rune {
	out = append(out, 0)
	copy(out[i+1:], out[i:])
	out[i] = r
	return out
}

// splitPunycode splits a label body into its extended digits and its
// copied-through basic code points: the basic part is everything before
// the last '-' (§6.2).
func splitPunycode(encoded string) (digits, basic []rune) {
	pos := strings.LastIndexByte(encoded, '-')
	if pos < 0 {
		return []rune(encoded), nil
	}
	for _, r := range encoded[:pos] {
		if r >= punyInitialN {
			return []rune(encoded), nil
		}
		basic = append(basic, r)
	}
	return []rune(encoded[pos+1:]), basic
}
