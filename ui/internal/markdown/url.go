package markdown

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Link and image destinations are sanitized here before they enter the
// model: the unsafe runes go, the scheme decides openability, and an IDN
// host is kept in both forms so the F10.3b popover can show the punycode
// and the display host side by side. The parsing is local string work on
// purpose: the app production code opens no sockets and keeps clear of
// the net package tree altogether (DA-E06), and net/url would pull it in.
//
// The openability rule is display-equals-destination: an http(s) link is
// openable only when its authority is "simple" — no userinfo, no
// backslash anywhere in the URL, a non-empty host of plain name runes (or
// a bracketed IPv6 literal) and a numeric port — because anything else
// can show one host while the browser opens another (the '\' read as '/'
// userinfo trick, bidi or zero-width runes smuggled in through
// percent-encoding). The kept URL of an openable link carries the
// punycode host, so what the popover shows is exactly what opens.

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

// authorityOf extracts the authority of a hierarchical destination: the
// part between "//" and the first path, query or fragment separator.
func authorityOf(rest string) string {
	if !strings.HasPrefix(rest, "//") {
		return ""
	}
	authority := rest[2:]
	for _, end := range []byte{'/', '?', '#'} {
		if i := strings.IndexByte(authority, end); i >= 0 {
			authority = authority[:i]
		}
	}
	return authority
}

// simpleHost extracts the host of an authority and reports whether it is
// safe to open and display: no userinfo, a non-empty host of plain name
// runes after percent-decoding (letters, digits, combining marks, '-' and
// '.'; bracketed IPv6 literals allow hex, ':' and '.'), and a numeric
// port only. The returned host never contains an unsafe rune; when the
// authority is not simple the cleaned host still comes back for display.
func simpleHost(authority string) (host string, ok bool) {
	if authority == "" || strings.ContainsAny(authority, "@\\") {
		return "", false
	}
	if strings.HasPrefix(authority, "[") {
		return ipv6Host(authority)
	}
	hostport := authority
	if i := strings.LastIndexByte(hostport, ':'); i >= 0 {
		if !numericPort(hostport[i:]) {
			return "", false
		}
		hostport = hostport[:i]
	}
	if hostport == "" {
		return "", false
	}
	decoded := percentDecode(hostport)
	clean := sanitizeText(decoded)
	return clean, validHostRunes(decoded)
}

// ipv6Host validates a bracketed IPv6 authority and returns the literal
// without brackets.
func ipv6Host(authority string) (string, bool) {
	end := strings.IndexByte(authority, ']')
	if end < 0 {
		return "", false
	}
	literal := authority[1:end]
	for i := range len(literal) {
		c := literal[i]
		if !isHexDigit(c) && c != ':' && c != '.' {
			return "", false
		}
	}
	if !numericPort(authority[end+1:]) {
		return "", false
	}
	return literal, true
}

// numericPort reports whether s is empty or a colon followed by digits.
func numericPort(s string) bool {
	if s == "" {
		return true
	}
	if s[0] != ':' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validHostRunes reports whether every rune of a percent-decoded host is a
// plain name rune: ASCII letters, digits, '-' and '.', or Unicode
// letters, digits and combining marks for IDN. Unsafe runes, spaces and
// symbols never pass, so a bidi override or zero-width character smuggled
// in through percent-encoding makes the link unopenable.
func validHostRunes(host string) bool {
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			continue
		case r == '-', r == '.':
			continue
		case unsafeRune(r):
			return false
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.IsMark(r):
			continue
		default:
			return false
		}
	}
	return host != ""
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
// IDN host carried in both forms. An openable http(s) URL is rewritten
// with its punycode host so the shown URL is the opened URL; a mailto
// address runs through sanitizeText like any other text.
func newLink(dest []byte) Link {
	raw := sanitizeURL(string(dest))
	scheme, rest, hasScheme := splitScheme(raw)
	if !hasScheme {
		return Link{URL: raw}
	}
	switch scheme {
	case "http", "https":
		return httpLink(scheme, rest)
	case "mailto":
		return Link{URL: scheme + ":" + sanitizeText(rest), Openable: true}
	default:
		return Link{URL: scheme + ":" + rest}
	}
}

// httpLink builds an http(s) link: openable only with a simple authority,
// the host kept in display and punycode form, and the URL rewritten with
// the punycode host when it opens.
func httpLink(scheme, rest string) Link {
	l := Link{URL: scheme + ":" + rest}
	host, ok := simpleHost(authorityOf(rest))
	ok = ok && validEscapes(rest)
	if ok {
		l.Openable = true
		l.Host = hostDisplay(host)
		l.HostPunycode = hostASCII(host)
		l.URL = rewriteHost(scheme, rest, l.HostPunycode)
		return l
	}
	if host != "" {
		l.Host = hostDisplay(host)
		l.HostPunycode = hostASCII(host)
	}
	return l
}

// validEscapes reports whether every percent sign in the destination
// starts a well-formed %XX escape; a malformed one means the kept URL is
// not a URL any parser would read back the same way, so the link stays
// plain text.
func validEscapes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+2 >= len(s) || !isHexDigit(s[i+1]) || !isHexDigit(s[i+2]) {
			return false
		}
	}
	return true
}

// rewriteHost replaces the authority of the destination with the
// punycode host, keeping the port and everything after the authority; the
// scheme comes in already lowercased.
func rewriteHost(scheme, rest, host string) string {
	port := ""
	authority := authorityOf(rest)
	if i := strings.LastIndexByte(authority, ':'); i >= 0 && !strings.HasPrefix(authority, "[") {
		port = authority[i:]
	}
	tail := ""
	if len(rest) > 2+len(authority) {
		tail = rest[2+len(authority):]
	}
	return scheme + "://" + host + port + tail
}

// newImage sanitizes an image destination. Nothing about it is ever
// fetched; the model keeps the alt text and the host only.
func newImage(dest, alt []byte) Image {
	raw := sanitizeURL(string(dest))
	img := Image{Alt: sanitizeText(string(alt)), URL: raw}
	_, rest, hasScheme := splitScheme(raw)
	if hasScheme {
		if host, ok := simpleHost(authorityOf(rest)); ok {
			img.Host = hostDisplay(host)
		}
	}
	return img
}
