package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func protoArgs(root string, extra ...string) []string {
	return append([]string{"proto", "-root", "testdata/" + root, "-generated", "gen/go"}, extra...)
}

// Each step of the proto gate has a violating fixture; proto-fresh is the clean
// twin that passes every step, including generated code that matches.
func TestProtoGate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"lint", protoArgs("proto-lint-bad"), "lint"},
		{"format", protoArgs("proto-format-bad"), "format"},
		{"breaking change", protoArgs("proto-breaking-after", "-against", "testdata/proto-breaking-before/proto"), "was deleted"},
		{"stale generated code", protoArgs("proto-stale", "-against", "testdata/proto-stale/proto"), "demo.pb.go: differs"},
		{"missing generated file", protoArgs("proto-stale", "-against", "testdata/proto-stale/proto"), "other.pb.go: missing"},
		{"leftover generated file", protoArgs("proto-stale", "-against", "testdata/proto-stale/proto"), "leftover.go: not produced"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := execute(t, c.args...)
			if !errors.Is(err, ErrProto) || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want ErrProto mentioning %q", err, c.wantErr)
			}
		})
	}
	out, err := execute(t, protoArgs("proto-fresh", "-against", "testdata/proto-fresh/proto")...)
	if err != nil || !strings.Contains(out, "proto ok") {
		t.Fatalf("clean fixture: %q, %v", out, err)
	}
}

func TestProtoBreakingIsSkippedOnlyWhenBaseHasNoProto(t *testing.T) {
	t.Parallel()
	out, err := execute(t, protoArgs("proto-fresh", "-base", "origin/does-not-exist")...)
	if err != nil || !strings.Contains(out, "breaking check skipped") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

// A field main gained after the PR branched is not a deletion by the PR: the
// breaking check compares with the merge-base, not with the base's tip.
func TestProtoBreakingComparesWithTheForkPoint(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)
		if out, err := capture(ctx, repo, "git", full...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	put := func(fixture string) {
		t.Helper()
		if err := os.CopyFS(filepath.Join(repo, "proto"), os.DirFS(filepath.Join("testdata", fixture, "proto"))); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "feature")
	put("proto-breaking-after")
	git("add", ".")
	git("commit", "-q", "-m", "fork point")
	if err := os.RemoveAll(filepath.Join(repo, "proto")); err != nil {
		t.Fatal(err)
	}
	git("checkout", "-q", "-b", "main")
	put("proto-breaking-before")
	git("add", ".")
	git("commit", "-q", "-m", "main adds a field")
	git("update-ref", "refs/remotes/origin/main", "main")
	git("checkout", "-q", "feature")
	buf, err := toolPath(ctx, "buf")
	if err != nil {
		t.Fatal(err)
	}
	g := &protoGate{root: repo, protoDir: filepath.Join(repo, "proto"), base: "origin/main", buf: buf}
	if err := g.breaking(ctx, io.Discard); err != nil {
		t.Fatalf("a field added on main after the fork point counted against the branch: %v", err)
	}
	git("checkout", "-q", "main")
	git("checkout", "-q", "-b", "drops-the-field", "HEAD")
	if err := os.RemoveAll(filepath.Join(repo, "proto")); err != nil {
		t.Fatal(err)
	}
	put("proto-breaking-after")
	git("add", "-A")
	git("commit", "-q", "-m", "branch deletes the field")
	if err := g.breaking(ctx, io.Discard); !errors.Is(err, ErrProto) || !strings.Contains(err.Error(), "was deleted") {
		t.Fatalf("a field the branch itself deleted: err = %v, want a breaking failure", err)
	}
}

// A local `task proto` must work without the network: the template it runs
// names no remote plugin, repeats the local plugins of the full template, and
// the gate passes with it on the repository's own proto/.
func TestProtoGateNeedsNoNetwork(t *testing.T) {
	t.Parallel()
	repo := os.DirFS(filepath.Join("..", ".."))
	read := func(name string) string {
		t.Helper()
		data, err := fs.ReadFile(repo, name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	checkLocalTemplate(t, read("proto/buf.gen.local.yaml"), read("proto/buf.gen.yaml"))
	if !strings.Contains(read("Taskfile.yml"), `{{if not (env "CI")}} -template proto/buf.gen.local.yaml -generated pkg/pb{{end}}`) {
		t.Error("task proto does not use the local template outside CI")
	}
	out, err := execute(t, "proto", "-root", "../..", "-template", "proto/buf.gen.local.yaml",
		"-generated", "pkg/pb", "-against", "../../proto")
	if err != nil || !strings.Contains(out, "proto ok") {
		t.Fatalf("local gate: %q, %v", out, err)
	}
}

func checkLocalTemplate(t *testing.T, local, full string) {
	t.Helper()
	if strings.Contains(local, "remote:") {
		t.Error("buf.gen.local.yaml names a remote plugin, which needs the network")
	}
	if !strings.Contains(full, "remote:") {
		t.Error("buf.gen.yaml no longer has a remote plugin; drop the local template")
	}
	for line := range strings.Lines(local) {
		line = strings.TrimRight(line, "\n")
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.Contains(full, line) {
			t.Errorf("buf.gen.local.yaml line %q is not in buf.gen.yaml", line)
		}
	}
}
