package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrProto reports a proto gate failure: lint, format, a breaking change or
// generated code that is out of date.
var ErrProto = errors.New("proto gate failed")

type protoGate struct {
	root      string
	protoDir  string
	template  string
	against   string
	base      string
	generated []string
	buf       string
}

func runProto(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("proto")
	root := fs.String("root", ".", "repository root holding proto/ and the generated directories")
	template := fs.String("template", "proto/buf.gen.yaml", "buf generation template, relative to root")
	against := fs.String("against", "", "buf input to compare against, instead of -base (a directory holding the previous module)")
	base := fs.String("base", "origin/main", "git ref whose proto/ the breaking check compares with")
	generated := fs.String("generated", "internal/hostapi/internal/pb,gen/ts", "comma-separated generated directories, relative to root")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	buf, err := toolPath(ctx, "buf")
	if err != nil {
		return err
	}
	g := &protoGate{
		root: abs, protoDir: filepath.Join(abs, "proto"), template: filepath.Join(abs, *template),
		against: *against, base: *base, generated: strings.Split(*generated, ","), buf: buf,
	}
	if err := g.verify(ctx, stdout); err != nil {
		return err
	}
	return say(stdout, "proto ok")
}

func (g *protoGate) verify(ctx context.Context, stdout io.Writer) error {
	if _, err := capture(ctx, ".", g.buf, "lint", g.protoDir); err != nil {
		return fmt.Errorf("%w: lint: %w", ErrProto, err)
	}
	if _, err := capture(ctx, ".", g.buf, "format", "-d", "--exit-code", g.protoDir); err != nil {
		return fmt.Errorf("%w: format: %w", ErrProto, err)
	}
	if err := g.breaking(ctx, stdout); err != nil {
		return err
	}
	return g.stale(ctx)
}

// breaking compares with the base branch. On the first change that adds proto/
// there is nothing to compare with, which is said out loud rather than skipped
// silently.
func (g *protoGate) breaking(ctx context.Context, stdout io.Writer) error {
	against := g.against
	if against == "" {
		if _, err := capture(ctx, g.root, "git", "cat-file", "-e", g.base+":proto/buf.yaml"); err != nil {
			return say(stdout, "breaking check skipped: %s has no proto/ yet", g.base)
		}
		// ref=, not branch=: CI checks out a detached merge commit with no local main.
		against = g.root + "/.git#ref=" + g.base + ",subdir=proto"
	}
	if _, err := capture(ctx, ".", g.buf, "breaking", g.protoDir, "--against", against); err != nil {
		return fmt.Errorf("%w: breaking: %w", ErrProto, err)
	}
	return nil
}

// stale regenerates into a temporary directory and compares it with the
// committed generated code, so the check never touches the working tree.
func (g *protoGate) stale(ctx context.Context) (err error) {
	tmp, err := os.MkdirTemp("", "fuguegate-proto-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(tmp); rmErr != nil && err == nil {
			err = fmt.Errorf("remove %s: %w", tmp, rmErr)
		}
	}()
	if _, err := capture(ctx, ".", g.buf, "generate", g.protoDir, "--template", g.template, "--output", tmp); err != nil {
		return fmt.Errorf("%w: generate: %w", ErrProto, err)
	}
	var problems []string
	for _, dir := range g.generated {
		diff, err := compareTrees(filepath.Join(tmp, dir), filepath.Join(g.root, dir))
		if err != nil {
			return err
		}
		for _, d := range diff {
			problems = append(problems, dir+"/"+d)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: generated code is out of date (run `task generate`):\n%s", ErrProto, strings.Join(problems, "\n"))
	}
	return nil
}

// compareTrees lists files that differ, are missing from have, or are extra in have.
func compareTrees(want, have string) ([]string, error) {
	w, err := hashTree(want)
	if err != nil {
		return nil, err
	}
	h, err := hashTree(have)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(w)) {
		switch got, ok := h[name]; {
		case !ok:
			out = append(out, name+": missing, regenerate")
		case got != w[name]:
			out = append(out, name+": differs from generated output")
		}
	}
	for _, name := range slices.Sorted(maps.Keys(h)) {
		if _, ok := w[name]; !ok {
			out = append(out, name+": not produced by the generators, delete it")
		}
	}
	return out, nil
}

func hashTree(dir string) (map[string][sha256.Size]byte, error) {
	out := map[string][sha256.Size]byte{}
	tree := os.DirFS(dir)
	err := fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(tree, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		out[p] = sha256.Sum256(data)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	return out, nil
}
