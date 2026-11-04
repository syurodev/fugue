package modeladapter

import "testing"

func TestErrorClassRetryClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		class     ErrorClass
		retryable bool
	}{
		{ErrorRateLimit, true},
		{ErrorOverloaded, true},
		{ErrorServer, true},
		{ErrorTimeout, true},
		{ErrorTransport, true},
		{ErrorEmptyResponse, true},
		{ErrorContextOverflow, false},
		{ErrorAuth, false},
		{ErrorPermission, false},
		{ErrorQuota, false},
		{ErrorInvalidRequest, false},
		{ErrorModelNotFound, false},
		{ErrorRefusal, false},
		{ErrorContentFilter, false},
		{ErrorClass("unknown"), false},
	}
	for _, testCase := range cases {
		if got := testCase.class.Retryable(); got != testCase.retryable {
			t.Errorf("Retryable(%q) = %t, want %t", testCase.class, got, testCase.retryable)
		}
	}
}
