package vendor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
)

func TestParseAndCompareVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want vendor.Version
	}{
		{name: "claude", raw: "2.1.319 (Claude Code)\n", want: vendor.Version{Major: 2, Minor: 1, Patch: 319}},
		{name: "codex", raw: "\ncodex-cli 0.167.0\n", want: vendor.Version{Major: 0, Minor: 167, Patch: 0}},
		{name: "prerelease", raw: "0.167.2-beta.1", want: vendor.Version{Major: 0, Minor: 167, Patch: 2}},
	}
	for _, tc := range cases {
		got, ok := vendor.ParseVersion(tc.raw)
		if !ok || got != tc.want {
			t.Fatalf("ParseVersion(%q) = (%v, %v), want (%v, true)", tc.raw, got, ok, tc.want)
		}
	}
	for _, bad := range []string{"", "   \n\t ", "not-a-version", "1.2", "1..2", "1.-2.3"} {
		if _, ok := vendor.ParseVersion(bad); ok {
			t.Fatalf("ParseVersion(%q) returned true, want false", bad)
		}
	}
}

func TestVersionRangeAndVerdict(t *testing.T) {
	t.Parallel()
	rng := vendor.VersionRange{
		Min: vendor.Version{Major: 0, Minor: 167, Patch: 0},
		Max: vendor.Version{Major: 0, Minor: 167, Patch: 999999},
	}
	if !rng.Contains(vendor.Version{Major: 0, Minor: 167, Patch: 5}) {
		t.Fatal("expected 0.167.5 to be in 0.167.x")
	}
	for _, out := range []vendor.Version{{Major: 0, Minor: 166, Patch: 9}, {Major: 0, Minor: 168, Patch: 0}, {Major: 1, Minor: 0, Patch: 0}} {
		if rng.Contains(out) {
			t.Fatalf("unexpected version %v in 0.167.x", out)
		}
	}
	if got := rng.String(); got != "0.167.0..0.167.999999" {
		t.Fatalf("rng.String() = %q, want 0.167.0..0.167.999999", got)
	}
	rng.Label = "0.167.x"
	if got := rng.String(); got != "0.167.x" {
		t.Fatalf("labeled rng.String() = %q, want 0.167.x", got)
	}
	exact := vendor.VersionRange{Min: vendor.Version{Major: 2, Minor: 1, Patch: 319}, Max: vendor.Version{Major: 2, Minor: 1, Patch: 319}}
	if got := exact.String(); got != "2.1.319..2.1.319" {
		t.Fatalf("exact.String() = %q, want 2.1.319..2.1.319", got)
	}
	assertVerdict(t, "codex-cli 0.167.0", rng, "0.167.0", true, false)
	assertVerdict(t, "codex-cli 0.168.0", rng, "0.168.0", false, true)
	assertVerdict(t, "unrecognized", rng, "", false, true)
}

func assertVerdict(t *testing.T, raw string, rng vendor.VersionRange, wantVer string, wantTested, wantWarn bool) {
	t.Helper()
	ver, tested, warn := vendor.VersionVerdict("codex", raw, rng)
	if ver != wantVer || tested != wantTested || (warn != "") != wantWarn {
		t.Fatalf("VersionVerdict(%q) = (%q, %v, %q), want (%q, %v, warn=%v)", raw, ver, tested, warn, wantVer, wantTested, wantWarn)
	}
}

const testPathEnv = "PATH=/usr/bin:/bin"

func TestCappedBufferAndResolveBinary(t *testing.T) {
	t.Parallel()
	overflowCalls := 0
	buf := vendor.NewCappedBuffer(5, func() { overflowCalls++ })
	assertBufWrite(t, buf, "abc", false, "abc", &overflowCalls, 0)
	assertBufWrite(t, buf, "defgh", true, "abcde", &overflowCalls, 1)
	assertBufWrite(t, buf, "ijk", true, "abcde", &overflowCalls, 1)
	if resolved, err := vendor.ResolveBinary("sh", "sh"); err != nil || resolved == "" {
		t.Fatalf("ResolveBinary(sh) = (%q, %v)", resolved, err)
	}
}

func assertBufWrite(t *testing.T, buf *vendor.CappedBuffer, in string, wantOverflow bool, wantStr string, calls *int, wantCalls int) {
	t.Helper()
	n, err := buf.Write([]byte(in))
	if err != nil || n != len(in) || buf.Overflowed() != wantOverflow || buf.String() != wantStr || *calls != wantCalls {
		t.Fatalf("Write(%q) -> n=%d err=%v overflow=%v str=%q calls=%d", in, n, err, buf.Overflowed(), buf.String(), *calls)
	}
}

func TestResolveBinaryAndRunVersionCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	assertResolveBinaryRejections(t, dir)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	linkedExe := filepath.Join(dir, "codex")
	if sErr := os.Symlink(exe, linkedExe); sErr != nil {
		t.Fatal(sErr)
	}
	if _, cErr := vendor.CheckAbsoluteBinary("codex", linkedExe); cErr != nil {
		t.Fatalf("CheckAbsoluteBinary(linkedExe) err = %v", cErr)
	}
	// Running the test binary with `--version` exits non-zero (unknown flag), which
	// RunVersionCommand treats as an unverified empty version without failing the probe.
	out, err := vendor.RunVersionCommand(t.Context(), "codex", linkedExe, []string{testPathEnv}, 0)
	if err != nil || out != "" {
		t.Fatalf("RunVersionCommand(linkedExe) = (%q, %v), want empty output and nil err", out, err)
	}

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := vendor.RunVersionCommand(canceledCtx, "codex", linkedExe, []string{testPathEnv}, time.Second); errcode.CodeOf(err) != errcode.Canceled {
		t.Fatalf("RunVersionCommand(canceled) err = %v, want Canceled", err)
	}
}

func assertResolveBinaryRejections(t *testing.T, dir string) {
	t.Helper()
	if _, err := vendor.ResolveBinary("codex", ""); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("empty binary err = %v, want InvalidArgument", err)
	}
	if _, err := vendor.ResolveBinary("codex", "relative/codex"); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("relative path err = %v, want InvalidArgument", err)
	}
	if _, err := vendor.ResolveBinary("codex", filepath.Join(dir, "missing")); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("missing binary err = %v, want NotFound", err)
	}
	if _, err := vendor.CheckAbsoluteBinary("codex", filepath.Join(dir, "missing")); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("CheckAbsoluteBinary(missing) err = %v, want NotFound", err)
	}
}
