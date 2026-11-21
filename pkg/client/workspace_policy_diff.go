package client

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// DiffRawTOMLPolicy compares two workspace-tier TOML documents and returns
// PolicyDiffEntry items for any policy-protected keys (or keys that bump
// policy_revision) that differ between oldTOML and newTOML.
func DiffRawTOMLPolicy(oldTOML, newTOML []byte) ([]PolicyDiffEntry, error) {
	oldDoc, err := decodeTOMLMap(oldTOML)
	if err != nil {
		return nil, fmt.Errorf("decode current workspace TOML: %w", err)
	}
	newDoc, err := decodeTOMLMap(newTOML)
	if err != nil {
		return nil, fmt.Errorf("decode candidate workspace TOML: %w", err)
	}

	keys := []string{
		"policy.approval_mode",
		"policy.sandbox_profile",
		"policy.shell_ask",
		"policy.shell_ask_add",
		"policy.shell_allow",
		"policy.shell_allow_add",
		"policy.env_tags",
		"env.keep",
		"env.keep_add",
		"env.override_allow",
		"env.override_allow_add",
		"redact.allow_keys",
		"redact.allow_keys_add",
		"redact.allow_paths",
		"redact.allow_paths_add",
		"session.teams",
		"session.teams_auto_approve",
		"tasks.verify",
		"roots",
		"roles",
	}

	var diffs []PolicyDiffEntry
	for _, fullKey := range keys {
		section, key, hasDot := strings.Cut(fullKey, ".")
		if !hasDot {
			section, key = "", fullKey
		}
		oldVal, hasOld := lookupTOMLValue(oldDoc, section, key)
		newVal, hasNew := lookupTOMLValue(newDoc, section, key)
		if !hasOld && !hasNew {
			continue
		}
		oldStr := formatTOMLValue(oldVal, hasOld)
		newStr := formatTOMLValue(newVal, hasNew)
		if oldStr != newStr {
			diffs = append(diffs, PolicyDiffEntry{
				Key:      fullKey,
				OldValue: oldStr,
				NewValue: newStr,
			})
		}
	}
	return diffs, nil
}

func decodeTOMLMap(raw []byte) (map[string]any, error) {
	doc := map[string]any{}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return doc, nil
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal toml: %w", err)
	}
	return doc, nil
}

func lookupTOMLValue(doc map[string]any, section, key string) (any, bool) {
	if section == "" {
		v, ok := doc[key]
		return v, ok
	}
	sec, ok := doc[section].(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := sec[key]
	return v, ok
}

func formatTOMLValue(v any, present bool) string {
	if !present || v == nil {
		return "<unset>"
	}
	if list, ok := v.([]any); ok {
		parts := make([]string, len(list))
		for i, item := range list {
			parts[i] = fmt.Sprint(item)
		}
		return "[" + strings.Join(slices.Clone(parts), ", ") + "]"
	}
	return fmt.Sprint(v)
}
