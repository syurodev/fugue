package codex

import (
	"encoding/json"
	"path/filepath"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
)

// Bridge wiring for the Fugue MCP tools (vendor-runtime §12). Codex reads its
// MCP servers from the user's config.toml or from `-c key=value` overrides on
// argv. Fugue appends overrides only: it never writes a file, and its flags
// are not profile input, so no profile can remove or override the bridge.
//
// The session token travels by name, not by value: `env_vars` lists variable
// names Codex copies from its own process environment into the bridge process
// (measured on codex-cli 0.167.0, 2026-11-16), so the token is in neither
// argv nor any file. The overrides beat the user's config.toml key for key,
// including `enabled`.

// BridgeServerName is the MCP server name Codex sees; its tools arrive in
// `mcp_tool_call` items as fugue/<tool>.
const BridgeServerName = "fugue"

// bridgeConfigOverrides renders the `-c` overrides that make Codex spawn
// `fugue mcp-bridge` with the host socket. Both paths are host values, never
// client or profile input.
func bridgeConfigOverrides(bridge vendor.Bridge) ([]string, error) {
	if !filepath.IsAbs(bridge.Binary) || !filepath.IsAbs(bridge.Socket) {
		return nil, errcode.New(errcode.InvalidArgument, "codex: the mcp bridge needs absolute binary and socket paths")
	}
	command, err := json.Marshal(bridge.Binary)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "codex: encode the mcp bridge command", err)
	}
	args, err := json.Marshal([]string{"mcp-bridge", "--socket", bridge.Socket})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "codex: encode the mcp bridge arguments", err)
	}
	envVars, err := json.Marshal([]string{vendor.SessionTokenEnv})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "codex: encode the mcp bridge env vars", err)
	}
	const key = "mcp_servers." + BridgeServerName + "."
	return []string{
		"-c", key + "command=" + string(command),
		"-c", key + "args=" + string(args),
		"-c", key + "env_vars=" + string(envVars),
		"-c", key + "enabled=true",
	}, nil
}
