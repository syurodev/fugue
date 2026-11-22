package vendor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

func TestValidateAccountIDRejectsTraversalAndReservedNames(t *testing.T) {
	t.Parallel()
	valid := []string{"work", "acct-2", "user_1.alt", "A1"}
	for _, id := range valid {
		if err := ValidateAccountID(id); err != nil {
			t.Fatalf("ValidateAccountID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{"", "default", ".", "..", "../escape", "a/b", `a\b`, "-lead", ".hidden", "has space", strings.Repeat("a", 65)}
	for _, id := range invalid {
		if err := ValidateAccountID(id); errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Fatalf("ValidateAccountID(%q) = %v, want InvalidArgument", id, err)
		}
	}
}

func TestPrepareExtraHomeSharesSettingsAndIsolatesCredentials(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	primary := t.TempDir()

	if err := os.WriteFile(filepath.Join(primary, "settings.json"), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(primary, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, ".credentials.json"), []byte(`{"token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	home, err := PrepareExtraHome(dataDir, "claude", "work-2", primary)
	if err != nil {
		t.Fatalf("PrepareExtraHome: %v", err)
	}
	wantDir := filepath.Join(dataDir, "vendor-homes", "claude", "work-2")
	if home.ConfigDir != wantDir {
		t.Fatalf("ConfigDir = %q, want %q", home.ConfigDir, wantDir)
	}

	for _, shared := range []string{"settings.json", "skills"} {
		info, statErr := os.Lstat(filepath.Join(home.ConfigDir, shared))
		if statErr != nil {
			t.Fatalf("expected shared symlink %s: %v", shared, statErr)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("expected %s to be a symlink", shared)
		}
	}
	if _, credErr := os.Lstat(filepath.Join(home.ConfigDir, ".credentials.json")); !os.IsNotExist(credErr) {
		t.Fatalf("credentials file must not be shared into extra home, err=%v", credErr)
	}
}

func TestPrepareExtraHomeRejectsSymlinkComponent(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dataDir, "vendor-homes")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareExtraHome(dataDir, "codex", "work-2", ""); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("PrepareExtraHome with symlinked vendor-homes = %v, want PermissionDenied", err)
	}
}

func TestAccountsMultiAccountFailClosedWhenDisabled(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	a := NewAccounts(clk, []AccountVendor{{Name: "claude", DirectQuota: true}})
	if err := a.RegisterAccount("claude", "work-2", AccountHome{ConfigDir: "/tmp/claude-work-2"}); err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}

	// Disabled by default: extra account is hidden and cannot pass quota or return home.
	if got := a.VendorAccounts("claude"); len(got) != 1 || got[0].ID != DefaultAccountID {
		t.Fatalf("VendorAccounts when disabled = %+v, want only default", got)
	}
	if _, ok := a.AccountHomeFor("claude", "work-2"); ok {
		t.Fatal("AccountHomeFor(work-2) must fail closed when multi-account is disabled")
	}
	a.ObserveAccountQuota("claude", "default", &RateLimit{Windows: []Window{{Name: "5h", Utilization: 0.90}}})
	if a.QuotaAllows("claude") {
		t.Fatal("QuotaAllows must be false when default is >=85% and multi-account is disabled")
	}

	// Enabled: extra account becomes eligible and QuotaAllows becomes true.
	a.SetMultiAccountEnabled(true)
	if got := a.VendorAccounts("claude"); len(got) != 2 {
		t.Fatalf("VendorAccounts when enabled = %d, want 2", len(got))
	}
	if home, ok := a.AccountHomeFor("claude", "work-2"); !ok || home.ConfigDir != "/tmp/claude-work-2" {
		t.Fatalf("AccountHomeFor(work-2) = (%+v, %v)", home, ok)
	}
	if !a.QuotaAllows("claude") {
		t.Fatal("QuotaAllows must be true when work-2 is healthy and multi-account is enabled")
	}
	if !strings.Contains(MultiAccountTermsWarning(), "Anthropic") || !strings.Contains(MultiAccountTermsWarning(), "OpenAI") {
		t.Fatalf("MultiAccountTermsWarning missing vendor notices: %q", MultiAccountTermsWarning())
	}
}

func TestDefaultPrimaryHomeDirAndSymlinkHardening(t *testing.T) {
	t.Parallel()
	if got := DefaultPrimaryHomeDir("/home/u", "claude"); got != filepath.Join("/home/u", ".claude") {
		t.Fatalf("claude primary home = %q", got)
	}
	if got := DefaultPrimaryHomeDir("/home/u", "codex"); got != filepath.Join("/home/u", ".codex") {
		t.Fatalf("codex primary home = %q", got)
	}
	if got := DefaultPrimaryHomeDir("/home/u", "agy"); got != "/home/u" {
		t.Fatalf("agy primary home = %q", got)
	}
	if got := DefaultPrimaryHomeDir("", "claude"); got != "" {
		t.Fatalf("empty home = %q, want empty", got)
	}
	assertSymlinkedPrimarySkipped(t)
	assertSymlinkedIntermediateAndSrcSkipped(t)
}

func assertSymlinkedPrimarySkipped(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	realPrimary := t.TempDir()
	if wErr := os.WriteFile(filepath.Join(realPrimary, "settings.json"), []byte("{}"), 0o600); wErr != nil {
		t.Fatal(wErr)
	}
	symPrimary := filepath.Join(t.TempDir(), "sym-primary")
	if symErr := os.Symlink(realPrimary, symPrimary); symErr != nil {
		t.Fatal(symErr)
	}
	home1, err := PrepareExtraHome(dataDir, "claude", "sym-pri", symPrimary)
	if err != nil {
		t.Fatalf("PrepareExtraHome(symPrimary): %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(home1.ConfigDir, "settings.json")); !os.IsNotExist(statErr) {
		t.Fatalf("symlinked primaryDir must not be linked, err=%v", statErr)
	}
}

func assertSymlinkedIntermediateAndSrcSkipped(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	outside := t.TempDir()
	if wErr := os.WriteFile(filepath.Join(outside, "settings.json"), []byte("{}"), 0o600); wErr != nil {
		t.Fatal(wErr)
	}
	agyPrimary := t.TempDir()
	if symErr := os.Symlink(outside, filepath.Join(agyPrimary, ".gemini")); symErr != nil {
		t.Fatal(symErr)
	}
	home2, err := PrepareExtraHome(dataDir, "agy", "sym-inter", agyPrimary)
	if err != nil {
		t.Fatalf("PrepareExtraHome(sym-inter): %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(home2.ConfigDir, ".gemini", "settings.json")); !os.IsNotExist(statErr) {
		t.Fatalf("symlinked intermediate .gemini must not be linked, err=%v", statErr)
	}

	claudePrimary := t.TempDir()
	if symErr := os.Symlink(filepath.Join(outside, "settings.json"), filepath.Join(claudePrimary, "settings.json")); symErr != nil {
		t.Fatal(symErr)
	}
	home3, err := PrepareExtraHome(dataDir, "claude", "sym-src", claudePrimary)
	if err != nil {
		t.Fatalf("PrepareExtraHome(sym-src): %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(home3.ConfigDir, "settings.json")); !os.IsNotExist(statErr) {
		t.Fatalf("symlinked source file must not be linked, err=%v", statErr)
	}
}
