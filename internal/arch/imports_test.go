package arch

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// checkFuguegateImports pins the user-approved exception in
// docs/adr/0001-package-layout.md: the development tool may import exactly one
// internal package, the time port.
func checkFuguegateImports(ctx context.Context, r repo) ([]string, error) {
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pkgs {
		if p.rel() != "cmd/fuguegate" {
			continue
		}
		for _, imp := range p.allImports() {
			irel, ok := strings.CutPrefix(imp, p.Module.Path+"/")
			if ok && strings.HasPrefix(irel, "internal/") && irel != "internal/platform/clock" {
				out = append(out, fmt.Sprintf("%s imports %s: the only internal package the development tool may import is internal/platform/clock", p.rel(), irel))
			}
		}
	}
	return out, nil
}

// nonStdImport reports an import path owned by a domain, which covers
// third-party modules and every package of this module.
func nonStdImport(imp string) bool {
	first := imp
	if i := strings.IndexByte(imp, '/'); i >= 0 {
		first = imp[:i]
	}
	return strings.Contains(first, ".")
}

// checkClockImports keeps the exception narrow: the time port's production
// imports stay stdlib-only. Test files are outside this rule; the goleak gate
// governs them.
func checkClockImports(ctx context.Context, r repo) ([]string, error) {
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pkgs {
		if p.rel() != "internal/platform/clock" {
			continue
		}
		for _, imp := range p.Imports {
			if !nonStdImport(imp) {
				continue
			}
			name, ok := strings.CutPrefix(imp, p.Module.Path+"/")
			if !ok {
				name = imp
			}
			out = append(out, fmt.Sprintf("%s imports %s: the time port stays a stdlib-only leaf", p.rel(), name))
		}
	}
	return out, nil
}

func TestFuguegateImportsOnlyPlatformClock(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkFuguegateImports(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkFuguegateImports(t.Context(), fixture("fuguegate-imports-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkFuguegateImports(t.Context(), fixture("fuguegate-imports-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"cmd/fuguegate imports internal/platform/redact: the only internal package the development tool may import is internal/platform/clock",
		)
	})
}

// The exception stays narrow: the time port may not grow dependencies.
func TestPlatformClockImportsOnlyStdlib(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkClockImports(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	testFixtureRule(t, checkClockImports, "fuguegate-imports-good", "fuguegate-imports-bad",
		"internal/platform/clock imports internal/platform/redact: the time port stays a stdlib-only leaf",
	)
}
