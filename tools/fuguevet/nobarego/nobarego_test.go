package nobarego_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/syurodev/fugue/tools/fuguevet/nobarego"
)

func TestNoBareGo(t *testing.T) {
	t.Parallel()
	analysistest.Run(t, analysistest.TestData(), nobarego.New(),
		"bare", "clean", "fugue/internal/platform/supervise")
}
