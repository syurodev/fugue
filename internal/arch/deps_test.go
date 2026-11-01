package arch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const approvedMarker = "(approved:"

func allowedLicenses() []string {
	return []string{"MIT", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "ISC"}
}

type goMod struct {
	Require []struct {
		Path     string
		Indirect bool
	}
	Tool []struct{ Path string }
}

func (r repo) goMod(ctx context.Context) (goMod, error) {
	cmd := exec.CommandContext(ctx, "go", "mod", "edit", "-json")
	cmd.Dir = r.root
	out, err := cmd.Output()
	if err != nil {
		return goMod{}, fmt.Errorf("go mod edit in %s: %w", r.root, err)
	}
	var m goMod
	if err := json.Unmarshal(out, &m); err != nil {
		return goMod{}, fmt.Errorf("decode go.mod: %w", err)
	}
	return m, nil
}

// governedModules are the modules that need a row: direct requirements and the
// modules that provide `tool` directives. Indirect modules are covered by
// go-licenses and govulncheck.
func governedModules(m goMod) []string {
	var mods []string
	for _, req := range m.Require {
		if !req.Indirect {
			mods = append(mods, req.Path)
		}
	}
	for _, tool := range m.Tool {
		best := ""
		for _, req := range m.Require {
			if (tool.Path == req.Path || strings.HasPrefix(tool.Path, req.Path+"/")) && len(req.Path) > len(best) {
				best = req.Path
			}
		}
		if best == "" {
			best = tool.Path
		}
		mods = append(mods, best)
	}
	slices.Sort(mods)
	return slices.Compact(mods)
}

// checkDependencies enforces TestDependencyAllowlist.
func checkDependencies(ctx context.Context, r repo) ([]string, error) {
	m, err := r.goMod(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.table(dependenciesFile, "| Module | License")
	if err != nil {
		return nil, err
	}
	byModule := map[string][]string{}
	for _, cells := range rows {
		byModule[cells[0]] = cells
	}
	var out []string
	for _, mod := range governedModules(m) {
		cells, ok := byModule[mod]
		if !ok {
			out = append(out, fmt.Sprintf("%s: no row in %s", mod, dependenciesFile))
			continue
		}
		out = append(out, rowProblems(mod, cells)...)
	}
	return out, nil
}

func rowProblems(mod string, cells []string) []string {
	const columns = 5
	if len(cells) < columns {
		return []string{fmt.Sprintf("%s: row has %d cells, want module, license, reason, approver, enters", mod, len(cells))}
	}
	var out []string
	for i, name := range []string{"license", "reason", "approver"} {
		if cells[i+1] == "" {
			out = append(out, fmt.Sprintf("%s: row has an empty %s", mod, name))
		}
	}
	if !slices.Contains(allowedLicenses(), cells[1]) && !strings.Contains(cells[1], approvedMarker) {
		out = append(out, fmt.Sprintf("%s: license %q is outside the allowed list and not marked %s <decision>)", mod, cells[1], approvedMarker))
	}
	return out
}

func TestDependencyAllowlist(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkDependencies(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkDependencies(t.Context(), fixture("deps-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkDependencies(t.Context(), fixture("deps-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"example.com/undocumented: no row",
			"example.com/copyleft: license \"GPL-3.0\" is outside the allowed list",
			"example.com/vague: row has an empty reason",
			"example.com/tooled: no row",
		)
		for _, v := range got {
			if strings.Contains(v, "example.com/indirect") || strings.Contains(v, "example.com/approved") {
				t.Errorf("indirect and explicitly approved modules are fine, got %s", v)
			}
		}
	})
}
