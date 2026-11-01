package quality

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const modFixtures = "testdata/mod"

var allowedLicensesPattern = regexp.MustCompile(`ALLOWED_LICENSES:\s*(\S+)`)

// execIn runs a program and returns its combined output and exit code; only a
// failure to start it fails the test.
func execIn(ctx context.Context, t *testing.T, dir string, env []string, name string, args ...string) (string, int) {
	t.Helper()
	//nolint:gosec // G204: programs are `go` or tool binaries resolved by `go tool -n`; arguments are literals in this file.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	var exit *exec.ExitError
	if err := cmd.Run(); err != nil {
		if !errors.As(err, &exit) {
			t.Fatalf("%s %v: %v", name, args, err)
		}
		return out.String(), exit.ExitCode()
	}
	return out.String(), 0
}

func toolBinary(t *testing.T, name string) string {
	t.Helper()
	out, code := execIn(t.Context(), t, ".", nil, "go", "tool", "-n", name)
	if code != 0 {
		t.Fatalf("go tool -n %s: %s", name, out)
	}
	return strings.TrimSpace(out)
}

func fixture(name string) string {
	return filepath.Join(modFixtures, name)
}

// expectGate asserts that the command fails on the violating fixture and
// passes on its clean twin.
func expectGate(t *testing.T, bad, good, env []string, name string, args ...string) {
	t.Helper()
	if out, code := execIn(t.Context(), t, bad[0], env, name, args...); code == 0 {
		t.Errorf("%s %v passed on violating fixture %s:\n%s", name, args, bad[0], out)
	}
	if out, code := execIn(t.Context(), t, good[0], env, name, args...); code != 0 {
		t.Errorf("%s %v failed on clean fixture %s:\n%s", name, args, good[0], out)
	}
}

func TestTidyGate(t *testing.T) {
	t.Parallel()
	expectGate(t, []string{fixture("tidy-bad")}, []string{fixture("tidy-good")}, nil, "go", "mod", "tidy", "-diff")
}

// `go mod verify` compares the module cache with the hashes recorded when each
// module was downloaded, so the violation is a cache edited after download.
func TestVerifyGate(t *testing.T) {
	t.Parallel()
	cache := t.TempDir()
	realCache, code := execIn(t.Context(), t, ".", nil, "go", "env", "GOMODCACHE")
	if code != 0 {
		t.Fatal("go env GOMODCACHE failed")
	}
	env := []string{
		"GOMODCACHE=" + cache,
		"GOFLAGS=-modcacherw",
		"GOPROXY=file://" + filepath.Join(strings.TrimSpace(realCache), "cache", "download"),
		"GONOSUMDB=*",
		"GONOSUMCHECK=1",
	}
	dir := fixture("verify-bad")
	if out, code := execIn(t.Context(), t, dir, env, "go", "mod", "download"); code != 0 {
		t.Fatalf("go mod download: %s", out)
	}
	if out, code := execIn(t.Context(), t, dir, env, "go", "mod", "verify"); code != 0 {
		t.Fatalf("pristine cache must verify:\n%s", out)
	}
	target := filepath.Join(cache, "github.com", "google", "go-cmp@v0.7.0", "cmp", "compare.go")
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("package cmp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := execIn(t.Context(), t, dir, env, "go", "mod", "verify"); code == 0 {
		t.Fatalf("go mod verify passed on a modified module cache:\n%s", out)
	}
}

// govulncheck reads a local database in the vuln.go.dev layout, so the test
// needs no network and no real vulnerability.
func TestGovulncheckGate(t *testing.T) {
	t.Parallel()
	bin := toolBinary(t, "govulncheck")
	abs := func(name string) string {
		p, err := filepath.Abs(fixture(name))
		if err != nil {
			t.Fatal(err)
		}
		return "file://" + p
	}
	if out, code := execIn(t.Context(), t, fixture("vuln-bad"), nil, bin, "-db", abs("vulndb"), "./..."); code == 0 {
		t.Errorf("govulncheck passed on a reachable vulnerability:\n%s", out)
	}
	if out, code := execIn(t.Context(), t, fixture("vuln-good"), nil, bin, "-db", abs("vulndb-empty"), "./..."); code != 0 {
		t.Errorf("govulncheck failed on a clean module:\n%s", out)
	}
}

func TestLicenseGate(t *testing.T) {
	t.Parallel()
	task, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	m := allowedLicensesPattern.FindSubmatch(task)
	if m == nil {
		t.Fatal("ALLOWED_LICENSES not found in Taskfile.yml")
	}
	bin := toolBinary(t, "go-licenses")
	args := []string{"check", "./...", "--allowed_licenses=" + string(m[1])}
	out, code := execIn(t.Context(), t, fixture("licenses-bad"), nil, bin, args...)
	if code == 0 || !strings.Contains(out, "go-version") {
		t.Errorf("go-licenses must reject the MPL-2.0 dependency go-version (exit %d):\n%s", code, out)
	}
	if out, code := execIn(t.Context(), t, fixture("licenses-good"), nil, bin, args...); code != 0 {
		t.Errorf("go-licenses failed on a BSD-3-Clause dependency:\n%s", out)
	}
}

// The tests above run the commands in isolation; this one keeps them tied to
// what `task check` really executes.
func TestTaskfileRunsEveryGate(t *testing.T) {
	t.Parallel()
	task, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"go mod tidy -diff",
		"go mod verify",
		"go tool govulncheck ./...",
		"go tool go-licenses check ./... --allowed_licenses={{.ALLOWED_LICENSES}}",
		"-race -shuffle=on -count=1",
		"fmt --diff ./...",
		"run ./...",
		"go vet -vettool={{.ROOT_DIR}}/bin/fuguevet ./...",
		"fuguegate coverage",
		"fuguegate deadcode",
		"fuguegate fuzz",
		"fuguegate mutation",
	} {
		if !strings.Contains(string(task), want) {
			t.Errorf("Taskfile.yml does not run %q", want)
		}
	}
	_, afterCheck, _ := strings.Cut(string(task), "  check:")
	check, _, _ := strings.Cut(afterCheck, "  fix:")
	for _, step := range []string{"fmt", "lint", "vet", "test", "coverage", "deadcode", "mod", "fuzz-short", "mutation-changed"} {
		if !strings.Contains(check, "- task: "+step+"\n") {
			t.Errorf("`task check` does not run %s", step)
		}
	}
}
