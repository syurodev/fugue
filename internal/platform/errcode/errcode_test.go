package errcode_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const masked = "internal error"

var errDriver = errors.New("driver: connection refused")

func TestErrorFormatsCodeMessageAndCause(t *testing.T) {
	t.Parallel()
	plain := errcode.New(errcode.NotFound, "session s1")
	if got, want := plain.Error(), "not_found: session s1"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	wrapped := errcode.Wrap(errcode.Unavailable, "open store", errDriver)
	if got, want := wrapped.Error(), "unavailable: open store: driver: connection refused"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(wrapped, errDriver) {
		t.Error("errors.Is must see the cause")
	}
	if wrapped.Code() != errcode.Unavailable || wrapped.Message() != "open store" {
		t.Errorf("accessors: %v %q", wrapped.Code(), wrapped.Message())
	}
}

func TestCodeOf(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		err  error
		want errcode.Code
	}{
		"nil":                {nil, ""},
		"direct":             {errcode.New(errcode.NotFound, "x"), errcode.NotFound},
		"through fmt.Errorf": {fmt.Errorf("layer: %w", errcode.New(errcode.AlreadyExists, "x")), errcode.AlreadyExists},
		"outermost wins":     {errcode.Wrap(errcode.Unavailable, "x", errcode.New(errcode.NotFound, "y")), errcode.Unavailable},
		"canceled":           {fmt.Errorf("wait: %w", context.Canceled), errcode.Canceled},
		"deadline":           {context.DeadlineExceeded, errcode.DeadlineExceeded},
		"uncoded":            {errDriver, errcode.Internal},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := errcode.CodeOf(c.err); got != c.want {
				t.Errorf("CodeOf = %q, want %q", got, c.want)
			}
		})
	}
}

func TestExposeNeverLeaksDetail(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		err      error
		wantCode errcode.Code
		wantMsg  string
	}{
		"public keeps its message": {errcode.Wrap(errcode.NotFound, "session s1", errDriver), errcode.NotFound, "session s1"},
		"hidden code is masked":    {errcode.Wrap(errcode.Internal, "key at /secret/path", errDriver), errcode.Internal, masked},
		"uncoded error is masked":  {errDriver, errcode.Internal, masked},
		"public by context":        {context.Canceled, errcode.Canceled, "canceled"},
		"unknown code is masked":   {errcode.New(errcode.Code("invented"), "x"), errcode.Internal, masked},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			code, msg := errcode.Expose(c.err)
			if code != c.wantCode || msg != c.wantMsg {
				t.Errorf("Expose = %q, %q; want %q, %q", code, msg, c.wantCode, c.wantMsg)
			}
		})
	}
}

// declaredCodes returns the string literal of every constant of type Code in f.
func declaredCodes(f *ast.File) []string {
	var declared []string
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "Code" {
			for _, v := range vs.Values {
				if lit, ok := v.(*ast.BasicLit); ok {
					declared = append(declared, lit.Value)
				}
			}
		}
		return true
	})
	return declared
}

// TestRegistryCoversEveryCode reads this package's own source: every constant of
// type Code must be classified in Registry, so a new code cannot skip it.
func TestRegistryCoversEveryCode(t *testing.T) {
	t.Parallel()
	f, err := parser.ParseFile(token.NewFileSet(), "errcode.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := declaredCodes(f)
	registry := errcode.Registry()
	registered := make([]string, 0, len(registry))
	for _, e := range registry {
		registered = append(registered, fmt.Sprintf("%q", string(e.Code)))
		if e.Note == "" {
			t.Errorf("code %s has no note", e.Code)
		}
	}
	slices.Sort(declared)
	slices.Sort(registered)
	if diff := cmp.Diff(declared, registered); diff != "" {
		t.Fatalf("Registry and declared codes differ (-declared +registered):\n%s", diff)
	}
}
