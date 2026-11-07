package arch

import (
	"context"
	"go/ast"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// checkDeterministicIDs reports every non-test call to ids.NewDeterministic:
// the deterministic generator is not random and must never feed a key, so
// only test files may construct it; production uses ids.CryptoRandom.
func checkDeterministicIDs(ctx context.Context, r repo) ([]string, error) {
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pkgs {
		// TestGoFiles and XTestGoFiles are outside the rule by design.
		for _, f := range p.GoFiles {
			rel := filepath.ToSlash(filepath.Join(p.rel(), f))
			found, err := fileCallsNewDeterministic(r, p, rel)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
	}
	return out, nil
}

// fileCallsNewDeterministic reports the call sites of ids.NewDeterministic in
// one non-test file, when the file imports the id port.
func fileCallsNewDeterministic(r repo, p *goPackage, rel string) ([]string, error) {
	file, err := r.parse(rel)
	if err != nil {
		return nil, err
	}
	name := idsImportName(file, p.Module.Path)
	if name == "" {
		return nil, nil
	}
	return deterministicCallSites(rel, name, file), nil
}

// deterministicCallSites returns one violation line per call of
// ids.NewDeterministic in the file.
func deterministicCallSites(rel, idsName string, f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == idsName && sel.Sel.Name == "NewDeterministic" {
			out = append(out, rel+" calls ids.NewDeterministic: the deterministic generator is not random and must never feed a key; production uses ids.CryptoRandom")
		}
		return true
	})
	return out
}

// idsImportName returns the file's local name for internal/platform/ids, or
// "" when the file does not import it.
func idsImportName(f *ast.File, modulePath string) string {
	idsPath := modulePath + "/internal/platform/ids"
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != idsPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return path[strings.LastIndexByte(path, '/')+1:]
	}
	return ""
}

func TestNewDeterministicIsTestOnly(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkDeterministicIDs(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	testFixtureRule(t, checkDeterministicIDs, "deterministic-ids-good", "deterministic-ids-bad",
		"cmd/prod/main.go calls ids.NewDeterministic: the deterministic generator is not random and must never feed a key; production uses ids.CryptoRandom",
	)
}
