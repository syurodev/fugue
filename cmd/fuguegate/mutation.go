package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	mutationThreshold = 85.0
	timeoutFactor     = "50"
)

// Errors reported by the mutation gate.
var (
	ErrMutation       = errors.New("mutation gate failed")
	ErrMutationOutput = errors.New("unreadable gremlins output")

	killedPattern   = regexp.MustCompile(`Killed: (\d+), Lived: (\d+), Not covered: (\d+)`)
	timedOutPattern = regexp.MustCompile(`Timed out: (\d+)`)
)

func runMutation(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("mutation")
	dir := fs.String("dir", ".", "module directory")
	base := fs.String("base", "origin/main", "ref to diff against; only changed packages are mutated")
	all := fs.Bool("all", false, "mutate every guarded package (nightly)")
	explicit := fs.String("packages", "", "comma-separated package directories, overriding -base and -all")
	budget := fs.Duration("budget", 20*time.Minute, "time box for discovery and each selected package")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	preparationCtx, cancel := context.WithTimeout(ctx, *budget)
	defer cancel()

	targets, err := mutationPackages(preparationCtx, *dir, *base, *all, *explicit)
	if err != nil {
		return err
	}
	bin, err := toolPath(preparationCtx, "gremlins")
	cancel()
	if err != nil {
		return err
	}
	for _, pkg := range targets {
		out, err := mutatePackage(ctx, *dir, bin, pkg, *budget)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrMutation, pkg, err)
		}
		score, err := mutationScore(out)
		if err != nil {
			return fmt.Errorf("%s: %w", pkg, err)
		}
		if score < mutationThreshold {
			return fmt.Errorf("%w: %s scored %.1f%%, need %.0f%%\n%s", ErrMutation, pkg, score, mutationThreshold, out)
		}
		if err := say(stdout, "%s: mutation score %.1f%%", pkg, score); err != nil {
			return err
		}
	}
	return say(stdout, "mutation ok: %d packages", len(targets))
}

// Each package gets the full budget; a slow predecessor must not consume it.
func mutatePackage(ctx context.Context, dir, bin, pkg string, budget time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return capture(ctx, dir, bin, "unleash", "--timeout-coefficient", timeoutFactor, "./"+pkg)
}

func mutationPackages(ctx context.Context, dir, base string, all bool, explicit string) ([]string, error) {
	switch {
	case explicit != "":
		return strings.Split(explicit, ","), nil
	case all:
		return guardedPackages(), nil
	}
	mergeBase, err := capture(ctx, dir, "git", "merge-base", "HEAD", base)
	if err != nil {
		return nil, err
	}
	changed, err := capture(ctx, dir, "git", "diff", "--name-only", strings.TrimSpace(mergeBase))
	if err != nil {
		return nil, err
	}
	fresh, err := capture(ctx, dir, "git", "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return changedGuarded(strings.Split(changed+fresh, "\n")), nil
}

func guardedPackages() []string {
	return []string{
		"internal/kernel", "internal/codec", "internal/execstore",
		"internal/coordinator", "internal/crypto", "internal/vault",
	}
}

// changedGuarded maps changed file paths to the guarded packages they belong to.
func changedGuarded(files []string) []string {
	var out []string
	for _, file := range files {
		pkg := path.Dir(file)
		if slices.Contains(guardedPackages(), pkg) && strings.HasSuffix(file, ".go") && !slices.Contains(out, pkg) {
			out = append(out, pkg)
		}
	}
	slices.Sort(out)
	return out
}

// mutationScore is killed mutants over every mutant that is not equivalent or
// unviable. Timed-out and uncovered mutants count against the score. gremlins
// applies its own threshold flag unreliably (a 0% run exits 0), so the gate
// reads the summary instead of trusting the exit code.
func mutationScore(out string) (float64, error) {
	m := killedPattern.FindStringSubmatch(out)
	t := timedOutPattern.FindStringSubmatch(out)
	if m == nil || t == nil {
		return 0, ErrMutationOutput
	}
	var n [4]int
	for i, text := range []string{m[1], m[2], m[3], t[1]} {
		v, err := strconv.Atoi(text)
		if err != nil {
			return 0, fmt.Errorf("%w: %w", ErrMutationOutput, err)
		}
		n[i] = v
	}
	total := n[0] + n[1] + n[2] + n[3]
	if total == 0 {
		return 100, nil
	}
	return 100 * float64(n[0]) / float64(total), nil
}
