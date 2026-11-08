package sealing

import "math"

// Protected payload size limits.
const (
	MaxPayloadPlaintext    = 1 << 20
	MaxSessionPayloadTotal = 1 << 30
)

// ExceedsLimit checks the total without integer overflow.
func ExceedsLimit(current, addition, limit int64) bool {
	return addition > math.MaxInt64-current || current+addition > limit
}
