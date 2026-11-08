package process

import (
	"slices"
	"strings"
)

// DefaultAllowlist lists the standard environment variables allowed from the host:
// PATH, HOME, USER, LOGNAME, LANG. TERM is forced to "dumb" unconditionally.
func DefaultAllowlist() []string {
	return []string{
		"PATH",
		"HOME",
		"USER",
		"LOGNAME",
		"LANG",
	}
}

// FilterEnv filters a host environment slice against an allowlist and constructs
// a clean environment for a vendor process per vendor-runtime §8.
//
// TERM is unconditionally set to "dumb".
// Vendor home overrides (e.g. CLAUDE_CONFIG_DIR, CODEX_HOME) are set when non-empty.
// Profile extras override or add to the resulting environment.
// It is pure and never queries the operating system environment.
func FilterEnv(base, allowlist []string, vendorHome map[string]string, extras []string) []string {
	allowed := make(map[string]bool, len(allowlist))
	for _, k := range allowlist {
		if k != "" && k != "TERM" {
			allowed[k] = true
		}
	}

	envMap := make(map[string]string)
	applyBaseEntries(envMap, base, allowed)

	// TERM is always dumb so CLIs do not emit terminal control codes.
	envMap["TERM"] = "dumb"

	applyVendorHome(envMap, vendorHome)
	applyExtras(envMap, extras)

	return sortedEnvSlice(envMap)
}

func applyBaseEntries(dst map[string]string, entries []string, allowed map[string]bool) {
	for _, entry := range entries {
		k, v, ok := strings.Cut(entry, "=")
		if ok && allowed[k] {
			dst[k] = v
		}
	}
}

func applyVendorHome(dst, vendorHome map[string]string) {
	for k, v := range vendorHome {
		if k != "" && v != "" {
			dst[k] = v
		}
	}
}

func applyExtras(dst map[string]string, extras []string) {
	for _, entry := range extras {
		k, v, ok := strings.Cut(entry, "=")
		if ok && k != "" {
			dst[k] = v
		}
	}
}

func sortedEnvSlice(envMap map[string]string) []string {
	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+envMap[k])
	}
	return out
}
