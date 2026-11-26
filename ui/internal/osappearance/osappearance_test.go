package osappearance

import (
	"context"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestAppearanceString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   Appearance
		want string
	}{
		{Unknown, "unknown"},
		{Light, "light"},
		{Dark, "dark"},
		{Appearance(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("Appearance(%d).String() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWatchStopsOnCancel(t *testing.T) {
	t.Parallel()
	src := New()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		src.Watch(ctx, func(Appearance) {})
	}()

	cancel()
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatalf("Watch did not stop on context cancellation: %v", t.Context().Err())
	}
}
