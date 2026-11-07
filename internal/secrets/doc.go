// Package secrets detects known values, token patterns and sensitive assignments
// for display masking. Owner: content safety (H-27). Allowed callers: future
// result gates and transcript display, through their owning services. It does
// no I/O and is separate from platform/redact's log and serialization protection.
package secrets
