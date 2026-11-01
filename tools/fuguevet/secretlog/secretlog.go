package secretlog

import (
	"go/ast"
	"go/types"
	"reflect"
	"regexp"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"

	"github.com/syurodev/fugue/tools/fuguevet/internal/scope"
)

// New returns the analyzer. An argument to a logging or error-building call is
// reported when it is a redact.Secret, a byte slice or array named like key
// material, or any non-numeric value named like a secret. Numbers and booleans
// are exempt so token counts and flags can be logged. An argument is safe when
// it is a call into the redact package, which is how a secret is shown.
func New() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "secretlog",
		Doc:        "report secrets passed to slog, fmt, errors or test output without redact",
		Run:        run,
		ResultType: reflect.TypeFor[struct{}](),
	}
}

var (
	secretName = regexp.MustCompile(`(?i)token|secret|password|apikey|credential|privatekey`)
	keyName    = regexp.MustCompile(`(?i)key|dek|kek|nonce|salt|seed`)
)

func run(pass *analysis.Pass) (any, error) {
	if scope.InTree(pass.Pkg.Path(), "internal/platform/redact") {
		return struct{}{}, nil
	}
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isSink(pass, call) {
				for _, arg := range call.Args {
					check(pass, arg)
				}
			}
			return true
		})
	}
	return struct{}{}, nil
}

func callee(pass *analysis.Pass, call *ast.CallExpr) *types.Func {
	if fn, ok := typeutil.Callee(pass.TypesInfo, call).(*types.Func); ok {
		return fn
	}
	return nil
}

var testingSink = regexp.MustCompile(`^(Log|Error|Fatal|Skip)f?$`)

func isSink(pass *analysis.Pass, call *ast.CallExpr) bool {
	fn := callee(pass, call)
	if fn == nil || fn.Pkg() == nil {
		return false
	}
	switch fn.Pkg().Path() {
	case "log/slog", "fmt":
		return true
	case "errors":
		return fn.Name() == "New" || fn.Name() == "Join"
	case "testing":
		return testingSink.MatchString(fn.Name())
	default:
		return false
	}
}

func check(pass *analysis.Pass, expr ast.Expr) {
	switch x := expr.(type) {
	case *ast.BasicLit:
	case *ast.ParenExpr:
		check(pass, x.X)
	case *ast.BinaryExpr:
		check(pass, x.X)
		check(pass, x.Y)
	case *ast.CallExpr:
		checkCall(pass, x)
	default:
		checkValue(pass, expr)
	}
}

// checkCall follows conversions such as string(token); a call into redact is
// the sanctioned way to show a secret and any other call is judged on its own
// when the walk reaches it.
func checkCall(pass *analysis.Pass, call *ast.CallExpr) {
	if tv, ok := pass.TypesInfo.Types[call.Fun]; ok && tv.IsType() && len(call.Args) == 1 {
		check(pass, call.Args[0])
	}
}

func checkValue(pass *analysis.Pass, expr ast.Expr) {
	t := pass.TypesInfo.TypeOf(expr)
	name := leafName(expr)
	switch {
	case t == nil:
	case isSecret(t):
		pass.Reportf(expr.Pos(), "redact.Secret value %s reaches a log or error message: show it through the redact package", name)
	case isNumeric(t):
	case isBytes(t) && keyName.MatchString(name):
		pass.Reportf(expr.Pos(), "key material %s reaches a log or error message: show it through the redact package", name)
	case secretName.MatchString(name):
		pass.Reportf(expr.Pos(), "%s looks like a secret and reaches a log or error message: show it through the redact package", name)
	}
}

func leafName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.IndexExpr:
		return leafName(x.X)
	case *ast.StarExpr:
		return leafName(x.X)
	case *ast.UnaryExpr:
		return leafName(x.X)
	default:
		return ""
	}
}

func isSecret(t types.Type) bool {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Name() == "Secret" && scope.InTree(named.Obj().Pkg().Path(), "internal/platform/redact")
}

func isNumeric(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)
	return ok && basic.Info()&(types.IsNumeric|types.IsBoolean) != 0
}

func isBytes(t types.Type) bool {
	var elem types.Type
	switch u := t.Underlying().(type) {
	case *types.Slice:
		elem = u.Elem()
	case *types.Array:
		elem = u.Elem()
	default:
		return false
	}
	basic, ok := elem.Underlying().(*types.Basic)
	return ok && basic.Kind() == types.Byte
}
