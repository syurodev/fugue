package interaction

import (
	"encoding/base64"
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
// request, keyed on (sessionID, turnID, vendorRequestID). Components are
// base64url-encoded without padding so untrusted vendor IDs containing the key
// separator never fail or collide. A relayed spec's Binding carries the same
// raw components in TurnID and VendorRequestID, and its OperationID must equal
// this key.
func VendorOperationKey(sessionID, turnID, vendorRequestID string) (string, error) {
	if sessionID == "" || turnID == "" || vendorRequestID == "" {
		return "", errcode.New(errcode.InvalidArgument, "vendor operation key requires session, turn and vendor request IDs")
	}
	return vendorKeyLabel + keySeparator +
		base64.RawURLEncoding.EncodeToString([]byte(sessionID)) + keySeparator +
		base64.RawURLEncoding.EncodeToString([]byte(turnID)) + keySeparator +
		base64.RawURLEncoding.EncodeToString([]byte(vendorRequestID)), nil
}

func joinKey(prefix string, parts ...string) (string, error) {
	for _, part := range parts {
		if strings.Contains(part, keySeparator) {
			return "", errcode.New(errcode.InvalidArgument, "operation key components must not contain the key separator")
		}
	}
	return prefix + keySeparator + strings.Join(parts, keySeparator), nil
}

// grantKey scopes a session-level confirmation grant to a single session,
// workspace, grant scope key and target/environment revision.
type grantKey struct {
	sessionID      string
	workspaceID    string
	grantScopeKey  string
	targetRevision string
	envRevision    string
	envTag         string
}

// grantKeyFor derives the session grant key for a binding. The environment
// revision is never taken from the caller: for a binding with a target it is the
// tag registry's current policy revision, so changing a tag's policy (even
// changing it and reverting) invalidates every grant recorded under the old
// revision (interaction §3.4, I-E03). With no TagPolicy a target cannot be
// keyed and the second result is false: targets never get session grants.
func (o *Owner) grantKeyFor(b *Binding) (grantKey, bool) {
	key := grantKey{
		sessionID:      b.SessionID,
		workspaceID:    b.WorkspaceID,
		grantScopeKey:  b.GrantScopeKey,
		targetRevision: b.TargetRevision,
		envRevision:    "",
		envTag:         b.EnvTag,
	}
	if !b.HasTarget {
		return key, true
	}
	if o.tagPolicy == nil {
		return grantKey{}, false
	}
	key.envRevision = strconv.FormatUint(o.tagPolicy.PolicyRevision(b.EnvTag), 10)
	return key, true
}
