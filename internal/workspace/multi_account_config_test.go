package workspace

import (
	"path/filepath"
	"testing"
)

func TestHostMultiAccountConfigValidationAndProvenance(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	raw := `
[host]
multi_account_enabled = true
multi_account_terms_accepted = true
account_strategy = "best"

[host.extra_accounts]
claude = ["work-2"]
`
	cfg, err := decodeTier2TOML(filepath.Join(home, "config.toml"), []byte(raw), home)
	if err != nil {
		t.Fatalf("decodeTier2TOML: %v", err)
	}
	base := DefaultConfig(home, "")
	eff := mergeConfigs(&base, &cfg, nil, nil, home)
	if !eff.Host.MultiAccountEnabled || !eff.Host.MultiAccountTermsAccepted || eff.Host.AccountStrategy != "best" {
		t.Fatalf("unexpected Host multi-account config: %+v", eff.Host)
	}
	if len(eff.Host.ExtraAccounts["claude"]) != 1 || eff.Host.ExtraAccounts["claude"][0] != "work-2" {
		t.Fatalf("unexpected ExtraAccounts: %+v", eff.Host.ExtraAccounts)
	}
	if eff.Provenance["host.multi_account_enabled"] != TierGlobal || eff.Provenance["host.multi_account_terms_accepted"] != TierGlobal {
		t.Fatalf("unexpected provenance: %+v", eff.Provenance)
	}

	// Invalid strategy and traversal account ID are rejected.
	for _, tc := range []struct {
		name string
		toml string
	}{
		{"bad strategy", "[host]\naccount_strategy = \"random\"\n"},
		{"bad vendor", "[host.extra_accounts]\nother = [\"work\"]\n"},
		{"traversal id", "[host.extra_accounts]\nclaude = [\"../escape\"]\n"},
		{"reserved default", "[host.extra_accounts]\nclaude = [\"default\"]\n"},
	} {
		if _, err := decodeTier2TOML(filepath.Join(home, "config.toml"), []byte(tc.toml), home); CodeOf(err) != ErrConfigInvalid {
			t.Fatalf("%s: err = %v, want ErrConfigInvalid", tc.name, err)
		}
	}
}
