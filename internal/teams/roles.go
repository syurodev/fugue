package teams

import "strings"

// Permission defines a catalog role's execution permission level (§5.2).
type Permission string

// Permission values defined by the role catalog (§5.2).
const (
	PermissionRead  Permission = "read"
	PermissionWrite Permission = "write"
)

// Vendor identifies a supported CLI vendor runtime.
type Vendor string

// Supported vendor runtime identifiers.
const (
	VendorClaude Vendor = "claude"
	VendorCodex  Vendor = "codex"
	VendorAGY    Vendor = "agy"
)

// Effort defines reasoning effort levels supported by vendor profiles.
type Effort string

// Supported reasoning effort levels for vendor profiles.
const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

// IsValidPermission reports whether p is read or write.
func IsValidPermission(p Permission) bool {
	return p == PermissionRead || p == PermissionWrite
}

// IsValidVendor reports whether v is a known vendor.
func IsValidVendor(v Vendor) bool {
	return v == VendorClaude || v == VendorCodex || v == VendorAGY
}

// IsValidEffort reports whether e is a supported effort level.
func IsValidEffort(e Effort) bool {
	switch e {
	case EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
		return true
	default:
		return false
	}
}

// Role describes an entry in the team role catalog (§5.2).
type Role struct {
	Name           string
	Description    string
	Vendor         Vendor
	Model          string
	Effort         Effort
	Permission     Permission
	IsLeadEligible bool
	Checks         []string
}

// VendorProbe summarizes the availability of an installed vendor CLI for starter catalog generation.
type VendorProbe struct {
	Vendor    Vendor
	Installed bool
}

// ValidateModelName checks Q-63 profile model constraints: <=128 bytes, no leading dash
// (prevents CLI flag injection), and only safe model identifier characters.
func ValidateModelName(field, model string) error {
	if model == "" {
		return nil
	}
	if len(model) > 128 || strings.HasPrefix(model, "-") {
		return &ValidationError{Field: field, Detail: "invalid model identifier", Reason: ErrInvalidCatalog}
	}
	for i := range len(model) {
		if !isModelChar(model[i]) {
			return &ValidationError{Field: field, Detail: "invalid model character", Reason: ErrInvalidCatalog}
		}
	}
	return nil
}

func isModelChar(b byte) bool {
	if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') {
		return true
	}
	switch b {
	case '-', '_', '.', ':', '/', '@':
		return true
	default:
		return false
	}
}

func isRoleIDChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
}

func validateRoleIdentifier(field, name string) error {
	if len(name) == 0 || len(name) > 64 {
		return &ValidationError{Field: field, Detail: "must be 1..64 ASCII characters", Reason: ErrInvalidCatalog}
	}
	if name[0] == '-' || name[0] == '_' {
		return &ValidationError{Field: field, Detail: "must match [a-z0-9][a-z0-9_-]*", Reason: ErrInvalidCatalog}
	}
	for i := range len(name) {
		if !isRoleIDChar(name[i]) {
			return &ValidationError{Field: field, Detail: "must match [a-z0-9][a-z0-9_-]*", Reason: ErrInvalidCatalog}
		}
	}
	return nil
}

// ValidateRole validates a single role catalog entry (§5.2).
func ValidateRole(r *Role) error {
	if r == nil {
		return &ValidationError{Field: "role", Detail: "role is required", Reason: ErrInvalidCatalog}
	}
	if err := validateRoleIdentifier("role.name", r.Name); err != nil {
		return err
	}
	if !IsValidPermission(r.Permission) {
		return &ValidationError{Field: "role.permission", Detail: "must be read or write", Reason: ErrInvalidCatalog}
	}
	if !IsValidVendor(r.Vendor) {
		return &ValidationError{Field: "role.vendor", Detail: "must be claude, codex, or agy", Reason: ErrInvalidCatalog}
	}
	if err := ValidateModelName("role.model", r.Model); err != nil {
		return err
	}
	if r.Effort != "" && !IsValidEffort(r.Effort) {
		return &ValidationError{Field: "role.effort", Detail: "must be low, medium, high, xhigh, or max", Reason: ErrInvalidCatalog}
	}
	for _, chk := range r.Checks {
		if strings.TrimSpace(chk) == "" || strings.ContainsAny(chk, "\x00\r\n") {
			return &ValidationError{Field: "role.checks", Detail: "check entries must be non-empty single-line strings", Reason: ErrInvalidCatalog}
		}
	}
	return nil
}

// ValidateCatalog validates every role entry and enforces unique role names (§5.2).
func ValidateCatalog(roles []Role) error {
	seen := make(map[string]struct{}, len(roles))
	for i := range roles {
		if err := ValidateRole(&roles[i]); err != nil {
			return err
		}
		if _, dup := seen[roles[i].Name]; dup {
			return &ValidationError{Field: "role.name", Detail: "duplicate role name " + roles[i].Name, Reason: ErrInvalidCatalog}
		}
		seen[roles[i].Name] = struct{}{}
	}
	return nil
}

// StarterCatalog builds the first-run starter role catalog (§5.2) purely from vendor probe results.
// It returns nil when no supported vendor is installed and never performs I/O.
func StarterCatalog(probes []VendorProbe) []Role {
	installed := collectInstalledVendors(probes)
	if len(installed) == 0 {
		return nil
	}
	coderVendor := pickInstalledVendor(installed, VendorCodex, VendorClaude, VendorAGY)
	return []Role{
		{
			Name:           "researcher",
			Description:    "Explore codebase, documentation, and gather structured findings.",
			Vendor:         pickInstalledVendor(installed, VendorClaude, VendorCodex, VendorAGY),
			Permission:     PermissionRead,
			IsLeadEligible: true,
		},
		{
			Name:        "scraper",
			Description: "Collect external documentation and web references safely.",
			Vendor:      pickInstalledVendor(installed, VendorAGY, VendorClaude, VendorCodex),
			Permission:  PermissionRead,
		},
		{
			Name:           "coder",
			Description:    "Implement scoped code changes within assigned write_scope.",
			Vendor:         coderVendor,
			Permission:     PermissionWrite,
			IsLeadEligible: true,
		},
		{
			Name:        "tester",
			Description: "Write and run verification suites for code changes.",
			Vendor:      pickInstalledVendor(installed, VendorCodex, VendorClaude, VendorAGY),
			Permission:  PermissionWrite,
		},
		{
			Name:        "reviewer",
			Description: "Audit diffs, architecture invariants, and security risks read-only.",
			Vendor:      pickAlternativeVendor(installed, coderVendor),
			Permission:  PermissionRead,
		},
		{
			Name:        "writer",
			Description: "Author and refine documentation, changelogs, and design notes.",
			Vendor:      pickInstalledVendor(installed, VendorClaude, VendorCodex, VendorAGY),
			Permission:  PermissionWrite,
		},
	}
}

func pickInstalledVendor(installed []Vendor, prefs ...Vendor) Vendor {
	for _, pref := range prefs {
		for _, cand := range installed {
			if cand == pref {
				return pref
			}
		}
	}
	return installed[0]
}

func collectInstalledVendors(probes []VendorProbe) []Vendor {
	order := []Vendor{VendorClaude, VendorCodex, VendorAGY}
	present := make(map[Vendor]bool, 3)
	for _, p := range probes {
		if p.Installed && IsValidVendor(p.Vendor) {
			present[p.Vendor] = true
		}
	}
	var out []Vendor
	for _, v := range order {
		if present[v] {
			out = append(out, v)
		}
	}
	return out
}

func pickAlternativeVendor(installed []Vendor, exclude Vendor) Vendor {
	for _, v := range installed {
		if v != exclude {
			return v
		}
	}
	return installed[0]
}
