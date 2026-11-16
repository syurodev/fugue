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
// `slashcmd.Expand`, which looks up `ModelFacingText` on the registered
// resolvers. Among the 13 advertised commands, `/compact` has no
// `ModelFacingText` resolver in `agy acp` and is handled only in the interactive
// TUI (`jetski/cli/commands`), so `compact` is hidden from the session's slash
// command list along with all interactive TUI-only commands (`add-dir`,
// `agents`, `artifact`, `branch`, `btw`, `changelog`, `clear`, `codesearch`,
// `config`, `context`, `conversation`, `copy`, `credits`, `cs`, `dictate`,
// `diff`, `effort`, `exit`, `feedback`, `fork`, `help`, `hooks`, `keybindings`,
// `login`, `logout`, `mcp`, `model`, `new`, `open`, `permissions`, `plugin`,
// `projects`, `quit`, `quota`, `remote-control`, `rename`, `resume`, `rewind`,
// `search`, `settings`, `skills`, `statusline`, `switch`, `tasks`, `theme`,
// `title`, `undo`, `usage`, `vim`).
//
// The `/fugue:` namespace is reserved by the host and rejected before reaching
// `agy` (`Session.Send` -> `vendor.ValidateInput`).

import (
	"slices"
	"strings"
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

// hideTerminalCommands filters terminal-only commands from the vendor's
// `available_commands_update` list while preserving vendor order.
func hideTerminalCommands(commands []AvailableCommand) []string {
	out := make([]string, 0, len(commands))
	seen := make(map[string]bool, len(commands))
	for _, cmd := range commands {
		bare := strings.TrimPrefix(strings.TrimSpace(cmd.Name), "/")
		if IsTerminalSlashCommand(bare) || seen[bare] {
			continue
		}
		seen[bare] = true
		out = append(out, bare)
	}
	return out
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
