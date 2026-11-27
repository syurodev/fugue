package arch

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	probesDir         = "quality/probes"
	probeHeaderPrefix = "# probe-test: "
)

type probeHeaderTarget struct {
	pkg  string
	test string
}

// TestProbeTargets verifies that every `# probe-test: <pkg> <TestName>` header
// under quality/probes names a top-level `func Test...` in that package,
// alongside TestVerificationEvidence which checks the verification registry.
func TestProbeTargets(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkProbeTargets(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkProbeTargets(t.Context(), fixture("probes-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkProbeTargets(t.Context(), fixture("probes-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"01-subtest.patch: probe target TestSubtest is not a top-level func Test... in ./internal/a",
			"02-wrong-pkg.patch: probe target TestInB is not a top-level func Test... in ./internal/a",
			"03-missing-header.patch: expected # probe-test: ./package TestName header",
		)
	})
}

func checkProbeTargets(ctx context.Context, r repo) ([]string, error) {
	patches, err := probePatchFiles(r)
	if err != nil {
		return nil, err
	}
	if len(patches) == 0 {
		return []string{probesDir + " has no probe patch files"}, nil
	}
	byPkg, err := r.testFuncsByPkgForOS(ctx, "linux")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, patch := range patches {
		probs, readErr := checkProbePatch(r, patch, byPkg)
		if readErr != nil {
			return nil, readErr
		}
		out = append(out, probs...)
	}
	slices.Sort(out)
	return out, nil
}

func probePatchFiles(r repo) ([]string, error) {
	all, err := r.files(".patch")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, file := range all {
		if filepath.ToSlash(filepath.Dir(file)) == probesDir {
			out = append(out, file)
		}
	}
	return out, nil
}

func checkProbePatch(r repo, patch string, byPkg map[string]map[string]bool) ([]string, error) {
	raw, err := r.read(patch)
	if err != nil {
		return nil, err
	}
	targets, probs := parseProbeHeaderTargets(patch, raw)
	for _, target := range targets {
		pkgKey := strings.TrimPrefix(target.pkg, "./")
		pkgFuncs, ok := byPkg[pkgKey]
		if !ok {
			probs = append(probs, fmt.Sprintf("%s: probe target package %s does not exist", patch, target.pkg))
			continue
		}
		if !pkgFuncs[target.test] {
			probs = append(probs, fmt.Sprintf("%s: probe target %s is not a top-level func Test... in %s", patch, target.test, target.pkg))
		}
	}
	return probs, nil
}

func parseProbeHeaderTargets(patch, raw string) ([]probeHeaderTarget, []string) {
	var targets []probeHeaderTarget
	var probs []string
	seen := map[probeHeaderTarget]bool{}
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		value, ok := strings.CutPrefix(line, probeHeaderPrefix)
		if !ok {
			break
		}
		fields := strings.Fields(value)
		if len(fields) != 2 || !isValidProbePkg(fields[0]) || !isValidProbeTestName(fields[1]) {
			probs = append(probs, fmt.Sprintf("%s: invalid header %q, expected # probe-test: ./package TestName", patch, line))
			continue
		}
		target := probeHeaderTarget{pkg: fields[0], test: fields[1]}
		if seen[target] {
			probs = append(probs, fmt.Sprintf("%s: duplicate target %s %s", patch, target.pkg, target.test))
			continue
		}
		seen[target] = true
		targets = append(targets, target)
	}
	if len(targets) == 0 && len(probs) == 0 {
		probs = append(probs, patch+": expected # probe-test: ./package TestName header")
	}
	return targets, probs
}

func isValidProbePkg(pkg string) bool {
	rel, ok := strings.CutPrefix(pkg, "./")
	return ok && rel != "" && rel != "." && filepath.IsLocal(rel) &&
		!strings.HasPrefix(rel, "/") && !strings.Contains(rel, "\\") &&
		path.Clean(rel) == rel && !strings.Contains(rel, "...")
}

func isValidProbeTestName(name string) bool {
	rest, ok := strings.CutPrefix(name, "Test")
	if !ok || rest == "" {
		return false
	}
	for i := range len(rest) {
		c := rest[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}
