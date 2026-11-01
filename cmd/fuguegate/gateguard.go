package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// ErrGateGuard reports a change to a gate-defining file without the approval label.
var ErrGateGuard = errors.New("gate guard failed")

const approvalLabel = "gate-change-approved"

// gateFiles are the gate-defining files and trees of quality-gates §10; a
// trailing "/**" means everything below.
func gateFiles() []string {
	return []string{
		".golangci.yml", "tools/fuguevet/**", "internal/arch/**", "quality/**",
		"docs/quality-gates.md", "docs/dependencies.md", ".github/workflows/**", "Taskfile.yml",
	}
}

func isGateFile(path string) bool {
	for _, g := range gateFiles() {
		if tree, ok := strings.CutSuffix(g, "/**"); ok {
			if strings.HasPrefix(path, tree+"/") {
				return true
			}
		} else if path == g {
			return true
		}
	}
	return false
}

func runGateGuard(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("gateguard")
	base := fs.String("base", "", "base ref; changed files are base...head")
	head := fs.String("head", "HEAD", "head ref")
	files := fs.String("files", "", "comma-separated changed files, instead of -base and -head")
	labels := fs.String("labels", "", "comma-separated labels on the PR")
	labeler := fs.String("label-actor", "", "login that applied the approval label")
	approver := fs.String("approver", "", "login that alone may apply the approval label")
	dir := fs.String("dir", ".", "repository directory")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	changed := strings.Split(*files, ",")
	if *files == "" {
		if *base == "" {
			return fmt.Errorf("%w: gateguard needs -files or -base", ErrUsage)
		}
		out, err := capture(ctx, *dir, "git", "diff", "--name-only", *base+"..."+*head)
		if err != nil {
			return err
		}
		changed = strings.Split(strings.TrimSpace(out), "\n")
	}
	if err := checkGate(changed, strings.Split(*labels, ","), *labeler, *approver); err != nil {
		return err
	}
	return say(stdout, "gate guard ok")
}

func checkGate(changed, labels []string, labeler, approver string) error {
	var touched []string
	for _, f := range changed {
		if isGateFile(strings.TrimSpace(f)) {
			touched = append(touched, f)
		}
	}
	if len(touched) == 0 {
		return nil
	}
	if !slices.Contains(labels, approvalLabel) {
		return fmt.Errorf("%w: gate-defining files changed without the %s label:\n%s", ErrGateGuard, approvalLabel, strings.Join(touched, "\n"))
	}
	if approver != "" && labeler != approver {
		return fmt.Errorf("%w: the %s label was applied by %q; only %q may apply it", ErrGateGuard, approvalLabel, labeler, approver)
	}
	return nil
}
