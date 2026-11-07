// Package secrets detects known values, token patterns and sensitive assignments
// for display masking. Owner: content safety (H-27). Allowed callers: future
// result gates and transcript display, through their owning services. It does
// no I/O and is separate from platform/redact's log and serialization protection.
//
// Key-name detection considers only the first assignment on each line. Later
// pairs in single-line JSON objects are a known stage A limit; known values
// and token patterns are still detected throughout the entire text.
package secrets
