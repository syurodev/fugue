package todoref_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/syurodev/fugue/tools/fuguevet/todoref"
)

func TestTodoRef(t *testing.T) {
	t.Parallel()
	analysistest.Run(t, analysistest.TestData(), todoref.New(), "todos")
}
