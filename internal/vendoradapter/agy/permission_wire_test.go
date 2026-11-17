package agy

import (
	"testing"

	"github.com/syurodev/fugue/internal/vendor"
)

// The ACP v1 RequestPermissionResponse carries `outcome` as a tagged union
// object; agy 1.5.10 rejects the flat string form.
func TestAgyPermissionResponseMatchesTheACPSpecShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		d    vendor.Decision
		want string
	}{
		{"allow", vendor.Decision{Message: "allow"}, `{"jsonrpc":"2.0","id":"p1","result":{"outcome":{"outcome":"selected","optionId":"allow"}}}`},
		{"deny", vendor.Decision{Message: "deny"}, `{"jsonrpc":"2.0","id":"p1","result":{"outcome":{"outcome":"selected","optionId":"deny"}}}`},
		{"cancelled", vendor.Decision{Cancelled: true}, `{"jsonrpc":"2.0","id":"p1","result":{"outcome":{"outcome":"cancelled"}}}`},
	}
	s := &Session{codec: NewCodec()}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pending := pendingServerRequest{rawID: []byte(`"p1"`), method: MethodSessionRequestPermission}
			raw, err := s.encodePermissionResponse(pending, tc.d)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got := string(raw); got != tc.want+"\n" && got != tc.want {
				t.Fatalf("response = %q, want %q", got, tc.want)
			}
		})
	}
}
