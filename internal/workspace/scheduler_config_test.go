package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostSchedulerConfigKeysAndValidation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	fugueHome := filepath.Join(home, ".fugue")
	if err := os.MkdirAll(fugueHome, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`
[host]
per_account_sessions = 4
max_sessions = 6
heavy_commands = 3
`)
	globalPath := filepath.Join(fugueHome, "config.toml")
	t2, err := decodeTier2TOML(globalPath, raw, home)
	if err != nil {
		t.Fatalf("decodeTier2TOML: %v", err)
	}
	base := DefaultConfig(fugueHome, "")
	if base.Provenance["host.per_account_sessions"] != TierDefault ||
		base.Provenance["host.max_sessions"] != TierDefault ||
		base.Provenance["host.heavy_commands"] != TierDefault {
		t.Fatalf("default host scheduler provenance = %+v", base.Provenance)
	}
	eff := mergeConfigs(&base, &t2, nil, nil, home)
	if eff.Host.PerAccountSessions != 4 || eff.Host.MaxSessions != 6 || eff.Host.HeavyCommands != 3 {
		t.Fatalf("Host = %+v", eff.Host)
	}
	if eff.Provenance["host.per_account_sessions"] != TierGlobal ||
		eff.Provenance["host.max_sessions"] != TierGlobal ||
		eff.Provenance["host.heavy_commands"] != TierGlobal {
		t.Fatalf("merged host scheduler provenance = %+v", eff.Provenance)
	}
}

func TestTier3RejectsHostSectionAndTier2RejectsInvalidHostValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := decodeTier3TOML(filepath.Join(dir, "fugue.toml"), []byte("[host]\nmax_sessions = 4\n"), dir); CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("decodeTier3TOML host section = %v, want ErrConfigInvalid", err)
	}
	for _, tc := range []struct {
		name string
		toml string
	}{
		{"zero per_account_sessions", "[host]\nper_account_sessions = 0\n"},
		{"zero max_sessions", "[host]\nmax_sessions = 0\n"},
		{"zero heavy_commands", "[host]\nheavy_commands = 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeTier2TOML(filepath.Join(dir, "config.toml"), []byte(tc.toml), dir); CodeOf(err) != ErrConfigInvalid {
				t.Fatalf("decodeTier2TOML(%s) = %v, want ErrConfigInvalid", tc.name, err)
			}
		})
	}
}
