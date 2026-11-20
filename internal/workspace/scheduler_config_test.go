package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostAndTeamsConfigKeysAndValidation(t *testing.T) {
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
prompt_pacing_min_seconds = 8
prompt_pacing_max_seconds = 12

[teams]
task_deadline_seconds = 600
task_max_turns = 20
silence_timeout_seconds = 90
team_deadline_seconds = 1800
team_max_agents = 8
idle_detach_seconds = 180
`)
	globalPath := filepath.Join(fugueHome, "config.toml")
	t2, err := decodeTier2TOML(globalPath, raw, home)
	if err != nil {
		t.Fatalf("decodeTier2TOML: %v", err)
	}
	base := DefaultConfig(fugueHome, "")
	eff := mergeConfigs(&base, &t2, nil, nil, home)
	if eff.Host.PerAccountSessions != 4 || eff.Host.MaxSessions != 6 || eff.Host.HeavyCommands != 3 {
		t.Fatalf("Host = %+v", eff.Host)
	}
	if eff.Host.PromptPacingMinSeconds != 8 || eff.Host.PromptPacingMaxSeconds != 12 {
		t.Fatalf("PromptPacing = %+v", eff.Host)
	}
	if eff.Teams.TaskDeadlineSeconds != 600 || eff.Teams.TaskMaxTurns != 20 || eff.Teams.IdleDetachSeconds != 180 {
		t.Fatalf("Teams = %+v", eff.Teams)
	}
}

func TestTier3RejectsHostSectionAndInvalidTeamsValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		toml string
	}{
		{"host section in tier 3", "[host]\nmax_sessions = 4\n"},
		{"negative task deadline", "[teams]\ntask_deadline_seconds = -1\n"},
		{"zero idle detach", "[teams]\nidle_detach_seconds = 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeTier3TOML(filepath.Join(dir, "fugue.toml"), []byte(tc.toml), dir); CodeOf(err) != ErrConfigInvalid {
				t.Fatalf("decodeTier3TOML(%s) = %v, want ErrConfigInvalid", tc.name, err)
			}
		})
	}
	if _, err := decodeTier2TOML(filepath.Join(dir, "config.toml"), []byte("[host]\nper_account_sessions = 0\n"), dir); CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("decodeTier2TOML invalid per_account_sessions = %v, want ErrConfigInvalid", err)
	}
	if _, err := decodeTier2TOML(filepath.Join(dir, "config.toml"), []byte("[host]\nprompt_pacing_min_seconds = 20\nprompt_pacing_max_seconds = 10\n"), dir); CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("decodeTier2TOML inverted pacing = %v, want ErrConfigInvalid", err)
	}
}
