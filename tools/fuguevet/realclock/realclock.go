package realclock

import (
	"go/ast"
	"go/types"
	"reflect"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/syurodev/fugue/tools/fuguevet/internal/scope"
)

// perfTree is the one place a test may measure wall time: speed budgets run
// there, only in the nightly job, as a median over several runs. clockTree
// owns the real clock and tests it.
const (
	perfTree  = "internal/perf"
	clockTree = "internal/platform/clock"
)

// New returns the analyzer. A test that reads the wall clock or times
// something compares a number a loaded runner changes, so it fails at random;
// tests use the fake clock, and a hang-only deadline goes through
// testutil.HangGuard, which never measures speed.
func New() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "realclock",
		Doc:        "report wall-clock reads and timing measurements in test files outside internal/perf",
		Run:        run,
		ResultType: reflect.TypeFor[struct{}](),
	}
}

func run(pass *analysis.Pass) (any, error) {
	// The clock package tests its own real implementation; its external test
	// package carries the _test suffix.
	path := strings.TrimSuffix(pass.Pkg.Path(), "_test")
	if scope.InTree(path, perfTree) || scope.InTree(path, clockTree) {
		return struct{}{}, nil
	}
	for _, file := range pass.Files {
		if !scope.IsTestFile(pass, file) {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if what := wallClock(pass.TypesInfo.Uses[id]); what != "" {
					pass.Reportf(id.Pos(), "%s reads or measures the wall clock in a test: use the fake clock, testutil.HangGuard for a hang-only deadline, or move the speed budget to internal/perf", what)
				}
			}
			return true
		})
	}
	return struct{}{}, nil
}

// wallClock names the forbidden object, or returns "" for any other one.
func wallClock(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	path, name := obj.Pkg().Path(), obj.Name()
	switch obj.(type) {
	case *types.Func:
		if path == "time" && slices.Contains([]string{"Now", "Since", "Until"}, name) {
			return "time." + name
		}
		if path == "testing" && (name == "Benchmark" || name == "Elapsed") {
			return "testing." + name
		}
	case *types.TypeName:
		if scope.InTree(path, clockTree) && name == "Real" {
			return "clock.Real"
		}
	}
	return ""
}
