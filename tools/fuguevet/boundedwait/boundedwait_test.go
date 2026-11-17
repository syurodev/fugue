package boundedwait_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/syurodev/fugue/tools/fuguevet/boundedwait"
)

func TestBoundedwaitAnalyzerFlagsUnboundedWaitLoops(t *testing.T) {
	t.Parallel()
	analysistest.Run(t, analysistest.TestData(), boundedwait.New(),
		"fugue/waits", "fugue/clean", "fugue/internal/testutil")
}
