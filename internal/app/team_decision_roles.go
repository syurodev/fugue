package app

// The decided plan's role profiles: what the user decided on the card is
// what the sealed TeamStarted payload records, and what F11.3 starts tasks
// with (OR-E13).

import (
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/workspace"
)

// decidedRolesOf resolves the decided profile of every role the plan uses.
// Catalog definitions win over proposed entries (§5.2), and a task profile
// override — which is how an approve-with-edits decision writes the edited
// model into the plan — wins over both (§4, OR-E13). The list is sorted by
// name so the sealed payload is stable.
func decidedRolesOf(plan *teams.ProposalInput, eff *workspace.EffectiveConfig) []decidedRole {
	resolved := planRoleDefinitions(plan, eff)
	profiles := planTaskProfiles(plan)
	out := make([]decidedRole, 0, len(resolved))
	for name := range resolved {
		if !planUsesRole(plan, name) {
			continue
		}
		role := resolved[name]
		out = append(out, decidedRoleOf(name, &role, profiles[name]))
	}
	slices.SortFunc(out, func(a, b decidedRole) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// planRoleDefinitions resolves every role the plan names: catalog
// definitions win over proposed entries of the same name (§5.2).
func planRoleDefinitions(plan *teams.ProposalInput, eff *workspace.EffectiveConfig) map[string]teams.Role {
	resolved := make(map[string]teams.Role, len(plan.Roles)+len(eff.Roles))
	for i := range eff.Roles {
		r := &eff.Roles[i]
		resolved[r.Name] = teams.Role{Name: r.Name, Vendor: teams.Vendor(r.Vendor), Model: r.Model, Effort: teams.Effort(r.Effort), Permission: teams.Permission(r.Permission)}
	}
	for i := range plan.Roles {
		p := &plan.Roles[i]
		if _, known := resolved[p.Name]; !known {
			resolved[p.Name] = teams.Role{Name: p.Name, Vendor: p.Vendor, Model: p.Model, Effort: p.Effort, Permission: p.Permission}
		}
	}
	return resolved
}

// planTaskProfiles collects the profile override of every task that carries
// one, keyed by the task's role.
func planTaskProfiles(plan *teams.ProposalInput) map[string]teams.ProfileOverride {
	profiles := make(map[string]teams.ProfileOverride, len(plan.Tasks))
	for i := range plan.Tasks {
		t := &plan.Tasks[i]
		if t.Profile != nil {
			profiles[t.Role] = *t.Profile
		}
	}
	return profiles
}

// planUsesRole reports whether any task of the plan names the role.
func planUsesRole(plan *teams.ProposalInput, name string) bool {
	for i := range plan.Tasks {
		if plan.Tasks[i].Role == name {
			return true
		}
	}
	return false
}

// decidedRoleOf merges the resolved role with its decided profile override.
func decidedRoleOf(name string, role *teams.Role, override teams.ProfileOverride) decidedRole {
	vendor, model, effort := role.Vendor, role.Model, role.Effort
	if override.Vendor != "" {
		vendor = override.Vendor
	}
	if override.Model != "" {
		model = override.Model
	}
	if override.Effort != "" {
		effort = override.Effort
	}
	return decidedRole{
		Name: name, Vendor: string(vendor), Model: model,
		Effort: string(effort), Permission: string(role.Permission),
	}
}
