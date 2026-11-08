package noprotoleak_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/syurodev/fugue/tools/fuguevet/noprotoleak"
)

func TestNoProtoLeak(t *testing.T) {
	t.Parallel()
	analysistest.Run(t, analysistest.TestData(), noprotoleak.New(),
		"leaky", "clean", "fugue/internal/hostapi/session", "fugue/pkg/client")
}
