package agy

// Slash command list normalization for `agy acp` (VR-E10, vendor-runtime §9).
//
// In `agy 1.5.9`, `session/update` notifications with `sessionUpdate:
// "available_commands_update"` advertise 13 slash commands on startup:
//   - Built-in system commands (`slashcommands.GetSystemSlashCommandsDefinitions`):
//     `goal`, `compact`, `schedule`, `browser`, `plan`, `grill-me`, `teamwork`,
//     `learn`, `boost`.
//   - Built-in skill commands (`slashcmd.AdvertisedResolvers`):
//     `agy-customizations`, `antigravity-guide`, `run-workflow`, `swarm`.
//
// `agy acp` expands prompt slash commands via `acp.expandSlashCommands` ->
// `slashcmd.Expand`; `/compact` and the 49 TUI-only commands from
// `jetski/cli/commands` have no `ModelFacingText` resolver and are hidden.
// `/fugue:` is reserved by the host and rejected before reaching `agy`.

import (
	"slices"
	"strings"
	"unicode"
)

const (
	maxSlashCommands    = 128
	maxSlashCommandName = 64
	fugueSlashPrefix    = "fugue:"
)

// IsTerminalSlashCommand reports whether name is a terminal-only command in
// `agy 1.5.9` that must be hidden from non-TUI ACP sessions.
func IsTerminalSlashCommand(name string) bool {
	bare := strings.TrimPrefix(strings.TrimSpace(name), "/")
	switch bare {
	case "", "add-dir", "agents", "artifact", "branch", "btw", "changelog", "clear",
		"codesearch", "compact", "config", "context", "conversation", "copy", "credits",
		"cs", "dictate", "diff", "effort", "exit", "feedback", "fork", "help", "hooks",
		"keybindings", "login", "logout", "mcp", "model", "new", "open", "permissions",
		"plugin", "projects", "quit", "quota", "remote-control", "rename", "resume",
		"rewind", "search", "settings", "skills", "statusline", "switch", "tasks",
		"theme", "title", "undo", "usage", "vim":
		return true
	default:
		return false
	}
}

// hideTerminalCommands filters terminal-only, `fugue:` (case-insensitive),
// empty, oversized and whitespace, control, or format-character names from the
// vendor's untrusted `available_commands_update` list, keeping vendor order and
// at most maxSlashCommands names.
func hideTerminalCommands(commands []AvailableCommand) []string {
	out := make([]string, 0, min(len(commands), maxSlashCommands))
	seen := make(map[string]bool, len(commands))
	for _, cmd := range commands {
		bare := strings.TrimPrefix(strings.TrimSpace(cmd.Name), "/")
		if len(out) == maxSlashCommands {
			break
		}
		if len(bare) > maxSlashCommandName || hasFoldPrefix(bare, fugueSlashPrefix) || IsTerminalSlashCommand(bare) || seen[bare] ||
			strings.IndexFunc(bare, isDisallowedSlashRune) >= 0 {
			continue
		}
		seen[bare] = true
		out = append(out, bare)
	}
	return out
}

func hasFoldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func isDisallowedSlashRune(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

func (n *Normalizer) normalizeAvailableCommands(wire *UpdateWire) Observation {
	if wire.AvailableCommands == nil {
		return n.unsupported(n.lineRef(), "acp:"+UpdateAvailableCommands)
	}
	n.slashCommands = hideTerminalCommands(wire.AvailableCommands)
	return Observation{
		Init: &Init{
			Version:        n.version,
			SessionID:      n.acpSessionID,
			Model:          n.model,
			PermissionMode: n.mode,
			SlashCommands:  slices.Clone(n.slashCommands),
		},
	}
}

// SlashCommands returns a copy of the latest non-terminal slash commands seen
// on the session's `available_commands_update` notifications, in vendor order.
func (n *Normalizer) SlashCommands() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.slashCommands)
}

// SlashCommands returns the normalizer's latest vendor-order command list.
func (s *Session) SlashCommands() []string {
	return s.normalizer.SlashCommands()
}
