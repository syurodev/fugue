package arch

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const codexAuthFile = "auth.json"

type credentialPattern struct {
	pattern string
	reason  string
}

// vendorCredentialPatterns lists the vendor credential files, keychain service
// names, and keychain CLI commands banned in non-test Go files by VR-03 and
// VR-E09 (vendor-runtime §4, §12, §15).
func vendorCredentialPatterns() []credentialPattern {
	return []credentialPattern{
		// Claude Code stores OAuth subscription credentials in ~/.claude/.credentials.json.
		{pattern: ".credentials.json", reason: "Claude Code OAuth credential file"},
		// Codex CLI stores subscription login tokens in ~/.codex/auth.json (or $CODEX_HOME/auth.json).
		{pattern: codexAuthFile, reason: "Codex CLI subscription credential file"},
		// Claude Code stores OAuth tokens in the macOS Keychain under this service name.
		{pattern: "Claude Code-credentials", reason: "Claude Code macOS Keychain service name"},
		// macOS Keychain CLI command used to extract generic passwords by service or account.
		{pattern: "security find-generic-password", reason: "macOS Keychain CLI credential lookup"},
	}
}

// checkNoVendorCredentialAccess enforces VR-E09 (vendor-runtime §12, §15):
// no non-test Go file outside testdata may reference vendor subscription
// credential files, vendor keychain service names, or keychain CLI lookups.
func checkNoVendorCredentialAccess(ctx context.Context, r repo) ([]string, error) {
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pkgs {
		// Only non-test GoFiles are checked; testdata is skipped by go list.
		for _, f := range p.GoFiles {
			rel := filepath.ToSlash(filepath.Join(p.rel(), f))
			found, ferr := fileReferencesVendorCredentials(r, rel)
			if ferr != nil {
				return nil, ferr
			}
			out = append(out, found...)
		}
	}
	slices.Sort(out)
	return out, nil
}

func fileReferencesVendorCredentials(r repo, rel string) ([]string, error) {
	file, err := r.parse(rel)
	if err != nil {
		return nil, err
	}
	patterns := vendorCredentialPatterns()
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val, uerr := strconv.Unquote(lit.Value)
		if uerr != nil {
			val = lit.Value
		}
		for _, cp := range patterns {
			if matchesCredentialPattern(val, cp.pattern) {
				out = append(out, fmt.Sprintf("%s references vendor credential store %q (%s)", rel, cp.pattern, cp.reason))
			}
		}
		return true
	})
	return out, nil
}

func matchesCredentialPattern(literal, pattern string) bool {
	if pattern == codexAuthFile {
		return literal == codexAuthFile ||
			strings.HasSuffix(literal, "/"+codexAuthFile) ||
			strings.HasSuffix(literal, `\`+codexAuthFile)
	}
	return strings.Contains(literal, pattern)
}

func TestNoVendorCredentialAccess(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkNoVendorCredentialAccess(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkNoVendorCredentialAccess(t.Context(), fixture("vendor-creds-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkNoVendorCredentialAccess(t.Context(), fixture("vendor-creds-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			`internal/steal/steal.go references vendor credential store ".credentials.json"`,
			`internal/steal/steal.go references vendor credential store "auth.json"`,
			`internal/steal/steal.go references vendor credential store "Claude Code-credentials"`,
			`internal/steal/steal.go references vendor credential store "security find-generic-password"`,
		)
	})
}
