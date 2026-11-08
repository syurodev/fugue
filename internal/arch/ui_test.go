package arch

import (
	"context"
	"fmt"
	"go/ast"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const uiModulePath = "github.com/syurodev/fugue/ui"

// coreUIImport reports whether an import path ties the core module to the UI
// module or its toolkit, which ADR 0002 forbids in both directions.
func coreUIImport(imp string) bool {
	return imp == uiModulePath || strings.HasPrefix(imp, uiModulePath+"/") ||
		imp == "gioui.org" || strings.HasPrefix(imp, "gioui.org/")
}

// checkCoreNeverImportsUI enforces TestCoreNeverImportsUI: no package of the
// core module imports the ui module or the UI toolkit (DA-E06, ADR 0002).
func checkCoreNeverImportsUI(ctx context.Context, r repo) ([]string, error) {
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pkgs {
		for _, imp := range slices.Compact(slices.Sorted(slices.Values(p.allImports()))) {
			if coreUIImport(imp) {
				out = append(out, fmt.Sprintf("%s imports %s: the core module never imports the ui module or its toolkit", p.rel(), imp))
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func TestCoreNeverImportsUI(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkCoreNeverImportsUI(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkCoreNeverImportsUI(t.Context(), fixture("ui-core-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkCoreNeverImportsUI(t.Context(), fixture("ui-core-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"internal/app imports github.com/syurodev/fugue/ui/internal/appwin: the core module never imports the ui module or its toolkit",
			"internal/app imports gioui.org/app: the core module never imports the ui module or its toolkit",
		)
	})
}

// allowedClientImport reports whether a ui file may import the core module
// path imp: the public client, the generated code, and the ui module itself
// (ADR 0002).
func allowedClientImport(imp string) bool {
	return imp == "github.com/syurodev/fugue/pkg/client" ||
		strings.HasPrefix(imp, "github.com/syurodev/fugue/pkg/pb/") ||
		imp == uiModulePath || strings.HasPrefix(imp, uiModulePath+"/")
}

// bannedCoreImport reports an import of the core module a ui file may never
// make: anything outside pkg/client and pkg/pb (DA-E06).
func bannedCoreImport(rel, imp string) []string {
	const corePath = "github.com/syurodev/fugue/"
	if strings.HasPrefix(imp, corePath) && !allowedClientImport(imp) {
		return []string{fmt.Sprintf("%s imports %s: the app imports the core only through pkg/client and pkg/pb", rel, imp)}
	}
	return nil
}

// bannedHostObjectImport reports an import that puts a socket or a database
// handle into the app: their objects belong to the host, and the app reaches
// them only through pkg/client (DA-E06).
func bannedHostObjectImport(rel, imp string) []string {
	for _, banned := range []string{"net", "database/sql", "modernc.org/"} {
		if imp == banned || strings.HasPrefix(imp, banned) {
			return []string{fmt.Sprintf("%s imports %s: the app opens no socket or database itself, only pkg/client does", rel, imp)}
		}
	}
	return nil
}

// isOSOpenCall reports whether name is an os function that opens or writes
// data files, matched on the selector name of an os-qualified call in
// production ui files.
func isOSOpenCall(name string) bool {
	switch name {
	case "Open", "OpenFile", "Create", "ReadFile", "WriteFile":
		return true
	default:
		return false
	}
}

// fileAppProblems checks one ui Go file: the import rules for every file, and
// the direct data-file calls only for production files (the tests run
// scripted hosts and sockets of their own).
func fileAppProblems(r repo, rel string) ([]string, error) {
	f, err := r.parse(rel)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, spec := range f.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, bannedCoreImport(rel, imp)...)
	}
	if strings.HasSuffix(rel, "_test.go") {
		// The ui tests run scripted hosts and sockets of their own; the
		// host-object rules bind the app's production files.
		return out, nil
	}
	for _, spec := range f.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, bannedHostObjectImport(rel, imp)...)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != "os" || !isOSOpenCall(sel.Sel.Name) {
			return true
		}
		out = append(out, fmt.Sprintf("%s calls os.%s: the app writes and reads only through the Host API", rel, sel.Sel.Name))
		return true
	})
	return out, nil
}

// checkAppWritesOnlyThroughHostAPI enforces TestAppWritesOnlyThroughHostAPI
// over every ui Go file: the app imports the core only through pkg/client and
// pkg/pb, opens no socket or database itself, and touches no data file
// directly.
func checkAppWritesOnlyThroughHostAPI(r repo) ([]string, error) {
	files, err := r.files(".go")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, file := range files {
		if !strings.HasPrefix(file, "ui/") {
			continue
		}
		problems, err := fileAppProblems(r, file)
		if err != nil {
			return nil, err
		}
		out = append(out, problems...)
	}
	slices.Sort(out)
	return out, nil
}

func TestAppWritesOnlyThroughHostAPI(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		target     repo
		violations []string
	}{
		{name: "repository", target: repo{root: repoRoot}},
		{name: "clean fixture", target: fixture("ui-writes-good")},
		{
			name:   "violations",
			target: fixture("ui-writes-bad"),
			violations: []string{
				"ui/internal/hostconn/hostconn.go imports net: the app opens no socket or database itself, only pkg/client does",
				"ui/internal/hostconn/hostconn.go imports github.com/syurodev/fugue/internal/platform/clock: the app imports the core only through pkg/client and pkg/pb",
				"ui/internal/hostconn/hostconn.go calls os.WriteFile: the app writes and reads only through the Host API",
				"ui/cmd/fugue-app/main.go imports github.com/syurodev/fugue/internal/storage: the app imports the core only through pkg/client and pkg/pb",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := checkAppWritesOnlyThroughHostAPI(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.violations) == 0 {
				expectNone(t, got)
				return
			}
			expectViolations(t, got, tc.violations...)
		})
	}
}
