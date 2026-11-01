package arch

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	sourceLimit = 500
	testLimit   = 800
	ratchetFile = "quality/ratchet.json"
)

var (
	nolintDirective = regexp.MustCompile(`^//\s*nolint\b`)
	// generatedHeader is the marker Go tooling uses for generated files, which are
	// measured by the generator, not by a line limit.
	generatedHeader = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)
)

type ratchet struct {
	Nolint             int `json:"nolint"`
	Gosec              int `json:"gosec"`
	FileSizeExceptions int `json:"fileSizeExceptions"`
}

func (r repo) ratchet() (ratchet, error) {
	data, err := r.read(ratchetFile)
	if err != nil {
		return ratchet{}, err
	}
	var rt ratchet
	if err := json.Unmarshal([]byte(data), &rt); err != nil {
		return ratchet{}, fmt.Errorf("decode %s: %w", ratchetFile, err)
	}
	return rt, nil
}

// oversize lists Go files above the line limits.
func (r repo) oversize() ([]string, error) {
	files, err := r.files(".go")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, file := range files {
		src, err := r.read(file)
		if err != nil {
			return nil, err
		}
		if generatedHeader.MatchString(src) {
			continue
		}
		lines, limit := strings.Count(src, "\n"), sourceLimit
		if strings.HasSuffix(file, "_test.go") {
			limit = testLimit
		}
		if lines > limit {
			out = append(out, fmt.Sprintf("%s has %d lines, limit %d", file, lines, limit))
		}
	}
	return out, nil
}

// checkFileSize enforces TestFileSize: oversize files are tolerated only up to
// the number recorded in the ratchet.
func checkFileSize(r repo) ([]string, error) {
	rt, err := r.ratchet()
	if err != nil {
		return nil, err
	}
	big, err := r.oversize()
	if err != nil {
		return nil, err
	}
	if len(big) > rt.FileSizeExceptions {
		return big, nil
	}
	return nil, nil
}

// directiveCounts counts nolint directives and, among them, the gosec ones.
func (r repo) directiveCounts() (nolint, gosec int, err error) {
	files, err := r.files(".go")
	if err != nil {
		return 0, 0, err
	}
	for _, file := range files {
		f, err := r.parse(file)
		if err != nil {
			return 0, 0, err
		}
		n, g := fileDirectives(f)
		nolint, gosec = nolint+n, gosec+g
	}
	return nolint, gosec, nil
}

func fileDirectives(f *ast.File) (nolint, gosec int) {
	for _, group := range f.Comments {
		for _, c := range group.List {
			if !nolintDirective.MatchString(c.Text) {
				continue
			}
			nolint++
			if strings.Contains(c.Text, "gosec") {
				gosec++
			}
		}
	}
	return nolint, gosec
}

// checkRatchets enforces quality-gates section 8: the counts never grow, and
// when one shrinks the baseline must be lowered in the same change.
func checkRatchets(r repo) ([]string, error) {
	rt, err := r.ratchet()
	if err != nil {
		return nil, err
	}
	nolint, gosec, err := r.directiveCounts()
	if err != nil {
		return nil, err
	}
	big, err := r.oversize()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range []struct {
		name         string
		actual, base int
	}{
		{"nolint directives", nolint, rt.Nolint},
		{"gosec exceptions", gosec, rt.Gosec},
		{"file-size exceptions", len(big), rt.FileSizeExceptions},
	} {
		switch {
		case c.actual > c.base:
			out = append(out, fmt.Sprintf("%s grew to %d, baseline is %d", c.name, c.actual, c.base))
		case c.actual < c.base:
			out = append(out, fmt.Sprintf("%s fell to %d: lower the baseline in %s from %d", c.name, c.actual, ratchetFile, c.base))
		}
	}
	return out, nil
}

// checkPlaceholders enforces TestNoPlaceholderCode.
func checkPlaceholders(r repo) ([]string, error) {
	files, err := r.files(".go")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := r.parse(file)
		if err != nil {
			return nil, err
		}
		if ast.IsGenerated(f) {
			continue
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil && placeholder(fn) != "" {
				out = append(out, fmt.Sprintf("%s: %s is a placeholder: %s", file, fn.Name.Name, placeholder(fn)))
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func isNop(fn *ast.FuncDecl) bool {
	name := fn.Name.Name
	if fn.Recv != nil && len(fn.Recv.List) == 1 {
		name = receiverName(fn.Recv.List[0].Type)
	}
	return strings.HasPrefix(name, "Nop") || strings.HasPrefix(name, "nop") ||
		strings.HasPrefix(fn.Name.Name, "Nop") || strings.HasPrefix(fn.Name.Name, "nop")
}

func receiverName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.StarExpr:
		return receiverName(x.X)
	case *ast.IndexExpr:
		return receiverName(x.X)
	case *ast.Ident:
		return x.Name
	default:
		return ""
	}
}

func placeholder(fn *ast.FuncDecl) string {
	if isNop(fn) {
		return ""
	}
	if len(fn.Body.List) == 0 {
		return "empty body"
	}
	if len(fn.Body.List) != 1 {
		return ""
	}
	switch stmt := fn.Body.List[0].(type) {
	case *ast.ReturnStmt:
		return returnPlaceholder(stmt)
	case *ast.ExprStmt:
		if call, ok := stmt.X.(*ast.CallExpr); ok && isIdent(call.Fun, "panic") && mentionsNotImplemented(call) {
			return "panics with \"not implemented\""
		}
	}
	return ""
}

func returnPlaceholder(ret *ast.ReturnStmt) string {
	const minResults = 2
	if len(ret.Results) >= minResults && !slices.ContainsFunc(ret.Results, func(e ast.Expr) bool { return !isIdent(e, "nil") }) {
		return "returns only nil values"
	}
	for _, res := range ret.Results {
		if call, ok := res.(*ast.CallExpr); ok && mentionsNotImplemented(call) {
			return "returns a \"not implemented\" error"
		}
	}
	return ""
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}

func mentionsNotImplemented(call *ast.CallExpr) bool {
	return slices.ContainsFunc(call.Args, func(a ast.Expr) bool {
		lit, ok := a.(*ast.BasicLit)
		return ok && lit.Kind == token.STRING && strings.Contains(strings.ToLower(lit.Value), "not implemented")
	})
}

func TestFileSize(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkFileSize(repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkFileSize(fixture("size-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkFileSize(fixture("size-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"internal/big/big.go has 501 lines, limit 500",
			"internal/big/big_test.go has 801 lines, limit 800",
		)
	})
}

func TestNoPlaceholderCode(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkPlaceholders(repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkPlaceholders(fixture("placeholders-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkPlaceholders(fixture("placeholders-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"Nothing is a placeholder: returns only nil values",
			"Empty is a placeholder: empty body",
			"Todo is a placeholder: returns a \"not implemented\" error",
			"Wrapped is a placeholder: returns a \"not implemented\" error",
			"Method is a placeholder: empty body",
			"Panics is a placeholder: panics with \"not implemented\"",
		)
	})
}

func TestRatchets(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkRatchets(repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkRatchets(fixture("ratchet-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("a count that grew", func(t *testing.T) {
		t.Parallel()
		got, err := checkRatchets(fixture("ratchet-grew"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got, "nolint directives grew to 3, baseline is 1", "gosec exceptions grew to 1, baseline is 0")
	})
	t.Run("a count that fell", func(t *testing.T) {
		t.Parallel()
		got, err := checkRatchets(fixture("ratchet-fell"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got, "nolint directives fell to 3: lower the baseline in quality/ratchet.json from 5")
	})
}
