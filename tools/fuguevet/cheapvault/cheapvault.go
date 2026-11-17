package cheapvault

import (
	"go/ast"
	"go/types"
	"reflect"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/syurodev/fugue/tools/fuguevet/internal/scope"
)

// New returns the analyzer. vault.Setup and crypto.NewKeyring derive keys with
// Argon2id at the production cost (64 MiB, three passes), which under the race
// detector dominates a package's run time and starves parallel tests on a
// slow runner. A fixture takes the minimum-cost keyring from
// internal/testutil/cheapvault instead; only the vault and crypto packages
// test the production cost itself.
func New() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "cheapvault",
		Doc:        "report vault.Setup and crypto.NewKeyring in test files outside internal/vault and internal/crypto; use internal/testutil/cheapvault",
		Run:        run,
		ResultType: reflect.TypeFor[struct{}](),
	}
}

func run(pass *analysis.Pass) (any, error) {
	path := strings.TrimSuffix(pass.Pkg.Path(), "_test")
	if scope.InTree(path, "internal/vault") || scope.InTree(path, "internal/crypto") {
		return struct{}{}, nil
	}
	for _, file := range pass.Files {
		if !scope.IsTestFile(pass, file) {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if what := productionCost(pass.TypesInfo.Uses[id]); what != "" {
					pass.Reportf(id.Pos(), "%s pays the production Argon2id cost in a fixture: use internal/testutil/cheapvault", what)
				}
			}
			return true
		})
	}
	return struct{}{}, nil
}

func productionCost(obj types.Object) string {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return ""
	}
	switch path := fn.Pkg().Path(); {
	case scope.InTree(path, "internal/vault") && fn.Name() == "Setup":
		return "vault.Service.Setup"
	case scope.InTree(path, "internal/crypto") && fn.Name() == "NewKeyring":
		return "crypto.NewKeyring"
	}
	return ""
}
