package kernelpure

import (
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/syurodev/fugue/tools/fuguevet/internal/scope"
)

// New returns the analyzer. The kernel decides and evolves state from values it
// is given; it never reads a clock, touches the OS, starts goroutines or keeps
// mutable package state. Test files are exempt: they may import testing and
// comparison helpers.
func New() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "kernelpure",
		Doc:        "keep internal/kernel pure: allowlisted stdlib imports, no package variables, no go statements",
		Run:        run,
		ResultType: reflect.TypeFor[struct{}](),
	}
}

func allowedImports() []string {
	return []string{
		"bytes", "cmp", "errors", "fmt", "iter", "maps", "math", "math/bits",
		"slices", "sort", "strconv", "strings", "time", "unicode", "unicode/utf8",
	}
}

// pureFmt are the fmt functions that only build values; the rest print or scan.
func pureFmt() []string {
	return []string{"Sprint", "Sprintf", "Sprintln", "Errorf", "Append", "Appendf", "Appendln"}
}

func run(pass *analysis.Pass) (any, error) {
	// ".test" packages are the generated test mains the go tool builds.
	if !scope.InTree(pass.Pkg.Path(), "internal/kernel") || strings.HasSuffix(pass.Pkg.Path(), ".test") {
		return struct{}{}, nil
	}
	for _, file := range pass.Files {
		if scope.IsTestFile(pass, file) || ast.IsGenerated(file) {
			continue
		}
		checkImports(pass, file)
		checkVars(pass, file)
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.GoStmt:
				pass.Reportf(n.Pos(), "go statement in the kernel: it must stay free of goroutines")
			case *ast.SelectorExpr:
				checkMember(pass, n)
			}
			return true
		})
	}
	return struct{}{}, nil
}

func checkImports(pass *analysis.Pass, file *ast.File) {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		if !scope.IsStdlib(path) || !slices.Contains(allowedImports(), path) {
			pass.Reportf(spec.Pos(), "kernel import %q is not allowlisted: the kernel imports only pure standard-library packages", path)
		}
	}
}

// checkVars allows only error sentinels (`var ErrX = errors.New(...)`) and
// blank interface assertions at package level.
func checkVars(pass *analysis.Pass, file *ast.File) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || isSentinel(pass, vs) || isBlank(vs) {
				continue
			}
			pass.Reportf(vs.Pos(), "package variable %s in the kernel: use a constant or pass the value in", vs.Names[0].Name)
		}
	}
}

func isBlank(vs *ast.ValueSpec) bool {
	return len(vs.Names) == 1 && vs.Names[0].Name == "_"
}

func isSentinel(pass *analysis.Pass, vs *ast.ValueSpec) bool {
	if len(vs.Names) != 1 || len(vs.Values) != 1 || !strings.HasPrefix(strings.ToLower(vs.Names[0].Name), "err") {
		return false
	}
	call, ok := vs.Values[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := pass.TypesInfo.Uses[sel.Sel].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == "errors" && fn.Name() == "New"
}

// checkMember rejects `time` functions (types, constants and methods are
// values) and the printing or scanning parts of `fmt`.
func checkMember(pass *analysis.Pass, sel *ast.SelectorExpr) {
	obj := pass.TypesInfo.Uses[sel.Sel]
	if obj == nil || obj.Pkg() == nil {
		return
	}
	switch obj.Pkg().Path() {
	case "time":
		if fn, ok := obj.(*types.Func); ok && fn.Signature().Recv() == nil {
			pass.Reportf(sel.Pos(), "time.%s in the kernel: take time as a value, never read or wait on a clock", obj.Name())
		}
	case "fmt":
		if _, ok := obj.(*types.Func); ok && !slices.Contains(pureFmt(), obj.Name()) {
			pass.Reportf(sel.Pos(), "fmt.%s in the kernel: it performs I/O; only the formatting functions are allowed", obj.Name())
		}
	}
}
