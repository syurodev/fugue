package markdown

import (
	"strings"
	"unicode/utf8"
)

// Link and image destinations are sanitized here before they enter the
// model: the unsafe runes go, the scheme decides openability, and an IDN
// host is kept in both forms so the F10.3b popover can show the punycode
// and the display host side by side. The parsing is local string work on
// purpose: the app production code opens no sockets and keeps clear of
// the net package tree altogether (DA-E06), and net/url would pull it in.

// openableScheme reports whether a destination scheme may be opened by the
// caller. Every other scheme — javascript:, file:, data:, vbscript:,
// unknown — and every relative destination renders as plain text.
func openableScheme(scheme string) bool {
	switch scheme {
	case "http", "https", "mailto":
		return true
	default:
		return false
	}
}

// splitScheme splits a destination into its lowercased scheme and the rest;
// hasScheme is false for relative destinations, whose text stays plain.
func splitScheme(raw string) (scheme, rest string, hasScheme bool) {
	i := strings.IndexByte(raw, ':')
	if i <= 0 {
		return "", raw, false
	}
	candidate := raw[:i]
	if !validScheme(candidate) {
		return "", raw, false
	}
	return strings.ToLower(candidate), raw[i+1:], true
}

// validScheme reports whether s matches the RFC 3986 scheme grammar: a
// letter first, then letters, digits, plus, hyphen or dot.
func validScheme(s string) bool {
	for j := range len(s) {
		c := s[j]
		alpha := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if alpha {
			continue
		}
		digitOrExtra := c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'
		if j > 0 && digitOrExtra {
			continue
		}
		return false
	}
	return len(s) > 0
}

// destHost extracts the authority host of a hierarchical destination:
// userinfo and port dropped, IPv6 brackets kept, percent-encoding decoded
// so an IDN host reaches its display form.
func destHost(rest string) string {
	if !strings.HasPrefix(rest, "//") {
		return ""
	}
	authority := rest[2:]
	for _, end := range []byte{'/', '?', '#'} {
		if i := strings.IndexByte(authority, end); i >= 0 {
			authority = authority[:i]
		}
	}
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		authority = authority[i+1:]
	}
	if strings.HasPrefix(authority, "[") {
		if i := strings.IndexByte(authority, ']'); i >= 0 {
			return authority[:i+1]
		}
		return ""
	}
	if i := strings.LastIndexByte(authority, ':'); i >= 0 {
		authority = authority[:i]
	}
	return percentDecode(authority)
}

// percentDecode decodes %XX sequences when the result is valid UTF-8;
// anything else returns the input unchanged so nothing is ever mangled.
func percentDecode(s string) string {
	if !strings.ContainsRune(s, '%') {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHexDigit(s[i+1]) && isHexDigit(s[i+2]) {
			out = append(out, hexVal(s[i+1])<<4|hexVal(s[i+2]))
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	if !utf8.Valid(out) {
		return s
	}
	return string(out)
}

// isHexDigit reports whether c is a hex digit.
func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// hexVal returns the value of a hex digit.
func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// newLink sanitizes a link destination: unsafe runes gone, scheme
// lowercased in the kept URL, openability decided by the scheme, and an
// IDN host carried in both forms.
func newLink(dest []byte) Link {
	raw := sanitizeURL(string(dest))
	scheme, rest, hasScheme := splitScheme(raw)
	if !hasScheme {
		return Link{URL: raw}
	}
	normalized := scheme + ":" + rest
	l := Link{URL: normalized, Openable: openableScheme(scheme)}
	host := destHost(rest)
	if host == "" {
		return l
	}
	l.HostPunycode = hostASCII(host)
	l.Host = hostDisplay(host)
	return l
}

// newImage sanitizes an image destination. Nothing about it is ever
// fetched; the model keeps the alt text and the host only.
func newImage(dest, alt []byte) Image {
	raw := sanitizeURL(string(dest))
	img := Image{Alt: sanitizeText(string(alt)), URL: raw}
	_, rest, hasScheme := splitScheme(raw)
	if hasScheme {
		img.Host = hostDisplay(destHost(rest))
	}
	return img
}
