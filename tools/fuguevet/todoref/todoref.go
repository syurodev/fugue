package todoref

import (
	"go/ast"
	"reflect"
	"regexp"

	"golang.org/x/tools/go/analysis"
)

const todoFormat = "TODO(FUG-<n>): ... or TODO(F<phase>.<n>): ... (optional lowercase suffix on the spec item ID)"

var (
	marker = regexp.MustCompile(`\b(TODO|FIXME|HACK|XXX)\b(?:\(([^)]*)\))?(:?)`)
	itemID = regexp.MustCompile(`^(?:FUG-\d+|F\d+\.\d+[a-z]?)$`)
)

// New returns the analyzer for work-item references in comments.
func New() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "todoref",
		Doc:        "require to-do markers in the form " + todoFormat,
		Run:        run,
		ResultType: reflect.TypeFor[struct{}](),
	}
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		for _, group := range file.Comments {
			for _, comment := range group.List {
				checkComment(pass, comment)
			}
		}
	}
	return struct{}{}, nil
}

func checkComment(pass *analysis.Pass, comment *ast.Comment) {
	for _, match := range marker.FindAllStringSubmatch(comment.Text, -1) {
		switch {
		case match[1] != "TODO":
			pass.Reportf(comment.Pos(), "%s is not allowed; use %s", match[1], todoFormat)
		case !itemID.MatchString(match[2]) || match[3] != ":":
			pass.Reportf(comment.Pos(), "TODO must use %s", todoFormat)
		}
	}
}
