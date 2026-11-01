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

func (r repo) layerTable() ([]layerRow, error) {
	rows, err := r.table(adrFile, "| Package | Layer")
	if err != nil {
		return nil, err
	}
	const columns = 5
	var out []layerRow
	for _, cells := range rows {
		if len(cells) < columns {
			return nil, fmt.Errorf("%w: layer table row %v has %d cells, want %d", errBadRow, cells, len(cells), columns)
		}
		row := layerRow{pkg: cells[0], layer: cells[1]}
		if cells[3] != "" {
			row.imports = strings.Split(cells[3], ", ")
		}
		out = append(out, row)
	}
	return out, nil
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
	rows, err := r.layerTable()
	if err != nil {
		return nil, err
	}
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	out := tableProblems(rows)
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
	if internal && !declared {
		out = append(out, fmt.Sprintf("%s: package has no row in the layer table of %s", rel, adrFile))
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

func importProblem(row layerRow, declared, internal bool, rel, irel string) []string {
	switch {
	case internal && !strings.HasPrefix(irel, "internal/"):
		return []string{fmt.Sprintf("%s imports %s: internal packages never import cmd, tools or other roots", rel, irel)}
	case internal && declared && !slices.ContainsFunc(row.imports, func(e string) bool { return matchPackage(e, irel) }):
		return []string{fmt.Sprintf("%s imports %s, which is not in its May import column", rel, irel)}
	case !internal && (strings.HasPrefix(rel, "tools/") || rel == "quality") && strings.HasPrefix(irel, "internal/"):
		return []string{fmt.Sprintf("%s imports %s: tools and quality tests do not depend on internal packages", rel, irel)}
	default:
		return nil
	}
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
		)
	})
}
