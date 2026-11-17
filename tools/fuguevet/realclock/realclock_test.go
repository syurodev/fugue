package realclock_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/syurodev/fugue/tools/fuguevet/realclock"
)

func TestRealclockAnalyzerFlagsWallClockInTests(t *testing.T) {
	t.Parallel()
	analysistest.Run(t, analysistest.TestData(), realclock.New(),
		"fugue/wall", "fugue/clean", "fugue/internal/perf")
}
