package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// changeScope is what a pull request's changed files require of the CI test
// jobs (quality-gates §9): code changes run the Linux tests, coverage and
// short fuzzing; platform changes also run the macOS tests.
type changeScope struct {
	code     bool
	platform bool
}

// everything is the fail-safe scope: when the change set cannot be read, every
// job runs.
func everything() changeScope { return changeScope{code: true, platform: true} }

// platformTrees are the trees whose code is platform-specific even where a
// file carries no build constraint: process trees, sockets and peer
// credentials, the data-directory lock, sandbox backends, shadow git and the
// entry points.
func platformTrees() []string {
	return []string{
		"cmd/", "internal/process/", "internal/hostapi/", "internal/storage/",
		"internal/sandbox/", "internal/snapshot/", ".github/workflows/",
	}
}

// platformFiles are the files that change how every platform builds or runs.
func platformFiles() []string {
	return []string{"go.mod", "go.sum", "Taskfile.yml"}
}

// goosSuffixes are the file-name suffixes Go uses for platform-specific files,
// plus the `_other` and `_unix` names this repository uses for the remainder.
func goosSuffixes() []string {
	return []string{
		"_darwin", "_linux", "_windows", "_freebsd", "_openbsd", "_netbsd",
		"_dragonfly", "_solaris", "_illumos", "_aix", "_unix", "_other", "_bsd",
	}
}

// isDocsOnly reports whether a changed file is documentation no test reads.
// Markdown under testdata is a fixture, and docs/quality-gates.md is read by
// the fuguegate tests, so both count as code. The arch job, which reads the
// roadmap and the ADRs, runs on every pull request regardless.
func isDocsOnly(file string) bool {
	if !strings.HasSuffix(file, ".md") || file == "docs/quality-gates.md" {
		return false
	}
	return !strings.HasPrefix(file, "testdata/") && !strings.Contains(file, "/testdata/")
}

// isPlatformPath reports whether a changed path is platform-specific by its
// location or name alone.
func isPlatformPath(file string) bool {
	for _, tree := range platformTrees() {
		if strings.HasPrefix(file, tree) {
			return true
		}
	}
	for _, f := range platformFiles() {
		if file == f {
			return true
		}
	}
	if path.Ext(file) != ".go" {
		return false
	}
	base := strings.TrimSuffix(strings.TrimSuffix(path.Base(file), ".go"), "_test")
	for _, suffix := range goosSuffixes() {
		if strings.HasSuffix(base, suffix) || strings.Contains(base, suffix+"_") {
			return true
		}
	}
	return false
}

// hasBuildConstraint reports whether a Go file in dir carries a //go:build
// line before its package clause. A file that cannot be read (deleted in the
// change, or unreadable) counts as constrained: the fail-safe answer runs the
// macOS tests.
func hasBuildConstraint(dir, file string) bool {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return true
	}
	data, readErr := root.ReadFile(filepath.FromSlash(file))
	if closeErr := root.Close(); readErr != nil || closeErr != nil {
		return true
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "//go:build ") {
			return true
		}
		if strings.HasPrefix(line, "package ") {
			return false
		}
	}
	return false
}

// classifyChanges decides which test jobs a change set needs. An empty set is
// treated as unknown and runs everything.
func classifyChanges(dir string, changed []string) changeScope {
	var scope changeScope
	seen := false
	for _, raw := range changed {
		file := strings.TrimSpace(raw)
		if file == "" {
			continue
		}
		seen = true
		if isDocsOnly(file) {
			continue
		}
		scope.code = true
		if isPlatformPath(file) || (path.Ext(file) == ".go" && hasBuildConstraint(dir, file)) {
			scope.platform = true
		}
	}
	if !seen {
		return everything()
	}
	return scope
}

// runChanges prints the change scope as GitHub Actions step outputs. It never
// fails on a diff it cannot compute: it prints the fail-safe scope instead,
// so a broken diff can only make CI run more, never less.
func runChanges(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("changes")
	base := fs.String("base", "", "base commit; changed files are base...head")
	head := fs.String("head", "HEAD", "head commit")
	files := fs.String("files", "", "comma-separated changed files, instead of -base and -head")
	dir := fs.String("dir", ".", "repository directory")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	scope := everything()
	switch {
	case *files != "":
		scope = classifyChanges(*dir, strings.Split(*files, ","))
	case *base != "":
		if out, err := capture(ctx, *dir, "git", "diff", "--name-only", *base+"..."+*head); err == nil {
			scope = classifyChanges(*dir, strings.Split(strings.TrimSpace(out), "\n"))
		}
	default:
		return fmt.Errorf("%w: changes needs -files or -base", ErrUsage)
	}
	if err := say(stdout, "code=%t", scope.code); err != nil {
		return err
	}
	return say(stdout, "platform=%t", scope.platform)
}
