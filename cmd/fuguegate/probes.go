package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const probeHeaderPrefix = "# probe-test: "

// ErrProbes reports an invalid inventory or a probe that its named test does
// not kill.
var ErrProbes = errors.New("probe gate failed")

var probeTestPattern = regexp.MustCompile(`^Test[[:alnum:]_]+$`)

type probeSpec struct {
	patch   string
	targets []probeTarget
}

type probeTarget struct {
	pkg  string
	test string
}

type probeTestRun func(context.Context, string, probeTarget, time.Duration) (string, error)

func runProbes(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("probes")
	dir := fs.String("dir", ".", "module directory")
	inventory := fs.String("inventory", "quality/probes", "directory containing probe patches")
	budget := fs.Duration("budget", 10*time.Minute, "time box for each baseline and patched test")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if fs.NArg() != 0 || *budget <= 0 {
		return fmt.Errorf("%w: probes expects flags and a positive budget", ErrUsage)
	}
	probes, err := discoverProbes(*dir, *inventory)
	if err != nil {
		return err
	}
	for _, probe := range probes {
		if err := runProbe(ctx, *dir, probe, *budget, runProbeTest); err != nil {
			return err
		}
		if err := say(stdout, "%s: killed by all %d declared tests", filepath.Base(probe.patch), len(probe.targets)); err != nil {
			return err
		}
	}
	return say(stdout, "probes ok: %d patches", len(probes))
}

func discoverProbes(dir, inventory string) ([]probeSpec, error) {
	pattern := filepath.Join(dir, filepath.FromSlash(inventory), "*.patch")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: inventory pattern: %w", ErrProbes, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: missing inventory: no files match %s", ErrProbes, filepath.ToSlash(filepath.Join(inventory, "*.patch")))
	}
	probes := make([]probeSpec, 0, len(files))
	for _, file := range files {
		probe, err := readProbe(file)
		if err != nil {
			return nil, err
		}
		probes = append(probes, probe)
	}
	return probes, nil
}

func readProbe(file string) (probeSpec, error) {
	absolute, err := filepath.Abs(file)
	if err != nil {
		return probeSpec{}, fmt.Errorf("%w: resolve %s: %w", ErrProbes, file, err)
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return probeSpec{}, fmt.Errorf("%w: read %s: %w", ErrProbes, file, err)
	}
	targets, err := parseProbeTargets(string(data))
	if err != nil {
		return probeSpec{}, fmt.Errorf("%s: %w", file, err)
	}
	return probeSpec{patch: absolute, targets: targets}, nil
}

func parseProbeTargets(data string) ([]probeTarget, error) {
	var targets []probeTarget
	seen := map[probeTarget]bool{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSuffix(line, "\r")
		value, ok := strings.CutPrefix(line, probeHeaderPrefix)
		if !ok {
			break
		}
		fields := strings.Fields(value)
		if len(fields) != 2 || !validProbePackage(fields[0]) || !probeTestPattern.MatchString(fields[1]) {
			return nil, fmt.Errorf("%w: expected a header of the form %q", ErrProbes, "# probe-test: ./package TestName")
		}
		target := probeTarget{pkg: fields[0], test: fields[1]}
		if seen[target] {
			return nil, fmt.Errorf("%w: duplicate target %s %s", ErrProbes, target.pkg, target.test)
		}
		seen[target] = true
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%w: missing probe-test header", ErrProbes)
	}
	return targets, nil
}

func validProbePackage(pkg string) bool {
	rel := strings.TrimPrefix(pkg, "./")
	return strings.HasPrefix(pkg, "./") && rel != "" && rel != "." && filepath.IsLocal(rel) && !strings.HasPrefix(rel, "/") &&
		!strings.Contains(rel, "\\") && path.Clean(rel) == rel && !strings.Contains(rel, "...")
}

func runProbe(ctx context.Context, dir string, probe probeSpec, budget time.Duration, test probeTestRun) (retErr error) {
	snapshot, err := snapshotRepository(ctx, dir)
	if err != nil {
		return fmt.Errorf("%w: %s snapshot: %w", ErrProbes, filepath.Base(probe.patch), err)
	}
	defer func() {
		if err := os.RemoveAll(snapshot); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove probe snapshot: %w", err))
		}
	}()

	if err := applyProbe(ctx, snapshot, probe.patch, "--check"); err != nil {
		return err
	}
	if err := checkProbeTargets(ctx, snapshot, probe, budget, test, "pass"); err != nil {
		return err
	}
	if err := applyProbe(ctx, snapshot, probe.patch); err != nil {
		return err
	}
	return checkProbeTargets(ctx, snapshot, probe, budget, test, "fail")
}

func applyProbe(ctx context.Context, snapshot, patch string, flags ...string) error {
	args := append([]string{"apply", "--whitespace=nowarn"}, flags...)
	if _, _, err := runCommand(ctx, snapshot, "git", append(args, patch)...); err != nil {
		return fmt.Errorf("%w: %s does not apply: %w", ErrProbes, filepath.Base(patch), err)
	}
	return nil
}

func checkProbeTargets(ctx context.Context, snapshot string, probe probeSpec, budget time.Duration, test probeTestRun, want string) error {
	for _, target := range probe.targets {
		log, commandErr := test(ctx, snapshot, target, budget)
		if !probeOutcome(log, target.test, want, commandErr) {
			return fmt.Errorf("%w: %s expected %s from %s %s", ErrProbes, filepath.Base(probe.patch), want, target.pkg, target.test)
		}
	}
	return nil
}

func runProbeTest(ctx context.Context, dir string, target probeTarget, budget time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, budget+time.Second)
	defer cancel()
	stdout, _, err := runCommand(ctx, dir, "go", "test", "-json", "-race", "-shuffle=on", "-count=1", "-timeout="+budget.String(),
		"-run", "^"+regexp.QuoteMeta(target.test)+"$", target.pkg)
	if ctx.Err() != nil {
		return "", fmt.Errorf("probe test interrupted: %w", ctx.Err())
	}
	return stdout, err
}

func probeOutcome(log, name, want string, commandErr error) bool {
	if want == "fail" {
		var exit *exec.ExitError
		if !errors.As(commandErr, &exit) || exit.ProcessState == nil || exit.ExitCode() != 1 {
			return false
		}
	} else if commandErr != nil {
		return false
	}

	var named, packageResult string
	var namedCount, packageCount int
	err := eachEvent(strings.NewReader(log), func(event testEvent) {
		if event.Action != "pass" && event.Action != "fail" {
			return
		}
		switch event.Test {
		case name:
			if event.Action == want {
				named = event.Package
			}
			namedCount++
		case "":
			if event.Action == want {
				packageResult = event.Package
			}
			packageCount++
		}
	})
	return err == nil && namedCount == 1 && packageCount == 1 && named != "" && named == packageResult
}

func snapshotRepository(ctx context.Context, dir string) (string, error) {
	listed, err := capture(ctx, dir, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return "", err
	}
	snapshot, err := os.MkdirTemp("", "fugue-probes-")
	if err != nil {
		return "", fmt.Errorf("create temporary directory: %w", err)
	}
	for _, rel := range strings.Split(strings.TrimSuffix(listed, "\x00"), "\x00") {
		if rel == "" {
			continue
		}
		if !filepath.IsLocal(rel) {
			_ = os.RemoveAll(snapshot)
			return "", fmt.Errorf("repository path is not local: %q", rel)
		}
		if err := copyProbeFile(dir, snapshot, rel); err != nil {
			_ = os.RemoveAll(snapshot)
			return "", err
		}
	}
	return snapshot, nil
}

func copyProbeFile(sourceRoot, targetRoot, rel string) error {
	source := filepath.Join(sourceRoot, filepath.FromSlash(rel))
	target := filepath.Join(targetRoot, filepath.FromSlash(rel))
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("stat %s: %w", rel, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("create parent for %s: %w", rel, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(source)
		if err != nil {
			return fmt.Errorf("read link %s: %w", rel, err)
		}
		if err := os.Symlink(link, target); err != nil {
			return fmt.Errorf("copy link %s: %w", rel, err)
		}
		return nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}
