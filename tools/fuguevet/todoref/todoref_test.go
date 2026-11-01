package todoref_test

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/syurodev/fugue/tools/fuguevet/todoref"
)

func TestTodoRef(t *testing.T) {
	t.Parallel()
	roadmap, err := filepath.Abs(filepath.Join("testdata", "roadmap.md"))
	if err != nil {
		t.Fatal(err)
	}
	a := todoref.New()
	if err := a.Flags.Set("roadmap", roadmap); err != nil {
		t.Fatal(err)
	}
	analysistest.Run(t, analysistest.TestData(), a, "todos")
}
