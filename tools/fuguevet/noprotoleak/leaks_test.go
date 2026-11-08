package noprotoleak

import (
	"strings"
	"testing"
)

// Generated code cannot be imported from a fixture outside its tree because Go's
// internal rule stops the fixture compiling, so the path rule is tested directly.
func TestLeaks(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"github.com/syurodev/fugue/pkg/pb/fugue/host/v1":         true,
		"github.com/syurodev/fugue/pkg/pb":                       true,
		"github.com/syurodev/fugue/internal/hostapi/internal/pb": true,
		"google.golang.org/protobuf/proto":                       true,
		"google.golang.org/protobuf":                             true,
		"connectrpc.com/connect":                                 true,
		"github.com/syurodev/fugue/internal/hostapi":             false,
		"github.com/syurodev/fugue/internal/kernel":              false,
		"google.golang.org/grpc":                                 false,
		"google.golang.org/protobufx":                            false,
		"github.com/syurodev/fugue/pkg/pbx/fugue/host/v1":        false,
	}
	for path, want := range cases {
		if got := leaks(path); got != want {
			t.Errorf("leaks(%q) = %v, want %v", path, got, want)
		}
	}
}

// The importers Q-31 3A allows are the generated tree itself, the Host API
// adapter, the public client and the entry points.
func TestAllowedImporters(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"github.com/syurodev/fugue/pkg/pb/fugue/host/v1":         true,
		"github.com/syurodev/fugue/pkg/pb":                       true,
		"github.com/syurodev/fugue/pkg/client":                   true,
		"github.com/syurodev/fugue/internal/hostapi":             true,
		"github.com/syurodev/fugue/internal/hostapi/internal/pb": true,
		"github.com/syurodev/fugue/cmd/fugue":                    true,
		"github.com/syurodev/fugue/cmd/fuguegate":                true,
		"github.com/syurodev/fugue/internal/kernel":              false,
		"github.com/syurodev/fugue/internal/app":                 false,
		"github.com/syurodev/fugue/pkg/client_test":              true,
		"github.com/syurodev/fugue/pkg/client_testx":             false,
		"github.com/syurodev/fugue/pkg/clientx":                  false,
		"github.com/syurodev/fugue/internal/hostapix":            false,
	}
	for path, want := range cases {
		// run() strips the external-test suffix before the check.
		if got := allowedImporter(strings.TrimSuffix(path, "_test")); got != want {
			t.Errorf("allowedImporter(%q) = %v, want %v", path, got, want)
		}
	}
}
