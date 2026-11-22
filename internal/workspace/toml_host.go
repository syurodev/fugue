package workspace

import (
	"slices"
	"strings"
)

const (
	vendorClaude = "claude"
	vendorCodex  = "codex"
	vendorAgy    = "agy"
)

func validateHost(file string, host *rawHost, homeDir string) error {
	if host == nil {
		return nil
	}
	return firstErr(
		validatePathField(file, "host.workspaces_dir", host.WorkspacesDir, homeDir),
		validateByteSizeField(file, "host.output_retention_total", host.OutputRetentionTotal),
		validateByteSizeField(file, "host.seen_versions_per_session", host.SeenVersionsPerSession),
		validateByteSizeField(file, "host.snapshot_disk", host.SnapshotDisk),
		validateIntMin(file, "host.per_account_sessions", host.PerAccountSessions, 1),
		validateIntMin(file, "host.max_sessions", host.MaxSessions, 1),
		validateIntMin(file, "host.heavy_commands", host.HeavyCommands, 1),
		validateIntRange(file, "host.heavy_slot_max_hold_seconds", host.HeavySlotMaxHoldSeconds, 0, maxHeavySlotHoldSeconds),
		validateIntRange(file, "host.prompt_pacing_min_seconds", host.PromptPacingMinSeconds, 0, maxPromptPacingSeconds),
		validateIntRange(file, "host.prompt_pacing_max_seconds", host.PromptPacingMaxSeconds, 0, maxPromptPacingSeconds),
		validatePacingBounds(file, host.PromptPacingMinSeconds, host.PromptPacingMaxSeconds),
		validateAccountStrategy(file, host.AccountStrategy),
		validateExtraAccounts(file, host.ExtraAccounts),
	)
}

func validateAccountStrategy(file string, strategy *string) error {
	if strategy == nil || *strategy == "" {
		return nil
	}
	return errIf(!slices.Contains([]string{"order", "best", "consumeFirst"}, *strategy), file, "host.account_strategy", "account_strategy must be order, best or consumeFirst")
}

func validateExtraAccounts(file string, extra map[string][]string) error {
	for vendorName, ids := range extra {
		if !slices.Contains([]string{vendorClaude, vendorCodex, vendorAgy}, vendorName) {
			return errIf(true, file, "host.extra_accounts."+vendorName, "unknown vendor in host.extra_accounts")
		}
		for _, id := range ids {
			if !isValidExtraAccountID(id) {
				return errIf(true, file, "host.extra_accounts."+vendorName, "invalid extra account ID in host.extra_accounts")
			}
		}
	}
	return nil
}

func isValidExtraAccountID(id string) bool {
	if id == "" || len(id) > 64 || id == "default" || strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return false
	}
	for i := range len(id) {
		if !isValidAccountIDByte(i, id[i]) {
			return false
		}
	}
	return true
}

func isValidAccountIDByte(pos int, c byte) bool {
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return true
	}
	return pos > 0 && (c == '.' || c == '_' || c == '-')
}
