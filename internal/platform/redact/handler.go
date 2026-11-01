package redact

import (
	"io"
	"log/slog"
	"regexp"
)

var secretKey = regexp.MustCompile(`(?i)token|secret|password|apikey|credential|privatekey`)

// SecretKey reports whether an attribute key looks like it names a secret.
func SecretKey(key string) bool { return secretKey.MatchString(key) }

// ReplaceAttr masks the value of every attribute whose key looks like a secret,
// at any depth of groups and in attributes added with With. It is the second
// line of defence behind the secretlog analyzer and Secret itself.
func ReplaceAttr(_ []string, a slog.Attr) slog.Attr {
	if SecretKey(a.Key) {
		return slog.String(a.Key, Mask)
	}
	return a
}

// Options returns handler options at the given level that install ReplaceAttr.
func Options(level slog.Leveler) *slog.HandlerOptions {
	return &slog.HandlerOptions{Level: level, ReplaceAttr: ReplaceAttr, AddSource: false}
}

// NewJSONHandler returns a JSON handler that masks secret-looking attributes.
func NewJSONHandler(w io.Writer, level slog.Leveler) slog.Handler {
	return slog.NewJSONHandler(w, Options(level))
}

// NewTextHandler returns a text handler that masks secret-looking attributes.
func NewTextHandler(w io.Writer, level slog.Leveler) slog.Handler {
	return slog.NewTextHandler(w, Options(level))
}
