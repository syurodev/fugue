package noprotoleak

import (
	"reflect"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/syurodev/fugue/tools/fuguevet/internal/scope"
)

// New returns the analyzer. Generated code lives in internal/hostapi/internal/pb,
// which Go's internal rule already hides; this is the second line of defence and
// also keeps the protobuf and Connect runtimes inside the Host API adapter, as
// docs/adr/0001-package-layout.md section 6 lists.
func New() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "noprotoleak",
		Doc:        "report imports of proto-generated code, protobuf or Connect outside internal/hostapi",
		Run:        run,
		ResultType: reflect.TypeFor[struct{}](),
	}
}

func run(pass *analysis.Pass) (any, error) {
	if scope.InTree(pass.Pkg.Path(), "internal/hostapi") {
		return struct{}{}, nil
	}
	for _, file := range pass.Files {
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			if leaks(path) {
				pass.Reportf(spec.Pos(), "import %q: proto-generated and Connect code stays inside internal/hostapi", path)
			}
		}
	}
	return struct{}{}, nil
}

func leaks(path string) bool {
	return scope.InTree(path, "internal/hostapi/internal/pb") ||
		path == "google.golang.org/protobuf" || strings.HasPrefix(path, "google.golang.org/protobuf/") ||
		path == "connectrpc.com/connect" || strings.HasPrefix(path, "connectrpc.com/connect/")
}
