package workspace

import (
	"bytes"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

// ConfigPatchOp identifies the operation for a structured workspace config patch.
type ConfigPatchOp uint8

// Supported structured config patch operations.
const (
	PatchOpSet ConfigPatchOp = iota + 1
	PatchOpDelete
)

// ConfigPatch describes one field-level update or deletion against fugue.toml.
type ConfigPatch struct {
	DottedKey string
	Op        ConfigPatchOp
	Value     any
}

type patchValueKind uint8

const (
	patchKindString patchValueKind = iota + 1
	patchKindBool
	patchKindInt
	patchKindStringSlice
	patchKindEnvTags
)

var allowedPatchKeys = map[string]patchValueKind{
	"name":                          patchKindString,
	keyPolicyApprovalMode:           patchKindString,
	"policy.sandbox_profile":        patchKindString,
	"policy.shell_ask":              patchKindStringSlice,
	"policy.shell_ask_add":          patchKindStringSlice,
	"policy.shell_allow":            patchKindStringSlice,
	"policy.shell_allow_add":        patchKindStringSlice,
	"policy.env_tags":               patchKindEnvTags,
	"env.keep":                      patchKindStringSlice,
	"env.keep_add":                  patchKindStringSlice,
	"env.override_allow":            patchKindStringSlice,
	"env.override_allow_add":        patchKindStringSlice,
	"redact.allow_keys":             patchKindStringSlice,
	"redact.allow_keys_add":         patchKindStringSlice,
	"redact.allow_paths":            patchKindStringSlice,
	"redact.allow_paths_add":        patchKindStringSlice,
	"session.scratch_dir":           patchKindString,
	"session.teams":                 patchKindBool,
	"session.teams_auto_approve":    patchKindBool,
	"teams.idle_detach_seconds":     patchKindInt,
	"teams.task_deadline_seconds":   patchKindInt,
	"teams.task_max_turns":          patchKindInt,
	"teams.silence_timeout_seconds": patchKindInt,
	"teams.team_deadline_seconds":   patchKindInt,
	"teams.team_max_agents":         patchKindInt,
	"teams.approve_every_diff_step": patchKindBool,
}

const maxConfigPatches = 64

// PatchTOML applies patches to rawTOML in-place while preserving comments,
// blank lines, and existing key order.
func PatchTOML(rawTOML []byte, patches []ConfigPatch) ([]byte, error) {
	if len(patches) == 0 || len(patches) > maxConfigPatches {
		return nil, cfgErr("fugue.toml", "", "config patch count must be between 1 and 64")
	}
	if err := checkTOMLSyntax(rawTOML); err != nil {
		return nil, err
	}
	lines := splitTOMLLines(rawTOML)
	for _, p := range patches {
		kind, section, key, err := validatePatchKey(p.DottedKey)
		if err != nil {
			return nil, err
		}
		var encoded string
		switch p.Op {
		case PatchOpSet:
			encoded, err = encodePatchValue(p.DottedKey, kind, p.Value)
			if err != nil {
				return nil, err
			}
		case PatchOpDelete:
		default:
			return nil, cfgErr("fugue.toml", p.DottedKey, "unsupported config patch operation")
		}
		lines = applySinglePatch(lines, p.DottedKey, section, key, p.Op, encoded)
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func checkTOMLSyntax(rawTOML []byte) error {
	if len(bytes.TrimSpace(rawTOML)) == 0 {
		return nil
	}
	var doc map[string]any
	err := toml.Unmarshal(rawTOML, &doc)
	if err == nil {
		return nil
	}
	var decErr *toml.DecodeError
	if errors.As(err, &decErr) {
		row, col := decErr.Position()
		return &Error{
			Code: ErrConfigInvalid, File: "fugue.toml",
			Key: strings.Join(decErr.Key(), "."), Line: row, Column: col,
			Message: "invalid TOML syntax or type",
		}
	}
	return &Error{Code: ErrConfigInvalid, File: "fugue.toml", Message: "invalid TOML syntax"}
}

func splitTOMLLines(rawTOML []byte) []string {
	if len(rawTOML) == 0 {
		return nil
	}
	s := strings.ReplaceAll(string(rawTOML), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

func validatePatchKey(dottedKey string) (patchValueKind, string, string, error) {
	if dottedKey == "" || hasControlChars(dottedKey) || strings.ContainsAny(dottedKey, " \t") {
		return 0, "", "", cfgErr("fugue.toml", dottedKey, "invalid configuration key")
	}
	if kind, ok := allowedPatchKeys[dottedKey]; ok {
		sec, key := splitDottedSection(dottedKey)
		return kind, sec, key, nil
	}
	parts := strings.Split(dottedKey, ".")
	if len(parts) == 3 && parts[0] == "profiles" && ValidateRootName(parts[1]) == nil && slices.Contains([]string{"vendor", "model", "effort"}, parts[2]) {
		return patchKindString, "profiles." + parts[1], parts[2], nil
	}
	return 0, "", "", cfgErr("fugue.toml", dottedKey, "unknown or unsupported configuration key")
}

func splitDottedSection(dottedKey string) (string, string) {
	idx := strings.LastIndexByte(dottedKey, '.')
	if idx < 0 {
		return "", dottedKey
	}
	return dottedKey[:idx], dottedKey[idx+1:]
}

func encodePatchValue(key string, kind patchValueKind, val any) (string, error) {
	switch kind {
	case patchKindString:
		s, ok := val.(string)
		if !ok || !utf8.ValidString(s) || hasControlChars(s) {
			return "", cfgErr("fugue.toml", key, "expected valid single-line string value")
		}
		return encodeTOMLString(s), nil
	case patchKindBool:
		b, ok := val.(bool)
		if !ok {
			return "", cfgErr("fugue.toml", key, "expected boolean value")
		}
		return strconv.FormatBool(b), nil
	case patchKindInt:
		n, ok := toInt64(val)
		if !ok {
			return "", cfgErr("fugue.toml", key, "expected integer value")
		}
		return strconv.FormatInt(n, 10), nil
	case patchKindStringSlice:
		return encodeStringSlice(key, val)
	case patchKindEnvTags:
		return encodeEnvTagSlice(key, val)
	default:
		return "", cfgErr("fugue.toml", key, "unsupported value type")
	}
}

func toInt64(val any) (int64, bool) {
	switch v := val.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v < math.MinInt64 || v > math.MaxInt64 {
			return 0, false
		}
		return int64(v), true
	default:
		return 0, false
	}
}

func encodeStringSlice(key string, val any) (string, error) {
	var items []string
	switch v := val.(type) {
	case []string:
		items = v
	case []any:
		items = make([]string, 0, len(v))
		for _, elem := range v {
			s, ok := elem.(string)
			if !ok {
				return "", cfgErr("fugue.toml", key, "expected string array element")
			}
			items = append(items, s)
		}
	default:
		return "", cfgErr("fugue.toml", key, "expected string array value")
	}
	parts := make([]string, 0, len(items))
	for _, s := range items {
		if !utf8.ValidString(s) || hasControlChars(s) {
			return "", cfgErr("fugue.toml", key, "string array element contains control characters")
		}
		parts = append(parts, encodeTOMLString(s))
	}
	return "[" + strings.Join(parts, ", ") + "]", nil
}

func encodeEnvTagSlice(key string, val any) (string, error) {
	var rules []EnvTagRule
	switch v := val.(type) {
	case []EnvTagRule:
		rules = v
	case []any:
		var err error
		rules, err = parseEnvTagRules(key, v)
		if err != nil {
			return "", err
		}
	default:
		return "", cfgErr("fugue.toml", key, "expected env_tags array value")
	}
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		if !utf8.ValidString(r.Match) || !utf8.ValidString(r.Tag) || hasControlChars(r.Match) || hasControlChars(r.Tag) {
			return "", cfgErr("fugue.toml", key, "env_tags rule contains control characters")
		}
		parts = append(parts, "{ match = "+encodeTOMLString(r.Match)+", tag = "+encodeTOMLString(r.Tag)+" }")
	}
	return "[" + strings.Join(parts, ", ") + "]", nil
}

func parseEnvTagRules(key string, raw []any) ([]EnvTagRule, error) {
	rules := make([]EnvTagRule, 0, len(raw))
	for _, elem := range raw {
		m, ok := elem.(map[string]any)
		if !ok {
			return nil, cfgErr("fugue.toml", key, "expected env_tags table entry")
		}
		matchStr, okM := m["match"].(string)
		tagStr, okT := m["tag"].(string)
		if !okM || !okT || len(m) != 2 {
			return nil, cfgErr("fugue.toml", key, "env_tags entry must have only match and tag strings")
		}
		rules = append(rules, EnvTagRule{Match: matchStr, Tag: tagStr})
	}
	return rules, nil
}

func hasControlChars(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

func encodeTOMLString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := range len(s) {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('"')
	return b.String()
}
