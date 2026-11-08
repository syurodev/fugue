package interaction

import (
	"strconv"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// The separator and prefixes of the neutral operation ID keys built by
// NativeOperationKey and VendorOperationKey.
const (
	keySeparator   = ":"
	nativeKeyLabel = "native"
	vendorKeyLabel = "vendor"
)

// NativeOperationKey returns the neutral operation ID of a native tool check,
// keyed on (invocationID, attemptID, checkGeneration). It fails typed when a
// component is empty, the generation is zero or a component carries the key
// separator.
func NativeOperationKey(invocationID, attemptID string, checkGeneration uint64) (string, error) {
	if invocationID == "" || attemptID == "" || checkGeneration == 0 {
		return "", errcode.New(errcode.InvalidArgument, "native operation key requires invocation ID, attempt ID and a nonzero generation")
	}
	return joinKey(nativeKeyLabel, invocationID, attemptID, strconv.FormatUint(checkGeneration, 10))
}

// VendorOperationKey returns the neutral operation ID of a relayed vendor
// request, keyed on (sessionID, turnID, vendorRequestID). A relayed spec's
// Binding carries the same components in TurnID and VendorRequestID, and its
// OperationID must equal this key.
func VendorOperationKey(sessionID, turnID, vendorRequestID string) (string, error) {
	if sessionID == "" || turnID == "" || vendorRequestID == "" {
		return "", errcode.New(errcode.InvalidArgument, "vendor operation key requires session, turn and vendor request IDs")
	}
	return joinKey(vendorKeyLabel, sessionID, turnID, vendorRequestID)
}

func joinKey(prefix string, parts ...string) (string, error) {
	for _, part := range parts {
		if strings.Contains(part, keySeparator) {
			return "", errcode.New(errcode.InvalidArgument, "operation key components must not contain the key separator")
		}
	}
	return prefix + keySeparator + strings.Join(parts, keySeparator), nil
}
