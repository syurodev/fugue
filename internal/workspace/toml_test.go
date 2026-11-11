package workspace

import (
	"errors"
	"strings"
	"testing"
)

func TestConfigRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	invalidTOML := []byte("name = \"my-project\"\nunknown_option = true\n")
	_, err := decodeConfigTOML(invalidTOML)
	if err == nil {
		t.Fatal("expected error decoding TOML with unknown keys, got nil")
	}
	if CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("expected ErrConfigInvalid, got %v", err)
	}
	var target *Error
	if !errors.As(err, &target) || target.Line != 2 {
		t.Fatalf("expected error at line 2, got %v", err)
	}
	if strings.Contains(err.Error(), "unknown_option = true") {
		t.Errorf("error message leaked file contents: %s", err.Error())
	}
}

func TestConfigDecodeErrorHasLineAndColumn(t *testing.T) {
	t.Parallel()
	syntaxErrTOML := []byte("name = \"good\"\n[[roots]]\nname = [unclosed\n")
	_, err := decodeConfigTOML(syntaxErrTOML)
	if err == nil {
		t.Fatal("expected syntax error, got nil")
	}
	if CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("expected ErrConfigInvalid, got %v", err)
	}
	var wsErr *Error
	if !errors.As(err, &wsErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	line, col, ok := wsErr.Position()
	if !ok || line == 0 || col == 0 {
		t.Fatalf("expected valid position (line > 0, col > 0), got line=%d, col=%d, ok=%v", line, col, ok)
	}
}

func TestConfigRoundTripsPathsWithQuotesAndUnicode(t *testing.T) {
	t.Parallel()
	roots := []Root{
		{
			Name:   "tieng-viet",
			Path:   "/tmp/dự án/thư mục \"đặc biệt\"\\test",
			Access: AccessReadWrite,
		},
	}
	encoded, err := encodeConfigTOML("dự-án-chính", roots)
	if err != nil {
		t.Fatalf("encodeConfigTOML: %v", err)
	}

	decoded, err := decodeConfigTOML(encoded)
	if err != nil {
		t.Fatalf("decodeConfigTOML: %v", err)
	}
	if decoded.Name != "dự-án-chính" {
		t.Errorf("decoded Name = %q, want 'dự-án-chính'", decoded.Name)
	}
	if len(decoded.Roots) != 1 || decoded.Roots[0].Path != roots[0].Path {
		t.Errorf("decoded roots = %+v, want %+v", decoded.Roots, roots)
	}
}

func TestIsSubPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
		parent string
		want   bool
	}{
		{"prefix dots ..x", "/a/..x", "/a", true},
		{"triple dots ...", "/a/...", "/a", true},
		{"identical path", "/a", "/a", false},
		{"sibling path", "/b", "/a", false},
		{"nested child", "/a/child", "/a", true},
		{"deep nested", "/a/b/c", "/a", true},
		{"parent of target", "/a", "/a/child", false},
		{"cousin paths", "/a/b", "/a/c", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isSubPath(tt.target, tt.parent)
			if got != tt.want {
				t.Errorf("isSubPath(%q, %q) = %v, want %v", tt.target, tt.parent, got, tt.want)
			}
		})
	}
}
