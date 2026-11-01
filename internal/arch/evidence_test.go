package arch

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	roadmapStatusColumn   = 5
	roadmapEvidenceColumn = 4
)

var (
	roadmapID    = regexp.MustCompile(`^F\d+\.\d+$`)
	evidenceName = regexp.MustCompile(`\b(?:Test|Fuzz)[A-Z]\w*`)
)

func ciOSes() []string   { return []string{"linux", "darwin"} }
func ciArches() []string { return []string{"amd64", "arm64"} }
func knownOSes() []string {
	return []string{"linux", "darwin", "windows", "freebsd", "openbsd", "netbsd", "solaris", "plan9", "js", "wasip1", "android", "ios", "aix", "illumos", "dragonfly"}
}

// testFunc is a Test or Fuzz function and whether CI builds its file.
type testFunc struct {
	file     string
	inCI     bool
	reasonNo string
}

func (r repo) testFuncs() (map[string]testFunc, error) {
	files, err := r.files("_test.go")
	if err != nil {
		return nil, err
	}
	funcs := map[string]testFunc{}
	for _, file := range files {
		f, err := r.parse(file)
		if err != nil {
			return nil, err
		}
		inCI, why := includedInCI(file, f)
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && (strings.HasPrefix(fn.Name.Name, "Test") || strings.HasPrefix(fn.Name.Name, "Fuzz")) {
				funcs[fn.Name.Name] = testFunc{file: file, inCI: inCI, reasonNo: why}
			}
		}
	}
	return funcs, nil
}

// includedInCI reports whether the CI matrix (linux and darwin, amd64 and
// arm64, no custom tags) builds the file, and why not when it does not.
func includedInCI(file string, f *ast.File) (bool, string) {
	expr := buildConstraint(f)
	for _, goos := range ciOSes() {
		for _, goarch := range ciArches() {
			if nameMatches(file, goos, goarch) && (expr == nil || expr.Eval(func(tag string) bool {
				return tag == goos || tag == goarch || (tag == "unix") || strings.HasPrefix(tag, "go1.")
			})) {
				return true, ""
			}
		}
	}
	if expr != nil {
		return false, "build constraint " + expr.String()
	}
	return false, "file name restricts it to an OS outside the CI matrix"
}

func buildConstraint(f *ast.File) constraint.Expr {
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			break
		}
		for _, c := range group.List {
			if constraint.IsGoBuild(c.Text) {
				if expr, err := constraint.Parse(c.Text); err == nil {
					return expr
				}
			}
		}
	}
	return nil
}

// nameMatches applies the _GOOS and _GOARCH file name suffix rule.
func nameMatches(file, goos, goarch string) bool {
	name := strings.TrimSuffix(strings.TrimSuffix(file[strings.LastIndex(file, "/")+1:], ".go"), "_test")
	parts := strings.Split(name, "_")
	if n := len(parts); n >= 2 {
		last := parts[n-1]
		if slices.Contains(knownOSes(), last) {
			return last == goos
		}
		if last == "amd64" || last == "arm64" || last == "386" || last == "arm" || last == "wasm" {
			return last == goarch && (n < 3 || !slices.Contains(knownOSes(), parts[n-2]) || parts[n-2] == goos)
		}
	}
	return true
}

// doneEvidence maps each Done roadmap item to the test names its Evidence cell
// mentions.
func (r repo) doneEvidence() (map[string][]string, error) {
	md, err := r.read(roadmapFile)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, line := range strings.Split(md, "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) <= roadmapStatusColumn || !roadmapID.MatchString(strings.TrimSpace(cells[0])) {
			continue
		}
		if strings.TrimSpace(cells[roadmapStatusColumn]) == "Done" {
			out[strings.TrimSpace(cells[0])] = evidenceName.FindAllString(cells[roadmapEvidenceColumn], -1)
		}
	}
	return out, nil
}

// checkRoadmapEvidence enforces TestRoadmapEvidence: the tests that a Done row
// names exist and are built by CI.
func checkRoadmapEvidence(r repo) ([]string, error) {
	done, err := r.doneEvidence()
	if err != nil {
		return nil, err
	}
	funcs, err := r.testFuncs()
	if err != nil {
		return nil, err
	}
	var out []string
	for id, names := range done {
		for _, name := range names {
			tf, ok := funcs[name]
			switch {
			case !ok:
				out = append(out, fmt.Sprintf("%s is Done but its evidence %s does not exist", id, name))
			case !tf.inCI:
				out = append(out, fmt.Sprintf("%s is Done but its evidence %s in %s is excluded from CI: %s", id, name, tf.file, tf.reasonNo))
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func TestRoadmapEvidence(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkRoadmapEvidence(repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkRoadmapEvidence(fixture("evidence-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkRoadmapEvidence(fixture("evidence-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"F1.1 is Done but its evidence TestMissing does not exist",
			"F1.2 is Done but its evidence TestTagged in internal/a/tagged_test.go is excluded from CI: build constraint integration",
			"F1.3 is Done but its evidence TestWindowsOnly in internal/a/a_windows_test.go is excluded from CI",
		)
	})
}
