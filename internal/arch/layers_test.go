package arch

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type layerRow struct {
	pkg     string
	layer   string
	imports []string
}

func (r repo) layerTable() ([]layerRow, []string, error) {
	const columns = 5
	var out []layerRow
	var problems []string
	seen := map[string]string{}
	for _, file := range adrFiles() {
		rows, err := r.table(file, "| Package | Layer")
		if err != nil {
			return nil, nil, err
		}
		for _, cells := range rows {
			if len(cells) < columns {
				return nil, nil, fmt.Errorf("%w: layer table row %v has %d cells, want %d", errBadRow, cells, len(cells), columns)
			}
			pkg := cells[0]
			if _, dup := seen[pkg]; dup {
				problems = append(problems, fmt.Sprintf("package %s has two layer rows", pkg))
			}
			seen[pkg] = file
			row := layerRow{pkg: pkg, layer: cells[1]}
			if cells[3] != "" {
				row.imports = strings.Split(cells[3], ", ")
			}
			out = append(out, row)
		}
	}
	return out, problems, nil
}

func findRow(rows []layerRow, rel string) (layerRow, bool) {
	i := slices.IndexFunc(rows, func(row layerRow) bool { return matchPackage(row.pkg, rel) })
	if i < 0 {
		return layerRow{}, false
	}
	return rows[i], true
}

// tableProblems checks the table itself: every allowed import names a row and
// never points to a higher layer.
func tableProblems(rows []layerRow) []string {
	var out []string
	for _, row := range rows {
		for _, entry := range row.imports {
			target, ok := findRow(rows, strings.TrimSuffix(entry, "/..."))
			switch {
			case row.layer == "test":
				out = append(out, fmt.Sprintf("layer table: %s is a test-only package and may import nothing, but lists %s", row.pkg, entry))
			case !ok:
				out = append(out, fmt.Sprintf("layer table: %s may import %s, which has no row", row.pkg, entry))
			case higher(target.layer, row.layer):
				out = append(out, fmt.Sprintf("layer table: %s (layer %s) may import %s (layer %s): edges point down or sideways only", row.pkg, row.layer, entry, target.layer))
			}
		}
	}
	return out
}

func higher(a, b string) bool {
	x, errA := strconv.Atoi(a)
	y, errB := strconv.Atoi(b)
	return errA == nil && errB == nil && x > y
}

// checkLayers enforces quality-gates section 5 TestLayerGraph: every internal
// package has a row, and every import between module packages is listed.
func checkLayers(ctx context.Context, r repo) ([]string, error) {
	rows, tableProbs, err := r.layerTable()
	if err != nil {
		return nil, err
	}
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	out = append(out, tableProbs...)
	out = append(out, tableProblems(rows)...)
	for _, p := range pkgs {
		out = append(out, packageProblems(rows, p)...)
	}
	slices.Sort(out)
	return out, nil
}

func packageProblems(rows []layerRow, p *goPackage) []string {
	rel := p.rel()
	var out []string
	row, declared := findRow(rows, rel)
	internal := strings.HasPrefix(rel, "internal/")
	isPkg := strings.HasPrefix(rel, "pkg/")
	if (internal || isPkg) && !declared {
		out = append(out, fmt.Sprintf("%s: package has no row in the layer table of %s", rel, strings.Join(adrFiles(), ", ")))
	}
	for _, imp := range slices.Compact(slices.Sorted(slices.Values(p.allImports()))) {
		irel, ok := strings.CutPrefix(imp, p.Module.Path+"/")
		if !ok || irel == rel {
			continue
		}
		out = append(out, importProblem(row, declared, internal, rel, irel)...)
	}
	return out
}

func allowedImport(imports []string, target string) bool {
	return slices.ContainsFunc(imports, func(e string) bool { return matchPackage(e, target) })
}

func pkgImportProblem(row layerRow, declared bool, rel, irel string) []string {
	if strings.HasPrefix(irel, "internal/") {
		return []string{fmt.Sprintf("%s imports %s: packages under pkg/ never import internal packages", rel, irel)}
	}
	if declared && !allowedImport(row.imports, irel) {
		return []string{fmt.Sprintf("%s imports %s, which is not in its May import column", rel, irel)}
	}
	return nil
}

func importProblem(row layerRow, declared, internal bool, rel, irel string) []string {
	if strings.HasPrefix(rel, "pkg/") {
		return pkgImportProblem(row, declared, rel, irel)
	}
	if !internal {
		if (strings.HasPrefix(rel, "tools/") || rel == "quality") && strings.HasPrefix(irel, "internal/") {
			return []string{fmt.Sprintf("%s imports %s: tools and quality tests do not depend on internal packages", rel, irel)}
		}
		return nil
	}
	if !strings.HasPrefix(irel, "internal/") {
		return []string{fmt.Sprintf("%s imports %s: internal packages never import cmd, tools or other roots", rel, irel)}
	}
	if declared && !allowedImport(row.imports, irel) {
		return []string{fmt.Sprintf("%s imports %s, which is not in its May import column", rel, irel)}
	}
	return nil
}

func TestLayerGraph(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkLayers(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkLayers(t.Context(), fixture("layers-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkLayers(t.Context(), fixture("layers-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"internal/undeclared: package has no row",
			"internal/kernel imports internal/app, which is not in its May import column",
			"internal/kernel imports cmd/tool: internal packages never import cmd",
			"tools/vet imports internal/kernel: tools and quality tests do not depend on internal packages",
			"internal/platform/clock (layer 0) may import internal/kernel (layer 1)",
			"internal/arch is a test-only package and may import nothing",
			"internal/app may import internal/ghost, which has no row",
			"package internal/kernel has two layer rows",
			"pkg/client imports internal/platform/clock: packages under pkg/ never import internal packages",
		)
	})
}
