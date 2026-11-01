package scope

import (
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// InTree reports whether pkgPath is the package at tree (for example
// "internal/kernel") or one below it, wherever the module is rooted. Matching
// on a path segment sequence keeps analyzers independent of the module path,
// which differs between the repository and analysistest fixtures.
func InTree(pkgPath, tree string) bool {
	return pkgPath == tree ||
		strings.HasPrefix(pkgPath, tree+"/") ||
		strings.HasSuffix(pkgPath, "/"+tree) ||
		strings.Contains(pkgPath, "/"+tree+"/")
}

// IsTestFile reports whether file is a _test.go file of the pass.
func IsTestFile(pass *analysis.Pass, file *ast.File) bool {
	return strings.HasSuffix(pass.Fset.File(file.Pos()).Name(), "_test.go")
}

// IsStdlib reports whether an import path belongs to the standard library:
// its first element has no dot.
func IsStdlib(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}
