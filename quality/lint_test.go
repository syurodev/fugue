package quality

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// Fixture files carry a directive on a line of their own:
//
//	// lint:expect <linter> [text]   the linter must report this file (with text in the message)
//	// lint:ok <linter>              the linter must stay silent on this file
//
// Each fixture set is copied into a temporary module named like the real one,
// so rules scoped by path (internal/kernel, cmd/*/main.go, ...) apply exactly
// as they do in the repository.
var (
	directivePattern = regexp.MustCompile(`^// lint:(expect|ok) (\S+)(?: (.+))?$`)
	versionPattern   = regexp.MustCompile(`GOLANGCI_LINT_VERSION:\s*(v[0-9][0-9.]*)`)

	errNoVersion = errors.New("GOLANGCI_LINT_VERSION not found in Taskfile.yml")
)

type directive struct {
	kind     string
	linter   string
	contains string
	file     string
}

type issue struct {
	FromLinter string
	Text       string
	Pos        struct{ Filename string }
}

func golangciArgs(t *testing.T, args ...string) []string {
	t.Helper()
	task, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	m := versionPattern.FindSubmatch(task)
	if m == nil {
		t.Fatal(errNoVersion)
	}
	return append([]string{"run", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@" + string(m[1])}, args...)
}

// golangci runs the pinned golangci-lint and returns stdout and the exit code.
// Exit code 1 means "issues found" and is not a failure of the run itself.
func golangci(ctx context.Context, t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	//nolint:gosec // G204: the program is `go`; arguments are the pinned linter module and flags written in this file.
	cmd := exec.CommandContext(ctx, "go", golangciArgs(t, args...)...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var exit *exec.ExitError
	err := cmd.Run()
	if stdout.Len() == 0 {
		t.Logf("golangci-lint %v printed nothing (err %v); stderr:\n%s", args, err, stderr.String())
	}
	switch {
	case err == nil:
		return stdout.String(), 0
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return stdout.String(), 1
	default:
		t.Fatalf("golangci-lint %v: %v\n%s", args, err, stderr.String())
		return "", -1
	}
}

// materialize copies one fixture set next to the repository configuration.
func materialize(t *testing.T, set string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "lint", set))); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile("../.golangci.yml")
	if err != nil {
		t.Fatal(err)
	}
	gomod := "module github.com/syurodev/fugue\n\ngo 1.27.2\n"
	if extra, err := fs.ReadFile(os.DirFS(dir), "go.mod.extra"); err == nil {
		gomod += "\n" + string(extra)
	}
	for name, content := range map[string]string{".golangci.yml": string(config), "go.mod": gomod} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func directives(t *testing.T, root string) []directive {
	t.Helper()
	var out []directive
	tree := os.DirFS(root)
	err := fs.WalkDir(tree, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		data, err := fs.ReadFile(tree, path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if m := directivePattern.FindStringSubmatch(line); m != nil {
				out = append(out, directive{kind: m[1], linter: m[2], contains: m[3], file: path})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func lintSet(ctx context.Context, t *testing.T, dir string) []issue {
	t.Helper()
	out, _ := golangci(ctx, t, dir, "run", "--output.json.path=stdout", "--show-stats=false", "./...")
	var report struct{ Issues []issue }
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out)
	}
	return report.Issues
}

func reported(issues []issue, d directive) bool {
	return slices.ContainsFunc(issues, func(i issue) bool {
		return i.FromLinter == d.linter && filepath.ToSlash(i.Pos.Filename) == d.file && strings.Contains(i.Text, d.contains)
	})
}

// A gate that does not fire on its violating fixture is broken; an exception
// that does not hold means the gate is stricter than its documentation.
func TestLintFixtures(t *testing.T) {
	t.Parallel()
	for _, set := range []string{"generic", "paths"} {
		t.Run(set, func(t *testing.T) {
			t.Parallel()
			dir := materialize(t, set)
			issues := lintSet(t.Context(), t, dir)
			for _, i := range issues {
				if i.FromLinter == "typecheck" {
					t.Errorf("fixture does not compile: %s: %s", i.Pos.Filename, i.Text)
				}
			}
			checkDirectives(t, issues, directives(t, dir))
		})
	}
}

func checkDirectives(t *testing.T, issues []issue, found []directive) {
	t.Helper()
	if len(found) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, d := range found {
		got := reported(issues, d)
		switch {
		case d.kind == "expect" && !got:
			t.Errorf("%s: %s did not report %q", d.file, d.linter, d.contains)
		case d.kind == "ok" && got:
			t.Errorf("%s: %s reported a permitted use", d.file, d.linter)
		}
	}
}

// Enabling a linter without a violating fixture would leave a gate unproven.
func TestEveryEnabledLinterHasAFixture(t *testing.T) {
	t.Parallel()
	out, _ := golangci(t.Context(), t, "..", "linters", "--json")
	var listed struct{ Enabled []struct{ Name string } }
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("decode linters: %v\n%s", err, out)
	}
	if len(listed.Enabled) == 0 {
		t.Fatal("no enabled linters reported")
	}
	have := map[string]bool{}
	for _, set := range []string{"generic", "paths"} {
		for _, d := range directives(t, filepath.Join("testdata", "lint", set)) {
			if d.kind == "expect" {
				have[d.linter] = true
			}
		}
	}
	for _, l := range listed.Enabled {
		if !have[l.Name] {
			t.Errorf("linter %s is enabled but has no `lint:expect` fixture", l.Name)
		}
	}
}

// The formatters run through `golangci-lint fmt --diff`, which `task fmt` uses.
func TestFormattersFlagUnformattedFiles(t *testing.T) {
	t.Parallel()
	dir := materialize(t, "fmt")
	out, code := golangci(t.Context(), t, dir, "fmt", "--diff", "./...")
	if code != 1 {
		t.Errorf("fmt --diff exited %d on unformatted files, want 1 so that task fmt fails", code)
	}
	for _, bad := range []string{"unformatted.go", "gofumpt.go", "imports.go"} {
		if !strings.Contains(out, bad) {
			t.Errorf("formatter diff does not mention %s:\n%s", bad, out)
		}
	}
	if strings.Contains(out, "formatted_ok.go") {
		t.Errorf("formatter diff flags a formatted file:\n%s", out)
	}
}
