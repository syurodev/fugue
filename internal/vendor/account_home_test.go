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
