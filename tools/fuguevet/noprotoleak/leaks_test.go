package noprotoleak

import "testing"

// Generated code cannot be imported from a fixture outside its tree because Go's
// internal rule stops the fixture compiling, so the path rule is tested directly.
func TestLeaks(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/session/v1": true,
		"github.com/syurodev/fugue/internal/hostapi/internal/pb":                  true,
		"google.golang.org/protobuf/proto":                                        true,
		"google.golang.org/protobuf":                                              true,
		"connectrpc.com/connect":                                                  true,
		"github.com/syurodev/fugue/internal/hostapi":                              false,
		"github.com/syurodev/fugue/internal/kernel":                               false,
		"google.golang.org/grpc":                                                  false,
		"google.golang.org/protobufx":                                             false,
	}
	for path, want := range cases {
		if got := leaks(path); got != want {
			t.Errorf("leaks(%q) = %v, want %v", path, got, want)
		}
	}
}
