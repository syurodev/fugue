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
heavy_slot_max_hold_seconds = 1800
prompt_pacing_min_seconds = 5
prompt_pacing_max_seconds = 20

[teams]
idle_detach_seconds = 120
task_deadline_seconds = 900
task_max_turns = 25
silence_timeout_seconds = 300
team_deadline_seconds = 7200
team_max_agents = 8
`)
	globalPath := filepath.Join(fugueHome, "config.toml")
	t2, err := decodeTier2TOML(globalPath, raw, home)
	if err != nil {
		t.Fatalf("decodeTier2TOML: %v", err)
	}
	base := DefaultConfig(fugueHome, "")
	assertProvenance(t, base.Provenance, TierDefault, "default")
	assertHostAndTeamsValues(t, &base.Host, &base.Teams, 10, 15, 3600, 300, 3600, 50, 600, 14400, 12)
	eff := mergeConfigs(&base, &t2, nil, nil, home)
	if eff.Host.PerAccountSessions != 4 || eff.Host.MaxSessions != 6 || eff.Host.HeavyCommands != 3 {
		t.Fatalf("merged host slots = %+v", eff.Host)
	}
	assertHostAndTeamsValues(t, &eff.Host, &eff.Teams, 5, 20, 1800, 120, 900, 25, 300, 7200, 8)
	assertProvenance(t, eff.Provenance, TierGlobal, "merged")
	verifyTier3TeamsOverride(t, dir, home, &base, &t2, &eff)
}

func assertHostAndTeamsValues(
	t *testing.T, h *HostConfig, tm *TeamsConfig,
	pMin, pMax, hold, detach, taskDL, maxTurns, silence, teamDL, maxAgents int,
) {
	t.Helper()
	if h.PromptPacingMinSeconds != pMin || h.PromptPacingMaxSeconds != pMax || h.HeavySlotMaxHoldSeconds != hold {
		t.Fatalf("HostConfig = %+v", *h)
	}
	if tm.IdleDetachSeconds != detach || tm.TaskDeadlineSeconds != taskDL || tm.TaskMaxTurns != maxTurns ||
		tm.SilenceTimeoutSeconds != silence || tm.TeamDeadlineSeconds != teamDL || tm.TeamMaxAgents != maxAgents {
		t.Fatalf("TeamsConfig = %+v", *tm)
	}
}

func verifyTier3TeamsOverride(t *testing.T, dir, home string, base *EffectiveConfig, t2 *tier2RawConfig, eff *EffectiveConfig) {
	t.Helper()
	t3, err := decodeTier3TOML(filepath.Join(dir, "fugue.toml"), []byte("[teams]\nidle_detach_seconds = 0\n"), home)
	if err != nil {
		t.Fatalf("decodeTier3TOML teams: %v", err)
	}
	eff3 := mergeConfigs(base, t2, &t3, nil, home)
	if eff3.Teams.IdleDetachSeconds != 0 || eff3.Provenance["teams.idle_detach_seconds"] != TierWorkspace {
		t.Fatalf("tier3 teams = %+v, prov=%v", eff3.Teams, eff3.Provenance["teams.idle_detach_seconds"])
	}
	if len(diffPolicy(eff, &eff3)) != 0 {
		t.Fatalf("teams.idle_detach_seconds must not trigger policy diff: %+v", diffPolicy(eff, &eff3))
	}
}

func assertProvenance(t *testing.T, prov map[string]Tier, want Tier, label string) {
	t.Helper()
	keys := []string{
		"host.per_account_sessions", "host.max_sessions", "host.heavy_commands",
		"host.heavy_slot_max_hold_seconds",
		"host.prompt_pacing_min_seconds", "host.prompt_pacing_max_seconds",
		"teams.idle_detach_seconds", "teams.task_deadline_seconds", "teams.task_max_turns",
		"teams.silence_timeout_seconds", "teams.team_deadline_seconds", "teams.team_max_agents",
	}
	for _, k := range keys {
		if prov[k] != want {
			t.Fatalf("%s provenance[%s] = %v, want %v", label, k, prov[k], want)
		}
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
		{"negative heavy hold", "[host]\nheavy_slot_max_hold_seconds = -1\n"},
		{"over max heavy hold", "[host]\nheavy_slot_max_hold_seconds = 86401\n"},
		{"negative pacing min", "[host]\nprompt_pacing_min_seconds = -1\n"},
		{"over max pacing", "[host]\nprompt_pacing_max_seconds = 301\n"},
		{"min greater than max", "[host]\nprompt_pacing_min_seconds = 20\nprompt_pacing_max_seconds = 10\n"},
		{"min exceeds default max", "[host]\nprompt_pacing_min_seconds = 20\n"},
		{"max below default min", "[host]\nprompt_pacing_max_seconds = 5\n"},
		{"negative idle detach", "[teams]\nidle_detach_seconds = -1\n"},
		{"over max idle detach", "[teams]\nidle_detach_seconds = 86401\n"},
		{"zero task deadline", "[teams]\ntask_deadline_seconds = 0\n"},
		{"over max task deadline", "[teams]\ntask_deadline_seconds = 604801\n"},
		{"zero task max turns", "[teams]\ntask_max_turns = 0\n"},
		{"over max task turns", "[teams]\ntask_max_turns = 1001\n"},
		{"negative silence timeout", "[teams]\nsilence_timeout_seconds = -1\n"},
		{"over max silence timeout", "[teams]\nsilence_timeout_seconds = 86401\n"},
		{"zero team deadline", "[teams]\nteam_deadline_seconds = 0\n"},
		{"over max team deadline", "[teams]\nteam_deadline_seconds = 604801\n"},
		{"zero team max agents", "[teams]\nteam_max_agents = 0\n"},
		{"over max team agents", "[teams]\nteam_max_agents = 65\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeTier2TOML(filepath.Join(dir, "config.toml"), []byte(tc.toml), dir); CodeOf(err) != ErrConfigInvalid {
				t.Fatalf("decodeTier2TOML(%s) = %v, want ErrConfigInvalid", tc.name, err)
			}
		})
	}
	if _, err := decodeTier3TOML(filepath.Join(dir, "fugue.toml"), []byte("[teams]\nidle_detach_seconds = 86401\n"), dir); CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("decodeTier3TOML invalid idle_detach_seconds = %v, want ErrConfigInvalid", err)
	}
}
