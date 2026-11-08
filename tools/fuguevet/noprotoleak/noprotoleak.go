package noprotoleak

import (
	"reflect"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/syurodev/fugue/tools/fuguevet/internal/scope"
)

// New returns the analyzer. Generated code lives in pkg/pb (ADR 0002): the
// Host API adapter, the public client and the entry points may import it with
// the protobuf and Connect runtimes (Q-31 3A); every other package, first of
// all the rest of internal/, may not. The retired internal/hostapi/internal/pb
// tree stays banned so a stray regeneration to it is caught too.
func New() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "noprotoleak",
		Doc:        "report imports of proto-generated code, protobuf or Connect outside internal/hostapi, pkg/client and cmd",
		Run:        run,
		ResultType: reflect.TypeFor[struct{}](),
	}
}

func run(pass *analysis.Pass) (any, error) {
	// The external test package of an allowed importer (pkg/client_test)
	// shares its rights: it tests the same boundary.
	if allowedImporter(strings.TrimSuffix(pass.Pkg.Path(), "_test")) {
		return struct{}{}, nil
	}
	for _, file := range pass.Files {
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			if leaks(path) {
				pass.Reportf(spec.Pos(), "import %q: proto-generated and Connect code is importable only by internal/hostapi, pkg/client and cmd", path)
			}
		}
	}
	return struct{}{}, nil
}

// allowedImporter reports whether pkg is the generated tree itself, the Host
// API adapter, the public client, or a cmd entry point.
func allowedImporter(pkg string) bool {
	return scope.InTree(pkg, "pkg/pb") ||
		scope.InTree(pkg, "internal/hostapi") ||
		scope.InTree(pkg, "pkg/client") ||
		scope.InTree(pkg, "cmd")
}

func leaks(path string) bool {
	return scope.InTree(path, "internal/hostapi/internal/pb") ||
		scope.InTree(path, "pkg/pb") ||
		path == "google.golang.org/protobuf" || strings.HasPrefix(path, "google.golang.org/protobuf/") ||
		path == "connectrpc.com/connect" || strings.HasPrefix(path, "connectrpc.com/connect/")
}
