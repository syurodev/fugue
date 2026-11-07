package process

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestEnvFilterAllowlistAndForcedDumbTerm(t *testing.T) {
	t.Parallel()

	base := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/Users/alice",
		"USER=alice",
		"LOGNAME=alice",
		"LANG=en_US.UTF-8",
		"TERM=xterm-256color",
		"FUGUE_SECRET_KEY=supersecretkey123",
		"AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
		"RANDOM_VAR=should_be_stripped",
	}

	vendorHome := map[string]string{
		"CLAUDE_CONFIG_DIR": "/custom/claude",
		"CODEX_HOME":        "/custom/codex",
	}

	extras := []string{
		"PROFILE_EXTRA=active",
		"LANG=C.UTF-8", // overrides base LANG
	}

	got := FilterEnv(base, DefaultAllowlist(), vendorHome, extras)

	want := []string{
		"CLAUDE_CONFIG_DIR=/custom/claude",
		"CODEX_HOME=/custom/codex",
		"HOME=/Users/alice",
		"LANG=C.UTF-8",
		"LOGNAME=alice",
		"PATH=/usr/bin:/bin",
		"PROFILE_EXTRA=active",
		"TERM=dumb",
		"USER=alice",
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("FilterEnv mismatch (-want +got):\n%s", diff)
	}

	// Verify no secrets leaked
	for _, entry := range got {
		if slices.Contains([]string{"FUGUE_SECRET_KEY", "AWS_ACCESS_KEY_ID", "RANDOM_VAR"}, entry) {
			t.Errorf("unwanted variable present: %s", entry)
		}
	}
}
