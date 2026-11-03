package codec

import (
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Supported codec version constants.
const (
	CodecV1 = "v1"
)

// Sentinel errors returned by the canonical codec and receipt deduplicator.
var (
	// ErrInvalidUTF8 is returned when input bytes are not valid UTF-8.
	ErrInvalidUTF8 = errcode.New(errcode.InvalidArgument, "input is not valid UTF-8")

	// ErrDuplicateKey is returned when a JSON object contains duplicate keys at any depth.
	ErrDuplicateKey = errcode.New(errcode.InvalidArgument, "duplicate JSON key")

	// ErrUnknownField is returned when JSON input contains fields unknown to the target struct.
	ErrUnknownField = errcode.New(errcode.InvalidArgument, "unknown JSON field")

	// ErrFloatNotAllowed is returned when a JSON number has a decimal point or exponent.
	ErrFloatNotAllowed = errcode.New(errcode.InvalidArgument, "floating-point numbers are not allowed")

	// ErrIntegerRange is returned when a JSON number exceeds the signed 64-bit integer range.
	ErrIntegerRange = errcode.New(errcode.InvalidArgument, "integer exceeds int64 range")

	// ErrTrailingData is returned when unexpected data follows the JSON root value.
	ErrTrailingData = errcode.New(errcode.InvalidArgument, "unexpected trailing data")

	// ErrMalformedJSON is returned when JSON syntax is invalid or input is truncated.
	ErrMalformedJSON = errcode.New(errcode.InvalidArgument, "malformed JSON input")

	// ErrNilTarget is returned when decoding into a nil destination pointer.
	ErrNilTarget = errcode.New(errcode.InvalidArgument, "target destination cannot be nil")

	// ErrUnrecognizedCommand is returned when a command kind is not in the kernel command registry.
	ErrUnrecognizedCommand = errcode.New(errcode.InvalidArgument, "unrecognized command kind")

	// ErrCodecVersionMismatch is returned when comparing receipts or payloads with different codec versions.
	ErrCodecVersionMismatch = errcode.New(errcode.FailedPrecondition, "codec version mismatch")

	// ErrReceiptConflict is returned when the same command ID has different content, kind, principal, or target.
	ErrReceiptConflict = errcode.New(errcode.FailedPrecondition, "command deduplication conflict: same command ID with different content or metadata")

	// ErrVaultRequired is returned when receipt deduplication cannot open protected payload because the vault is locked.
	ErrVaultRequired = errcode.New(errcode.FailedPrecondition, "vault is locked; cannot open payload for receipt deduplication")
)
