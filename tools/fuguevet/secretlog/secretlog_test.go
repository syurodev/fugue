package secretlog_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/syurodev/fugue/tools/fuguevet/secretlog"
)

func TestSecretLog(t *testing.T) {
	t.Parallel()
	analysistest.Run(t, analysistest.TestData(), secretlog.New(),
		"fugue/svc/logs", "fugue/internal/platform/redact")
}
