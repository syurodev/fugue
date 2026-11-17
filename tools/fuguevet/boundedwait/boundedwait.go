package boundedwait

import (
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/syurodev/fugue/tools/fuguevet/internal/scope"
)

// testutilTree owns the sanctioned wait helpers, which are the one place a
// loop yields or sleeps, under a hang deadline.
const testutilTree = "internal/testutil"

// New returns the analyzer. A test loop that yields (runtime.Gosched) or
// sleeps (time.Sleep) until some state appears has no end if the state never
// comes: it spins until the package timeout kills every test in the package
// with one panic and no name. The loop ends only when its own condition
// holds, so a wait must use testutil.Poll, testutil.Spin or testutil.Await,
// which fail the one test after a hang deadline. t.Context() and a context
// the test cancels at its end are not deadlines.
//
// A loop that cannot leave early (a range over a fixed count or collection, or
// a counted loop, with no return, break or goto) is bounded work, not a wait,
// and may yield between rounds.
func New() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "boundedwait",
		Doc:        "report test loops that call runtime.Gosched or time.Sleep with no hang deadline",
		Run:        run,
		ResultType: reflect.TypeFor[struct{}](),
	}
}

func run(pass *analysis.Pass) (any, error) {
	if scope.InTree(strings.TrimSuffix(pass.Pkg.Path(), "_test"), testutilTree) {
		return struct{}{}, nil
	}
	for _, file := range pass.Files {
		if !scope.IsTestFile(pass, file) {
			continue
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				walk(pass, fn.Body, nil)
			}
		}
	}
	return struct{}{}, nil
}

// walk visits n with the enclosing loops of the current function (innermost
// last). A function literal starts a new function: its calls belong to its own
// loops, not to the loop that creates it.
func walk(pass *analysis.Pass, n ast.Node, loops []ast.Stmt) {
	ast.Inspect(n, func(c ast.Node) bool {
		switch c := c.(type) {
		case *ast.FuncLit:
			walk(pass, c.Body, nil)
			return false
		case *ast.ForStmt:
			walkLoop(pass, c, c.Body, append(loops, c))
			return false
		case *ast.RangeStmt:
			walkLoop(pass, c, c.Body, append(loops, c))
			return false
		case *ast.CallExpr:
			if what := yields(pass.TypesInfo, c); what != "" && anyWaits(loops) {
				pass.Reportf(c.Pos(), "%s in a loop that can only end when its condition holds waits with no deadline: use testutil.Poll, testutil.Spin or testutil.Await, which fail the test after a hang timeout", what)
			}
		}
		return true
	})
}

func walkLoop(pass *analysis.Pass, loop ast.Stmt, body *ast.BlockStmt, loops []ast.Stmt) {
	if f, ok := loop.(*ast.ForStmt); ok {
		for _, part := range []ast.Node{f.Init, f.Cond, f.Post} {
			if part != nil {
				walk(pass, part, loops[:len(loops)-1])
			}
		}
	} else if r, ok := loop.(*ast.RangeStmt); ok {
		walk(pass, r.X, loops[:len(loops)-1])
	}
	walk(pass, body, loops)
}

// yields names the call when it is runtime.Gosched or time.Sleep.
func yields(info *types.Info, call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return ""
	}
	switch path, name := fn.Pkg().Path(), fn.Name(); {
	case path == "runtime" && name == "Gosched", path == "time" && name == "Sleep":
		return path + "." + name
	}
	return ""
}

func anyWaits(loops []ast.Stmt) bool {
	for _, l := range loops {
		if waits(l) {
			return true
		}
	}
	return false
}

// waits reports whether the loop can run for an unbounded time: it has no
// fixed bound (a bare loop or one with only a condition) or it can leave early.
func waits(loop ast.Stmt) bool {
	switch l := loop.(type) {
	case *ast.ForStmt:
		if l.Init == nil || l.Post == nil {
			return true
		}
		return leavesEarly(l.Body)
	case *ast.RangeStmt:
		return leavesEarly(l.Body)
	}
	return false
}

// leavesEarly reports whether body returns, jumps, or breaks out of the loop
// it belongs to. A break inside a nested loop, switch or select leaves only
// that statement; one with a label may leave this loop.
func leavesEarly(body *ast.BlockStmt) bool {
	return exits(body, false)
}

func exits(n ast.Node, nested bool) bool {
	found := false
	ast.Inspect(n, func(c ast.Node) bool {
		if found {
			return false
		}
		switch c := c.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = true
		case *ast.BranchStmt:
			found = jumpsOut(c, nested)
		default:
			if inner := innerBody(c); inner != nil {
				found = exits(inner, true)
				return false
			}
		}
		return true
	})
	return found
}

func jumpsOut(b *ast.BranchStmt, nested bool) bool {
	return b.Tok == token.GOTO || b.Label != nil || (b.Tok == token.BREAK && !nested)
}

// innerBody is the body of a statement that owns an unlabeled break.
func innerBody(n ast.Node) *ast.BlockStmt {
	switch s := n.(type) {
	case *ast.ForStmt:
		return s.Body
	case *ast.RangeStmt:
		return s.Body
	case *ast.SwitchStmt:
		return s.Body
	case *ast.TypeSwitchStmt:
		return s.Body
	case *ast.SelectStmt:
		return s.Body
	}
	return nil
}
