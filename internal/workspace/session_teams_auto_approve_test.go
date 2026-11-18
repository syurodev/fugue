package workspace

import (
	"path/filepath"
	"testing"
)

// TestSessionTeamsAutoApproveDefaultsFalseAndMergesAcrossTiers pins the Q-111
// decision: [session].teams_auto_approve defaults to false — every proposal
// waits for the user unless the workspace turns it on — every configuration
// tier can set it, and the merged provenance names the deciding tier.
func TestSessionTeamsAutoApproveDefaultsFalseAndMergesAcrossTiers(t *testing.T) {
	t.Parallel()
	fugueHome := t.TempDir()
	base := DefaultConfig(fugueHome, "ws-teams-auto")
	if base.Session.TeamsAutoApprove || base.Provenance["session.teams_auto_approve"] != TierDefault {
		t.Fatalf("default teams_auto_approve = %v (%v), want false at tier default", base.Session.TeamsAutoApprove, base.Provenance["session.teams_auto_approve"])
	}
	on := decodeTier(t, filepath.Join(fugueHome, "config.toml"), []byte("[session]\nteams_auto_approve = true\n"), true)
	merged := mergeConfigs(&base, &on, nil, nil, fugueHome)
	if !merged.Session.TeamsAutoApprove || merged.Provenance["session.teams_auto_approve"] != TierGlobal {
		t.Fatalf("tier 2 teams_auto_approve = %v (%v), want true at tier global", merged.Session.TeamsAutoApprove, merged.Provenance["session.teams_auto_approve"])
	}
	offFlag := false
	merged = mergeConfigs(&base, &on, &tier3RawConfig{Session: &rawSession{TeamsAutoApprove: &offFlag}}, nil, fugueHome)
	if merged.Session.TeamsAutoApprove || merged.Provenance["session.teams_auto_approve"] != TierWorkspace {
		t.Fatalf("tier 3 teams_auto_approve = %v (%v), want false at tier workspace", merged.Session.TeamsAutoApprove, merged.Provenance["session.teams_auto_approve"])
	}
	merged = mergeConfigs(&base, nil, nil, &SessionOverride{Model: "sonnet"}, fugueHome)
	if merged.Session.TeamsAutoApprove {
		t.Fatalf("tier 4 model override turned auto-approve on: %v", merged.Session.TeamsAutoApprove)
	}
}

// TestSessionTeamsAutoApproveIsPolicyProtected pins the Q-64 companion: an
// out-of-band widening of the auto-approve key waits for user confirmation.
func TestSessionTeamsAutoApproveIsPolicyProtected(t *testing.T) {
	t.Parallel()
	assertSessionBoolPolicyProtected(t, "session.teams_auto_approve", func(c *EffectiveConfig) { c.Session.TeamsAutoApprove = true })
}
