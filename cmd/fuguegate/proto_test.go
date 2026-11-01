package main

import (
	"errors"
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
