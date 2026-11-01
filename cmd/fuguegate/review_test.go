package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const approver = "syurodev"

const (
	notConventional = "not a conventional commit"
	coAuthored      = "co-authored-by trailer"
)

// checkLint runs a lint function over messages it must accept and messages it
// must reject, the latter with a phrase the report must contain (lower case).
func checkLint(t *testing.T, lint func(string) []string, accept []string, reject map[string]string) {
	t.Helper()
	for _, m := range accept {
		if got := lint(m); len(got) > 0 {
			t.Errorf("%q rejected: %v", m, got)
		}
	}
	for m, want := range reject {
		if got := lint(m); !strings.Contains(strings.ToLower(strings.Join(got, "\n")), want) {
			t.Errorf("%q: got %v, want mention of %q", m, got, want)
		}
	}
}

func TestLintTitle(t *testing.T) {
	t.Parallel()
	checkLint(t, lintTitle, []string{
		"feat(kernel): derive wait reasons (F2.1)",
		"fix(storage): reject a wrong pragma",
		"chore: bump a tool",
		"docs(adr)!: rename the layer table",
		"test(arch): add architecture tests and ratchets (F0.5)",
	}, map[string]string{
		"Update stuff":                 notConventional,
		"feat: ":                       notConventional,
		"feature(x): y":                notConventional,
		"feat(Kernel): upper scope":    notConventional,
		"fix: ok\n\nCo-Authored-By: A": coAuthored,
		"fix: 🤖 generated":             "generator footer",
	})
}

func TestLintMessage(t *testing.T) {
	t.Parallel()
	checkLint(t, lintMessage, []string{
		"feat(x): add y\n\nbody text\n",
		"Merge branch 'main' into feat/x",
		"Merge pull request #3 from a/b",
		"Revert \"feat: x\"",
		"fixup! feat: x",
	}, map[string]string{
		"feat(x): add y\n\nCo-Authored-By: Claude <noreply@example.com>\n":       coAuthored,
		"feat(x): add y\n\nco-authored-by: someone\n":                            coAuthored,
		"feat(x): add y\n\nSigned-off-by: A <a@b.c>\n":                           "signed-off-by trailer",
		"feat(x): add y\n\nGenerated with [Claude Code](https://claude.com/x)\n": "generator footer",
		"added a thing": notConventional,
		"":              notConventional,
	})
}

func TestCommitMsgHookReadsTheFileAndIgnoresComments(t *testing.T) {
	t.Parallel()
	write := func(content string) string {
		path := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := execute(t, "commitmsg", write("feat(x): fine\n\n# Co-Authored-By in a comment is ignored\n")); err != nil {
		t.Errorf("clean message rejected: %v", err)
	}
	if _, err := execute(t, "commitmsg", write("feat(x): bad\n\nCo-Authored-By: Claude <noreply@example.com>\n")); !errors.Is(err, ErrCommitLint) {
		t.Errorf("attribution accepted: %v", err)
	}
	if _, err := execute(t, "commitmsg", write("no type here\n")); !errors.Is(err, ErrCommitLint) {
		t.Errorf("non-conventional subject accepted: %v", err)
	}
	if _, err := execute(t, "commitmsg"); !errors.Is(err, ErrUsage) {
		t.Errorf("missing file argument: %v", err)
	}
}

func TestCommitLintChecksTheTitle(t *testing.T) {
	t.Parallel()
	if _, err := execute(t, "commitlint", "-title", "fix(x): ok"); err != nil {
		t.Errorf("good title rejected: %v", err)
	}
	if _, err := execute(t, "commitlint", "-title", "WIP"); !errors.Is(err, ErrCommitLint) {
		t.Errorf("bad title accepted: %v", err)
	}
}

func TestGateGuard(t *testing.T) {
	t.Parallel()
	gate := []string{"Taskfile.yml", "internal/arch/size_test.go", "quality/ratchet.json", ".github/workflows/pr.yml", "tools/fuguevet/main.go", ".golangci.yml", "docs/quality-gates.md", "docs/dependencies.md"}
	plain := []string{"README.md", "internal/kernel/state.go", "docs/roadmap.md", "cmd/fuguegate/main.go", "docs/adr/0001-package-layout.md"}
	for _, f := range gate {
		if !isGateFile(f) {
			t.Errorf("%s must be a gate file", f)
		}
		if err := checkGate([]string{f}, nil, "", approver); !errors.Is(err, ErrGateGuard) {
			t.Errorf("%s without the label: %v", f, err)
		}
	}
	for _, f := range plain {
		if isGateFile(f) {
			t.Errorf("%s must not be a gate file", f)
		}
		if err := checkGate([]string{f}, nil, "", approver); err != nil {
			t.Errorf("%s needs no label: %v", f, err)
		}
	}
	if err := checkGate(gate, []string{approvalLabel}, approver, approver); err != nil {
		t.Errorf("approved by the owner: %v", err)
	}
	if err := checkGate(gate, []string{approvalLabel}, "someone-else", approver); !errors.Is(err, ErrGateGuard) {
		t.Errorf("approved by someone else: %v", err)
	}
	if err := checkGate(gate, []string{approvalLabel}, "anyone", ""); err != nil {
		t.Errorf("label with no approver configured: %v", err)
	}
}

func TestGateGuardCommand(t *testing.T) {
	t.Parallel()
	if _, err := execute(t, "gateguard", "-files", "Taskfile.yml,README.md", "-labels", "bug"); !errors.Is(err, ErrGateGuard) {
		t.Errorf("gate file without the label: %v", err)
	}
	out, err := execute(t, "gateguard", "-files", "Taskfile.yml", "-labels", "bug,"+approvalLabel, "-label-actor", approver, "-approver", approver)
	if err != nil || !strings.Contains(out, "gate guard ok") {
		t.Errorf("approved change: %q, %v", out, err)
	}
	if _, err := execute(t, "gateguard"); !errors.Is(err, ErrUsage) {
		t.Errorf("no input: %v", err)
	}
}

// documentedGateFiles reads the backticked paths of the "Gate-defining files"
// paragraph of quality-gates §10.
func documentedGateFiles(t *testing.T) []string {
	t.Helper()
	doc, err := os.ReadFile("../../docs/quality-gates.md")
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	inList := false
	for _, line := range strings.Split(string(doc), "\n") {
		switch {
		case strings.HasPrefix(line, "Gate-defining files:"):
			inList = true
		case strings.TrimSpace(line) == "":
			inList = false
		}
		if inList {
			for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(line, -1) {
				listed = append(listed, m[1])
			}
		}
	}
	return listed
}

// ownedBy reports whether a CODEOWNERS entry for owner covers path.
func ownedBy(codeowners, path, owner string) bool {
	for _, line := range strings.Split(codeowners, "\n") {
		entry, who, ok := strings.Cut(line, " ")
		if ok && who == owner && strings.HasPrefix(entry, "/") && strings.HasPrefix(path, entry) {
			return true
		}
	}
	return false
}

// The guard must agree with the document that defines it and with CODEOWNERS.
func TestGateFilesMatchTheDocumentAndCodeowners(t *testing.T) {
	t.Parallel()
	if diff := cmp.Diff(slices.Sorted(slices.Values(documentedGateFiles(t))), slices.Sorted(slices.Values(gateFiles()))); diff != "" {
		t.Fatalf("gateFiles() and quality-gates §10 differ (-doc +code):\n%s", diff)
	}
	owners, err := os.ReadFile("../../.github/CODEOWNERS")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gateFiles() {
		if path := "/" + strings.TrimSuffix(g, "**"); !ownedBy(string(owners), path, "@"+approver) {
			t.Errorf("CODEOWNERS has no entry covering %s", path)
		}
	}
}
