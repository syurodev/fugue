package scope_test

import (
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/tools/fuguevet/internal/scope"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestInTree(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"internal/kernel":                                 true,
		"internal/kernel/sub":                             true,
		"github.com/syurodev/fugue/internal/kernel":       true,
		"github.com/syurodev/fugue/internal/kernel/sub/x": true,
		"github.com/syurodev/fugue/internal/kernelx":      false,
		"github.com/syurodev/fugue/internal/other/kernel": false,
		"github.com/syurodev/fugue/xinternal/kernel":      false,
		"github.com/syurodev/fugue/internal/platform/ids": false,
	}
	for path, want := range cases {
		if got := scope.InTree(path, "internal/kernel"); got != want {
			t.Errorf("InTree(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestIsStdlib(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{"os": true, "log/slog": true, "golang.org/x/tools": false, "example.com/dep": false} {
		if got := scope.IsStdlib(path); got != want {
			t.Errorf("IsStdlib(%q) = %v, want %v", path, got, want)
		}
	}
}
