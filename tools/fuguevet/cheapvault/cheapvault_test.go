package cheapvault_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/syurodev/fugue/tools/fuguevet/cheapvault"
)

func TestEveryFixtureUsesTheCheapVault(t *testing.T) {
	t.Parallel()
	analysistest.Run(t, analysistest.TestData(), cheapvault.New(),
		"fugue/fixture", "fugue/clean", "fugue/internal/vault")
}
