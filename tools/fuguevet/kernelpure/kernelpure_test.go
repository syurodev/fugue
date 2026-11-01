package kernelpure_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/syurodev/fugue/tools/fuguevet/kernelpure"
)

// bad violates every rule; good is the shape kernel code must have, including a
// test file that may import os; other shows the rules apply only to the kernel.
func TestKernelPure(t *testing.T) {
	t.Parallel()
	analysistest.Run(t, analysistest.TestData(), kernelpure.New(),
		"fugue/internal/kernel/bad", "fugue/internal/kernel/good", "fugue/internal/other")
}
