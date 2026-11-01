package redact

import (
	"fmt"
	"log/slog"
)

// Mask is what a redacted value shows.
const Mask = "[redacted]"

// Secret holds a value that must not reach logs, errors, JSON or test output.
// Every formatting path prints Mask; Reveal is the single way to read it, which
// makes each use easy to find in review. The zero Secret is empty.
type Secret struct {
	value string
}

// NewSecret wraps value.
func NewSecret(value string) Secret { return Secret{value: value} }

// Reveal returns the protected value. Call it only at the point of use, such as
// an Authorization header.
func (s Secret) Reveal() string { return s.value }

// Redacted returns Mask, for code that wants an explicit string.
func (Secret) Redacted() string { return Mask }

// IsZero reports whether the Secret is empty.
func (s Secret) IsZero() bool { return s.value == "" }

// String implements fmt.Stringer.
func (Secret) String() string { return Mask }

// GoString implements fmt.GoStringer so %#v does not print the value.
func (Secret) GoString() string { return "redact.Secret(" + Mask + ")" }

// Format implements fmt.Formatter so no verb, including %x and %q, prints the value.
func (Secret) Format(f fmt.State, _ rune) {
	// A failed write to the formatter's own buffer has nowhere to be reported.
	_, err := f.Write([]byte(Mask))
	if err != nil {
		return
	}
}

// MarshalText implements encoding.TextMarshaler, which json and other encoders use.
func (Secret) MarshalText() ([]byte, error) { return []byte(Mask), nil }

// MarshalJSON implements json.Marshaler.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Mask + `"`), nil }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(Mask) }

// String returns Mask for any string; it is the sanctioned way to mention a
// secret-looking value that is not a Secret, for example a header name's value.
func String(string) string { return Mask }

// Bytes returns Mask for any byte slice, such as key material.
func Bytes([]byte) string { return Mask }
