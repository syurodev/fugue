package nobarego

import (
	"go/ast"
	"reflect"

	"golang.org/x/tools/go/analysis"

	"github.com/syurodev/fugue/tools/fuguevet/internal/scope"
)

// New returns the analyzer. Every goroutine starts through supervise.Go so it
// has an owner, a name and a stop path; the supervise package itself, tests
// included, is the one place a bare `go` statement is legal — plus the test
// files of pkg/: those packages cannot import supervise (pkg never imports
// internal), their goroutines are test servers owned by t.Cleanup stop paths,
// and production pkg/ code stays banned.
func New() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "nobarego",
		Doc:        "report go statements outside internal/platform/supervise; use supervise.Go(ctx, name, fn)",
		Run:        run,
		ResultType: reflect.TypeFor[struct{}](),
	}
}

func run(pass *analysis.Pass) (any, error) {
	if scope.InTree(pass.Pkg.Path(), "internal/platform/supervise") {
		return struct{}{}, nil
	}
	for _, file := range pass.Files {
		if scope.InTree(pass.Pkg.Path(), "pkg") && scope.IsTestFile(pass, file) {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if g, ok := n.(*ast.GoStmt); ok {
				pass.Reportf(g.Pos(), "bare go statement: start goroutines with supervise.Go(ctx, name, fn)")
			}
			return true
		})
	}
	return struct{}{}, nil
}
