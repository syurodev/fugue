package workspace

import (
	"path/filepath"
	"slices"
	"testing"
)

func decodeTier(t *testing.T, path string, raw []byte, tier2 bool) tier2RawConfig {
	t.Helper()
	var cfg tier2RawConfig
	var err error
	if tier2 {
		cfg, err = decodeTier2TOML(path, raw, t.TempDir())
	} else {
		t3, decodeErr := decodeTier3TOML(path, raw, t.TempDir())
		if decodeErr != nil {
			t.Fatalf("decodeTier3TOML: %v", decodeErr)
		}
		return tier2RawConfig{Session: t3.Session, Roles: t3.Roles}
	}
	if err != nil {
		t.Fatalf("decodeTier2TOML: %v", err)
	}
	return cfg
}

// TestSessionTeamsDefaultsTrueAndMergesAcrossTiers pins the Q-105 decision:
// [session].teams defaults to true, every configuration tier can narrow or
// widen it, and the merged provenance always names the tier that decided.
func TestSessionTeamsDefaultsTrueAndMergesAcrossTiers(t *testing.T) {
	t.Parallel()
	fugueHome := t.TempDir()
	base := DefaultConfig(fugueHome, "ws-teams")
	if !base.Session.Teams || base.Provenance["session.teams"] != TierDefault {
		t.Fatalf("default teams = %v (%v), want true at tier default", base.Session.Teams, base.Provenance["session.teams"])
	}
	off := decodeTier(t, filepath.Join(fugueHome, "config.toml"), []byte("[session]\nteams = false\n"), true)
	merged := mergeConfigs(&base, &off, nil, nil, fugueHome)
	if merged.Session.Teams || merged.Provenance["session.teams"] != TierGlobal {
		t.Fatalf("tier 2 teams = %v (%v), want false at tier global", merged.Session.Teams, merged.Provenance["session.teams"])
	}
	// Tier 3 restates the tier 2 value: the provenance moves to the deciding tier.
	falseFlag := false
	merged = mergeConfigs(&base, &off, &tier3RawConfig{Session: &rawSession{Teams: &falseFlag}}, nil, fugueHome)
	if merged.Session.Teams || merged.Provenance["session.teams"] != TierWorkspace {
		t.Fatalf("tier 3 restated teams = %v (%v), want false at tier workspace", merged.Session.Teams, merged.Provenance["session.teams"])
	}
	// Tier 3 widens over the narrowed tier 2.
	merged = mergeConfigs(&base, &off, &tier3RawConfig{Session: &rawSession{Teams: boolPtr(true)}}, nil, fugueHome)
	if !merged.Session.Teams || merged.Provenance["session.teams"] != TierWorkspace {
		t.Fatalf("tier 3 teams = %v (%v), want true at tier workspace", merged.Session.Teams, merged.Provenance["session.teams"])
	}
	merged = mergeConfigs(&base, nil, nil, &SessionOverride{Model: "sonnet"}, fugueHome)
	if !merged.Session.Teams {
		t.Fatalf("tier 4 model override changed teams: %v", merged.Session.Teams)
	}
}

// TestSessionTeamsIsPolicyProtected pins the Q-105 companion decision: the
// flag is a policy key (Q-64), so any accepted-vs-candidate change waits for
// user confirmation.
func TestSessionTeamsIsPolicyProtected(t *testing.T) {
	t.Parallel()
	old := DefaultConfig(t.TempDir(), "ws-teams")
	candidate := old
	if keys := diffPolicy(&old, &candidate); slices.ContainsFunc(keys, func(e PolicyDiffEntry) bool { return e.Key == "session.teams" }) {
		t.Fatalf("unchanged teams diffed: %+v", keys)
	}
	candidate.Session.Teams = false
	keys := diffPolicy(&old, &candidate)
	if !slices.ContainsFunc(keys, func(e PolicyDiffEntry) bool { return e.Key == "session.teams" }) {
		t.Fatalf("teams change not in the policy diff: %+v", keys)
	}
}

func boolPtr(v bool) *bool { return &v }
