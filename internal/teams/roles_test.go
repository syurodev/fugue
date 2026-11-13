package teams_test

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/teams"
)

const testCatalogRoleCoder = "coder"

func invalidCatalogCases() []struct {
	name  string
	roles []teams.Role
} {
	return []struct {
		name  string
		roles []teams.Role
	}{
		{
			name: "duplicate name",
			roles: []teams.Role{
				{Name: testCatalogRoleCoder, Vendor: teams.VendorClaude, Permission: teams.PermissionWrite},
				{Name: testCatalogRoleCoder, Vendor: teams.VendorCodex, Permission: teams.PermissionRead},
			},
		},
		{
			name:  "invalid permission",
			roles: []teams.Role{{Name: testCatalogRoleCoder, Vendor: teams.VendorClaude, Permission: teams.Permission("admin")}},
		},
		{
			name:  "unknown vendor",
			roles: []teams.Role{{Name: testCatalogRoleCoder, Vendor: teams.Vendor("gemini"), Permission: teams.PermissionRead}},
		},
		{
			name:  "model flag injection",
			roles: []teams.Role{{Name: testCatalogRoleCoder, Vendor: teams.VendorClaude, Model: "--dangerously-skip-permissions", Permission: teams.PermissionRead}},
		},
		{
			name:  "invalid effort",
			roles: []teams.Role{{Name: testCatalogRoleCoder, Vendor: teams.VendorClaude, Effort: teams.Effort("turbo"), Permission: teams.PermissionRead}},
		},
		{
			name:  "blank check string",
			roles: []teams.Role{{Name: testCatalogRoleCoder, Vendor: teams.VendorClaude, Permission: teams.PermissionWrite, Checks: []string{"   "}}},
		},
	}
}

func TestRoleCatalogValidation(t *testing.T) {
	t.Parallel()
	valid := []teams.Role{
		{
			Name: "researcher", Vendor: teams.VendorClaude, Model: "claude-sonnet-4-5",
			Effort: teams.EffortHigh, Permission: teams.PermissionRead, IsLeadEligible: true,
		},
		{
			Name: testCatalogRoleCoder, Vendor: teams.VendorCodex, Model: "gpt-5-codex",
			Effort: teams.EffortMedium, Permission: teams.PermissionWrite, Checks: []string{"go test ./..."},
		},
	}
	if err := teams.ValidateCatalog(valid); err != nil {
		t.Fatalf("valid catalog rejected: %v", err)
	}
	for _, tc := range invalidCatalogCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := teams.ValidateCatalog(tc.roles); !errors.Is(err, teams.ErrInvalidCatalog) {
				t.Fatalf("ValidateCatalog() err = %v, want ErrInvalidCatalog", err)
			}
		})
	}
}

func TestStarterCatalogFollowsInstalledVendors(t *testing.T) {
	t.Parallel()
	if got := teams.StarterCatalog([]teams.VendorProbe{{Vendor: teams.VendorClaude, Installed: false}}); len(got) != 0 {
		t.Fatalf("expected empty catalog when nothing installed, got %d", len(got))
	}

	single := teams.StarterCatalog([]teams.VendorProbe{
		{Vendor: teams.VendorClaude, Installed: true},
		{Vendor: teams.VendorCodex, Installed: false},
		{Vendor: teams.VendorAGY, Installed: false},
	})
	if err := teams.ValidateCatalog(single); err != nil {
		t.Fatalf("single-vendor starter catalog invalid: %v", err)
	}
	for _, r := range single {
		if r.Vendor != teams.VendorClaude {
			t.Fatalf("single-vendor catalog assigned uninstalled vendor %q to %q", r.Vendor, r.Name)
		}
	}

	all := teams.StarterCatalog([]teams.VendorProbe{
		{Vendor: teams.VendorClaude, Installed: true},
		{Vendor: teams.VendorCodex, Installed: true},
		{Vendor: teams.VendorAGY, Installed: true},
	})
	if err := teams.ValidateCatalog(all); err != nil {
		t.Fatalf("multi-vendor starter catalog invalid: %v", err)
	}
	byName := make(map[string]teams.Role, len(all))
	for _, r := range all {
		byName[r.Name] = r
	}
	if byName[testCatalogRoleCoder].Vendor != teams.VendorCodex || byName["reviewer"].Vendor == byName[testCatalogRoleCoder].Vendor {
		t.Fatalf("unexpected cross-vendor assignment: coder=%s reviewer=%s", byName[testCatalogRoleCoder].Vendor, byName["reviewer"].Vendor)
	}
	if byName["researcher"].Permission != teams.PermissionRead || !byName["researcher"].IsLeadEligible {
		t.Fatalf("unexpected researcher role defaults: %+v", byName["researcher"])
	}
}
