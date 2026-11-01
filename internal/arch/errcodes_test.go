package arch

import (
	"fmt"
	"go/ast"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	errorCodesFile = "docs/error-codes.md"
	errcodeDir     = "internal/platform/errcode"
)

// declaredCodes lists the string constants of type Code in the errcode package.
func (r repo) declaredCodes() ([]string, error) {
	files, err := r.files(".go")
	if err != nil {
		return nil, err
	}
	var codes []string
	for _, file := range files {
		if !strings.HasPrefix(file, errcodeDir+"/") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := r.parse(file)
		if err != nil {
			return nil, err
		}
		codes = append(codes, codeConstants(f)...)
	}
	return codes, nil
}

func codeConstants(f *ast.File) []string {
	var codes []string
	for _, decl := range f.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
			codes = append(codes, specCodes(gen)...)
		}
	}
	return codes
}

func specCodes(gen *ast.GenDecl) []string {
	var codes []string
	for _, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok || !isCodeType(vs.Type) {
			continue
		}
		for _, v := range vs.Values {
			if s, ok := stringValue(v); ok {
				codes = append(codes, s)
			}
		}
	}
	return codes
}

func stringValue(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

func isCodeType(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "Code"
}

// checkErrorCodes enforces TestErrorCodesPublished: every code is either in the
// public registry or in the hidden list with a reason, never both, and the
// registry names no code that does not exist.
func checkErrorCodes(r repo) ([]string, error) {
	public, err := r.table(errorCodesFile, "| Code | Meaning")
	if err != nil {
		return nil, err
	}
	hidden, err := r.table(errorCodesFile, "| Code | Reason")
	if err != nil {
		return nil, err
	}
	declared, err := r.declaredCodes()
	if err != nil {
		return nil, err
	}
	return codeProblems(declared, firstCells(public), hidden), nil
}

// codeProblems compares the declared codes with the public registry and the
// hidden rows (code, reason).
func codeProblems(declared, pub []string, hidden [][]string) []string {
	hid := firstCells(hidden)
	var out []string
	for _, code := range declared {
		inPub, inHid := slices.Contains(pub, code), slices.Contains(hid, code)
		switch {
		case inPub && inHid:
			out = append(out, fmt.Sprintf("code %s is in both the public registry and the hidden list", code))
		case !inPub && !inHid:
			out = append(out, fmt.Sprintf("code %s is declared in %s but neither published nor hidden in %s", code, errcodeDir, errorCodesFile))
		}
	}
	for _, row := range hidden {
		if len(row) < 2 || row[1] == "" {
			out = append(out, "hidden code "+row[0]+" has no reason")
		}
	}
	for _, code := range slices.Concat(pub, hid) {
		if !slices.Contains(declared, code) {
			out = append(out, fmt.Sprintf("%s lists code %s, which is not declared in %s", errorCodesFile, code, errcodeDir))
		}
	}
	slices.Sort(out)
	return out
}

func firstCells(rows [][]string) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row[0])
	}
	return out
}

func TestErrorCodesPublished(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkErrorCodes(repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkErrorCodes(fixture("errcodes-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkErrorCodes(fixture("errcodes-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"code forgotten is declared in internal/platform/errcode but neither published nor hidden",
			"code both is in both the public registry and the hidden list",
			"hidden code secret_detail has no reason",
			"lists code stale, which is not declared",
		)
	})
}
